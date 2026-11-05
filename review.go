package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gotk3/gotk3/cairo"
	"github.com/gotk3/gotk3/gdk"
	"github.com/gotk3/gotk3/gtk"
)

type reviewLine struct {
	Old      int    `json:"old,omitempty"`
	New      int    `json:"new,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Text     string `json:"text"`
	Reviewed bool   `json:"reviewed"`
}

type reviewComment struct {
	ID    string       `json:"id"`
	Text  string       `json:"text"`
	Lines []reviewLine `json:"lines,omitempty"`
}

type reviewFile struct {
	Reviewed bool                  `json:"reviewed"`
	Lines    map[string]reviewLine `json:"lines,omitempty"`
	Comments []reviewComment       `json:"comments,omitempty"`
}

type commitReview struct {
	Repository string                 `json:"repository"`
	Commit     string                 `json:"commit"`
	Comparison string                 `json:"comparison"`
	Parents    []string               `json:"parents,omitempty"`
	Subject    string                 `json:"subject,omitempty"`
	Author     string                 `json:"author,omitempty"`
	Date       string                 `json:"date,omitempty"`
	Files      map[string]*reviewFile `json:"files"`
}

func loadCommitReview(repo *repository, row historyRow, combined bool) (*commitReview, string, error) {
	if row.kind != "commit" {
		return nil, "", nil
	}
	comparison := "first-parent"
	if combined {
		comparison = "merge-resolution"
	}
	directory, err := reviewDataDirectory()
	if err != nil {
		return nil, "", err
	}
	root := repo.path
	identity := sha256.Sum256([]byte(root))
	path := filepath.Join(directory, fmt.Sprintf("%x", identity[:16]), row.revision+"-"+comparison+".json")
	seed := &commitReview{Repository: root, Commit: row.revision, Comparison: comparison, Parents: row.parents, Subject: row.subject, Author: row.author, Date: row.date}
	review, err := readCommitReview(path, seed)
	return review, path, err
}

func reviewDataDirectory() (string, error) {
	data := os.Getenv("XDG_DATA_HOME")
	if !filepath.IsAbs(data) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		data = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(data, "giti", "reviews"), nil
}

func lockReviewData(path string, flags int) (*os.File, error) {
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), flags); err != nil {
		lock.Close()
		return nil, err
	}
	return lock, nil
}

func cleanupCommitReviews(directory string, now time.Time) error {
	lock, err := lockReviewData(filepath.Join(directory, ".cleanup.lock"), syscall.LOCK_EX|syscall.LOCK_NB)
	if os.IsNotExist(err) || err == syscall.EWOULDBLOCK {
		return nil
	}
	if err != nil {
		return err
	}
	defer lock.Close()
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	marker := filepath.Join(directory, ".last-cleanup.json")
	if data, err := os.ReadFile(marker); err == nil {
		var last time.Time
		if json.Unmarshal(data, &last) == nil && now.Sub(last) < 24*time.Hour {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	cutoff := now.Add(-30 * 24 * time.Hour)
	err = filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !entry.Type().IsRegular() || filepath.Dir(path) == directory || !strings.HasSuffix(path, ".json") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.ModTime().Before(cutoff) {
			return nil
		}
		fileLock, err := lockReviewData(path+".lock", syscall.LOCK_EX|syscall.LOCK_NB)
		if err == syscall.EWOULDBLOCK {
			return nil // A writer is updating this review; check it on the next cleanup.
		}
		if err != nil {
			return err
		}
		defer fileLock.Close()
		defer syscall.Flock(int(fileLock.Fd()), syscall.LOCK_UN)
		// Recheck under the same lock as saves, which may have refreshed the file.
		info, err = os.Lstat(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() && info.ModTime().Before(cutoff) {
			return os.Remove(path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Keep lock files so waiting writers continue to coordinate on the same inode.
	return writeJSONAtomic(marker, now)
}

func saveCommitReview(path string, seed *commitReview, change func(*commitReview)) (*commitReview, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	lock, err := lockReviewData(path+".lock", syscall.LOCK_EX)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	// Reload under the lock to preserve updates from other windows.
	review, err := readCommitReview(path, seed)
	if err != nil {
		return nil, err
	}
	change(review)
	for path, file := range review.Files {
		for key, line := range file.Lines {
			if line.Reviewed == (file.Reviewed && line.changed()) {
				delete(file.Lines, key)
			}
		}
		if !file.Reviewed && len(file.Lines) == 0 && len(file.Comments) == 0 {
			delete(review.Files, path)
		}
	}
	return review, writeJSONAtomic(path, review)
}

func (review *commitReview) file(path string) *reviewFile {
	file := review.Files[path]
	if file == nil {
		file = &reviewFile{}
		review.Files[path] = file
	}
	if file.Lines == nil {
		file.Lines = make(map[string]reviewLine)
	}
	return file
}

func readCommitReview(path string, seed *commitReview) (*commitReview, error) {
	contents, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		review := *seed
		review.Files = make(map[string]*reviewFile)
		return &review, nil
	}
	if err != nil {
		return nil, err
	}
	var review commitReview
	if err = json.Unmarshal(contents, &review); err != nil {
		return nil, err
	}
	if review.Repository != seed.Repository || review.Commit != seed.Commit || review.Comparison != seed.Comparison || review.Files == nil {
		return nil, fmt.Errorf("invalid review metadata in %s", path)
	}
	// Describe the commit from Git rather than trusting older saved notes.
	review.Subject, review.Author, review.Date = seed.Subject, seed.Author, seed.Date
	for _, file := range review.Files {
		if file == nil {
			return nil, fmt.Errorf("invalid file review in %s", path)
		}
		// Derive keys from anchors, including marks saved with the earlier hashed keys.
		marks := file.Lines
		file.Lines = make(map[string]reviewLine, len(marks))
		for _, line := range marks {
			file.Lines[reviewLineKey(line)] = line
		}
	}
	return &review, nil
}

func (display displayLine) reviewAnchor() reviewLine {
	kind := display.tag
	if kind == "" {
		kind = "context"
	}
	return reviewLine{Old: display.old, New: display.new, Kind: kind, Text: strings.TrimSuffix(display.text, "\n")}
}

func reviewLineKey(line reviewLine) string {
	// The commit and comparison are immutable; line coordinates identify the content.
	return fmt.Sprintf("%s:%d:%d", line.Kind, line.Old, line.New)
}

func (line reviewLine) changed() bool {
	return line.Kind == "added" || line.Kind == "removed"
}

type reviewState int

const (
	reviewNone reviewState = iota
	reviewPartial
	reviewDone
	reviewCommented
)

// File list marks share one column; the comment blue matches commented diff lines.
var reviewStates = [...]struct{ mark, file, folder string }{
	{"", "Not reviewed", "No changed files in this folder reviewed"},
	{`<span foreground="#9ca3af">◐</span>`, "Partially reviewed", "Some changed files in this folder remain unreviewed"},
	{`<span foreground="#2e8c47">✓</span>`, "Reviewed — good to go", "All changed files in this folder reviewed"},
	{`<span foreground="#2563eb">✓</span>`, "Reviewed with comments", "All changed files in this folder reviewed; some have comments"},
}

// A comment reviews the lines it covers until the comment is deleted.
func (file *reviewFile) commented() map[string]bool {
	keys := make(map[string]bool)
	if file != nil {
		for _, comment := range file.Comments {
			for _, line := range comment.Lines {
				keys[reviewLineKey(line)] = true
			}
		}
	}
	return keys
}

func (file *reviewFile) lineReviewed(line reviewLine, commented map[string]bool) bool {
	key := reviewLineKey(line)
	if mark, ok := file.Lines[key]; ok && !commented[key] {
		return mark.Reviewed
	}
	return commented[key] || file.Reviewed && line.changed()
}

// reviewedLines counts changed lines reviewed by the file mark, line marks, or comments.
func (file *reviewFile) reviewedLines(total int) int {
	if file == nil {
		return 0
	}
	count, commented, lines := 0, file.commented(), make(map[string]reviewLine)
	if file.Reviewed {
		count = total
	}
	for key, line := range file.Lines {
		lines[key] = line
	}
	for _, comment := range file.Comments {
		for _, line := range comment.Lines {
			lines[reviewLineKey(line)] = line
		}
	}
	for _, line := range lines {
		if line.changed() && file.lineReviewed(line, commented) != file.Reviewed {
			if file.Reviewed {
				count--
			} else {
				count++
			}
		}
	}
	return max(0, min(count, total))
}

func (file *reviewFile) state(total int) reviewState {
	if file == nil {
		return reviewNone
	}
	reviewed := file.reviewedLines(total)
	switch {
	case reviewed == total && (total > 0 || file.Reviewed):
		if len(file.Comments) > 0 {
			return reviewCommented
		}
		return reviewDone
	case reviewed > 0 || len(file.Comments) > 0:
		return reviewPartial
	}
	return reviewNone
}

func (review *commitReview) state(changed changedFile) reviewState {
	return review.Files[changed.path].state(changed.additions + changed.deletions)
}

func (review *commitReview) progress(files []changedFile) (reviewed, total int, states [4]int) {
	for _, changed := range files {
		lines := changed.additions + changed.deletions
		total += lines
		reviewed += review.Files[changed.path].reviewedLines(lines)
		states[review.state(changed)]++
	}
	return
}

// reviewState uses whitespace-inclusive totals, so hidden whitespace lines still need review.
func (app *giti) reviewState(path string) reviewState {
	for _, changed := range app.reviewFiles {
		if changed.path == path {
			return app.review.state(changed)
		}
	}
	return app.review.Files[path].state(0)
}

func (app *giti) changeReview(change func(*commitReview)) bool {
	if app.review == nil {
		return false
	}
	review, err := saveCommitReview(app.reviewPath, app.review, change)
	if err != nil {
		app.showError(fmt.Errorf("save review: %w", err))
		return false
	}
	app.review = review
	app.updateReviewUI()
	if app.diffStack.GetVisibleChildName() == "review-preview" {
		app.showReviewPreview()
	}
	return true
}

func (app *giti) toggleFileReviewed(path string) {
	complete := app.reviewState(path) >= reviewDone
	app.changeReview(func(review *commitReview) {
		// Comments survive unmarking, so their lines keep the file partially reviewed.
		file := review.file(path)
		file.Reviewed, file.Lines = !complete, make(map[string]reviewLine)
	})
}

func (app *giti) selectedReviewLines() []reviewLine {
	if !app.reviewActive || app.currentFile == nil || !app.diffLoaded {
		return nil
	}
	start, end, selected := app.diffBuffer.GetSelectionBounds()
	first, last := app.diffContextLine, app.diffContextLine
	if selected {
		first, last = start.GetLine(), end.GetLine()
		if end.GetLineOffset() == 0 && last > first {
			last--
		}
	}
	return reviewAnchors(app.reviewLines, max(0, first), last+1)
}

func reviewAnchors(lines []displayLine, first, end int) []reviewLine {
	var result []reviewLine
	for _, line := range lines[min(first, len(lines)):min(max(first, end), len(lines))] {
		if line.old > 0 || line.new > 0 {
			result = append(result, line.reviewAnchor())
		}
	}
	return result
}

func (app *giti) markReviewLines(lines []reviewLine, reviewed bool) {
	if app.currentFile == nil || len(lines) == 0 {
		return
	}
	path := app.currentFile.path
	// The marks now show the result; a leftover selection would hide them.
	if _, end, selected := app.diffBuffer.GetSelectionBounds(); selected {
		app.diffBuffer.PlaceCursor(end)
	}
	app.changeReview(func(review *commitReview) {
		file := review.file(path)
		for _, line := range lines {
			line.Reviewed = reviewed
			file.Lines[reviewLineKey(line)] = line
		}
	})
}

// reviewFileTarget finds the next listed file, wrapping to the start; while
// reviewing it skips files that are already reviewed.
func (app *giti) reviewFileTarget() *gtk.TreePath {
	type listed struct {
		tree string
		done bool
	}
	var files []listed
	current := -1
	app.fileModel.ForEach(func(model *gtk.TreeModel, path *gtk.TreePath, iter *gtk.TreeIter) bool {
		value, _ := model.GetValue(iter, fileIndexColumn)
		raw, _ := value.GoValue()
		if index, ok := raw.(int); ok && index >= 0 && index < len(app.files) {
			if app.currentFile != nil && app.files[index].path == app.currentFile.path {
				current = len(files)
			}
			files = append(files, listed{path.String(), app.reviewActive && app.review != nil && app.reviewState(app.files[index].path) >= reviewDone})
		}
		return false
	})
	for step := 1; step <= len(files); step++ {
		if index := (current + step) % len(files); index != current && !files[index].done {
			return must(gtk.TreePathNewFromString(files[index].tree))
		}
	}
	return nil
}

// updateReviewFileActions refreshes the file row: name, counts, review action, and next file.
func (app *giti) updateReviewFileActions() {
	file := app.currentFile
	app.headerFileName.SetMarkup(`<span foreground="#9ca3af">No file selected</span>`)
	app.headerFileName.SetTooltipText("")
	if path := app.openFilePath(); path != "" {
		app.headerFileName.SetMarkup("<b>" + html.EscapeString(filepath.Base(path)) + "</b>")
		app.headerFileName.SetTooltipText(path)
	}
	// Unchanged files have no diff for these to adjust.
	diffControls := app.plainFile == ""
	app.fullFileToggle.SetVisible(diffControls)
	app.whitespaceToggle.SetVisible(diffControls)
	app.fullMergeToggle.SetVisible(diffControls && app.currentRow != nil && app.currentRow.kind == "commit" && len(app.currentRow.parents) > 1)
	// Untracked and binary files have no line counts; working-tree views list them beside each file already.
	app.fileStatistics.SetVisible(file != nil && !file.binary && file.status != "??" && app.currentRow != nil && app.currentRow.kind == "commit")
	if file != nil {
		app.fileStatisticLabels[0].SetText(fmt.Sprintf("+%d", file.additions))
		app.fileStatisticLabels[1].SetText(fmt.Sprintf("−%d", file.deletions))
		for _, label := range app.fileStatisticLabels {
			label.Show()
		}
	}
	visible := app.review != nil && app.reviewActive && app.diffStack.GetVisibleChildName() == "diff"
	app.reviewFileButton.SetVisible(visible)
	app.reviewLinesLabel.SetVisible(visible)
	app.reviewFileButton.SetSensitive(visible && file != nil && app.diffLoaded)
	app.reviewCommentItem.SetSensitive(app.reviewFileButton.GetSensitive())
	next := app.reviewFileTarget()
	app.reviewNextButton.SetSensitive(next != nil)
	app.reviewNextButton.SetTooltipText(map[bool]string{false: "Next file", true: "Next file to review"}[app.reviewActive])
	if app.reviewActive && next == nil {
		app.reviewNextButton.SetTooltipText("All files reviewed")
	}
	app.reviewFileButton.SetLabel("Mark _Reviewed")
	app.reviewFileButton.SetTooltipText("Mark this whole file reviewed, including whitespace changes (Alt+R)")
	app.reviewLinesLabel.SetText("")
	if !visible || file == nil {
		return
	}
	if app.reviewState(file.path) >= reviewDone {
		app.reviewFileButton.SetLabel("Unmark _Reviewed")
		app.reviewFileButton.SetTooltipText("Clear this file’s review marks; comments stay (Alt+R)")
	}
	for _, changed := range app.reviewFiles {
		if changed.path == file.path {
			total := changed.additions + changed.deletions
			app.reviewLinesLabel.SetText(fmt.Sprintf("%d/%d lines", app.review.Files[changed.path].reviewedLines(total), total))
		}
	}
}

func (app *giti) updateReviewUI() {
	if app.reviewModeButton == nil {
		return
	}
	available := app.review != nil
	active := available && app.reviewActive
	app.reviewSessionActions.SetVisible(available)
	app.reviewColumn.SetVisible(available)
	app.reviewFilesLabel.SetVisible(active)
	app.reviewModeButton.SetLabel("Review")
	app.reviewModeButton.SetTooltipText("Review this commit; saved marks and comments are restored")
	if active {
		app.reviewModeButton.SetLabel("Exit Review")
		app.reviewModeButton.SetTooltipText("Return to browsing; your marks and comments are saved")
	}
	comments, reviewedLines, totalLines, hasContent := 0, 0, 0, false
	app.reviewStateCounts = [4]int{}
	directories := make(map[string]reviewState)
	if available {
		for _, file := range app.review.Files {
			comments += len(file.Comments)
			hasContent = hasContent || file.Reviewed || len(file.Lines) > 0 || len(file.Comments) > 0
		}
		reviewedLines, totalLines, app.reviewStateCounts = app.review.progress(app.reviewFiles)
		for _, changed := range app.reviewFiles {
			state := app.review.state(changed)
			// Folders show their weakest descendant: complete only when every file is.
			for directory := filepath.Dir(changed.path); directory != "."; directory = filepath.Dir(directory) {
				if folder, exists := directories[directory]; !exists || folder >= reviewDone && state >= reviewDone {
					directories[directory] = max(folder, state)
				} else if folder > reviewNone || state > reviewNone {
					directories[directory] = reviewPartial
				}
			}
		}
	}
	for _, item := range []*gtk.MenuItem{app.reviewPreviewItem, app.reviewExportItem, app.reviewCopyItem} {
		item.SetSensitive(hasContent)
	}
	app.reviewSummaryItem.SetLabel(fmt.Sprintf("%d of %d lines reviewed · %s", reviewedLines, totalLines, plural(comments, "comment")))
	complete := app.reviewStateCounts[reviewDone] + app.reviewStateCounts[reviewCommented]
	app.reviewFilesLabel.SetText(fmt.Sprintf("%d/%d files", complete, len(app.reviewFiles)))
	app.reviewFilesLabel.SetTooltipText("Files reviewed in this commit")
	if complete == len(app.reviewFiles) && complete > 0 {
		app.reviewFilesLabel.SetMarkup(fmt.Sprintf(`<span foreground="#2e8c47">✓ %d/%d files</span>`, complete, complete))
		app.reviewFilesLabel.SetTooltipText("Review complete: every changed file is reviewed")
	}
	app.reviewProgressBar.SetTooltipText(fmt.Sprintf("%d good to go · %d with comments · %d partial · %d not reviewed", app.reviewStateCounts[reviewDone], app.reviewStateCounts[reviewCommented], app.reviewStateCounts[reviewPartial], app.reviewStateCounts[reviewNone]))
	app.reviewProgressBar.QueueDraw()
	for _, store := range []struct {
		model *gtk.TreeModel
		set   func(*gtk.TreeIter, int, any) error
	}{{&app.fileStore.TreeModel, app.fileStore.SetValue}, {&app.fileTreeStore.TreeModel, app.fileTreeStore.SetValue}} {
		store.model.ForEach(func(model *gtk.TreeModel, _ *gtk.TreePath, iter *gtk.TreeIter) bool {
			value, _ := model.GetValue(iter, fileIndexColumn)
			index, _ := value.GoValue()
			fileIndex, ok := index.(int)
			state := reviewNone
			if available && ok {
				if fileIndex >= 0 && fileIndex < len(app.files) {
					state = app.reviewState(app.files[fileIndex].path)
				} else if fileIndex < -1 {
					state = directories[app.fileDirectoryReviewPaths[fileIndex]]
				}
			}
			store.set(iter, fileReviewMarkColumn, reviewStates[state].mark)
			return false
		})
	}
	start, end := app.diffBuffer.GetBounds()
	for _, tag := range []string{"reviewed-added", "reviewed-removed", "reviewed-context", "review-comment"} {
		app.diffBuffer.RemoveTagByName(tag, start, end)
	}
	app.overviewComments = app.overviewComments[:0]
	if active && app.currentFile != nil {
		file := app.review.Files[app.currentFile.path]
		commented := file.commented()
		if draft := app.reviewDraft; draft != nil && draft.path == app.currentFile.path && draft.reviewPath == app.reviewPath {
			for _, line := range draft.comment.Lines {
				commented[reviewLineKey(line)] = true
			}
		}
		for index, display := range app.reviewLines {
			line := display.reviewAnchor()
			if line.Old == 0 && line.New == 0 {
				continue
			}
			tag := ""
			if commented[reviewLineKey(line)] {
				tag = "review-comment"
				app.overviewComments = append(app.overviewComments, index)
			} else if file != nil && file.lineReviewed(line, commented) {
				tag = "reviewed-context"
				if line.changed() {
					tag = "reviewed-" + line.Kind
				}
			}
			if tag != "" {
				app.diffBuffer.ApplyTagByName(tag, app.diffBuffer.GetIterAtLine(index), app.diffBuffer.GetIterAtLine(index+1))
			}
		}
	}
	app.diffOverview.QueueDraw()
	app.fileView.QueueDraw()
	app.rebuildReviewCards()
	app.placeReviewFloaters()
	app.updateReviewFileActions()
}

func plural(count int, noun string) string {
	if count != 1 {
		noun += "s"
	}
	return fmt.Sprintf("%d %s", count, noun)
}

// reviewRange names anchors by new-file lines, or by old-file lines for pure removals.
func reviewRange(lines []reviewLine) (text string, removed bool) {
	for _, removed := range []bool{false, true} {
		first, last := 0, 0
		for _, line := range lines {
			number := line.New
			if removed {
				number = line.Old
			}
			if number > 0 && (first == 0 || number < first) {
				first = number
			}
			last = max(last, number)
		}
		if first == last && first > 0 {
			return strconv.Itoa(first), removed
		} else if first > 0 {
			return fmt.Sprintf("%d-%d", first, last), removed
		}
	}
	return "", false
}

type reviewDraft struct {
	reviewPath, path string
	comment          reviewComment
	buffer           *gtk.TextBuffer
}

type reviewCard struct {
	widget       *gtk.EventBox
	line, height int // line is the diff line above the card; -1 places it above the diff.
}

const reviewCardGap = 6

func (app *giti) startReviewDraft(path string, comment reviewComment) {
	if draft := app.reviewDraft; draft != nil {
		start, end := draft.buffer.GetBounds()
		if text, _ := draft.buffer.GetText(start, end, true); strings.TrimSpace(text) != "" && text != draft.comment.Text {
			app.showNotification("Save or cancel the open comment first.", 3*time.Second)
			if app.reviewDraftView != nil {
				app.reviewDraftView.GrabFocus()
			}
			return
		}
	}
	buffer := must(gtk.TextBufferNew(nil))
	buffer.SetText(comment.Text)
	buffer.Connect("changed", func() {
		start, end := buffer.GetBounds()
		text, _ := buffer.GetText(start, end, true)
		if app.reviewDraftSave != nil {
			app.reviewDraftSave.SetSensitive(strings.TrimSpace(text) != "")
		}
		addMainSource(0, func() bool { app.layoutReviewCards(); return false })
	})
	app.reviewDraft = &reviewDraft{app.reviewPath, path, comment, buffer}
	// The draft's lines take the comment tint, so the selection is no longer needed.
	if start, _, selected := app.diffBuffer.GetSelectionBounds(); selected {
		app.diffBuffer.PlaceCursor(start)
	}
	app.updateReviewUI()
	if app.reviewDraftView != nil {
		app.reviewDraftView.GrabFocus()
		if len(comment.Lines) == 0 {
			app.diffScroller.GetVAdjustment().SetValue(0)
		}
	}
}

func (app *giti) saveReviewDraft() {
	draft := app.reviewDraft
	if draft == nil {
		return
	}
	start, end := draft.buffer.GetBounds()
	text, _ := draft.buffer.GetText(start, end, true)
	if strings.TrimSpace(text) == "" {
		return
	}
	comment := draft.comment
	comment.Text = text
	if comment.ID == "" {
		comment.ID = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	app.reviewDraft = nil
	if !app.changeReview(func(review *commitReview) {
		file := review.file(draft.path)
		for index := range file.Comments {
			if file.Comments[index].ID == comment.ID {
				file.Comments[index] = comment
				return
			}
		}
		file.Comments = append(file.Comments, comment)
	}) {
		app.reviewDraft = draft
		app.updateReviewUI()
	}
	app.diffView.GrabFocus()
}

func (app *giti) cancelReviewDraft() {
	app.reviewDraft = nil
	app.updateReviewUI() // Drops the draft's comment tint along with its card.
	app.diffView.GrabFocus()
}

func (app *giti) deleteReviewComment(path, id string) {
	app.changeReview(func(review *commitReview) {
		file := review.file(path)
		for index, comment := range file.Comments {
			if comment.ID == id {
				file.Comments = append(file.Comments[:index], file.Comments[index+1:]...)
				return
			}
		}
	})
}

func reviewButton(label, class string, clicked func()) *gtk.Button {
	button := must(gtk.ButtonNewWithLabel(label))
	button.SetRelief(gtk.RELIEF_NONE)
	context, _ := button.GetStyleContext()
	context.AddClass(class)
	button.Connect("clicked", clicked)
	return button
}

// Comment cards float in the diff's text window over space reserved below their
// last line, so buffer lines, line numbers, copying, and find stay unchanged.
func (app *giti) rebuildReviewCards() {
	for _, card := range app.reviewCards {
		card.widget.Destroy()
	}
	app.reviewCards, app.reviewDraftView, app.reviewDraftSave = app.reviewCards[:0], nil, nil
	if app.reviewActive && app.review != nil && app.currentFile != nil && app.diffLoaded {
		path := app.currentFile.path
		positions := make(map[string]int)
		for index, display := range app.reviewLines {
			if display.old > 0 || display.new > 0 {
				positions[reviewLineKey(display.reviewAnchor())] = index
			}
		}
		draft := app.reviewDraft
		if draft != nil && (draft.path != path || draft.reviewPath != app.reviewPath) {
			draft = nil
		}
		var comments []reviewComment
		if file := app.review.Files[path]; file != nil {
			comments = append(comments, file.Comments...)
		}
		if draft != nil && draft.comment.ID == "" {
			comments = append(comments, draft.comment)
		}
		for _, comment := range comments {
			line, shown := -1, len(comment.Lines) == 0
			for _, anchor := range comment.Lines {
				if index, ok := positions[reviewLineKey(anchor)]; ok {
					line, shown = max(line, index), true
				}
			}
			box := must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 6))
			context, _ := box.GetStyleContext()
			context.AddClass("giti-review-card")
			header := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 4))
			title := "File comment"
			if text, removed := reviewRange(comment.Lines); text != "" {
				title = "Line " + text
				if strings.Contains(text, "-") {
					title = "Lines " + text
				}
				if removed {
					title = "Removed " + strings.ToLower(title)
				}
			}
			if !shown {
				title += " · not shown in this view"
			}
			label := must(gtk.LabelNew(title))
			labelContext, _ := label.GetStyleContext()
			labelContext.AddClass("giti-secondary-label")
			header.PackStart(label, false, false, 0)
			box.PackStart(header, false, false, 0)
			if draft != nil && draft.comment.ID == comment.ID {
				view := must(gtk.TextViewNewWithBuffer(draft.buffer))
				view.SetWrapMode(gtk.WRAP_WORD_CHAR)
				view.SetAcceptsTab(false)
				view.SetLeftMargin(6)
				view.SetRightMargin(6)
				view.SetTopMargin(4)
				view.SetBottomMargin(4)
				view.SetSizeRequest(-1, 56)
				view.Connect("key-press-event", func(_ *gtk.TextView, event *gdk.Event) bool {
					key := gdk.EventKeyNewFromEvent(event)
					if key.KeyVal() == gdk.KEY_Escape {
						app.cancelReviewDraft()
					} else if (key.KeyVal() == gdk.KEY_Return || key.KeyVal() == gdk.KEY_KP_Enter) && key.State()&uint(gdk.CONTROL_MASK) != 0 {
						app.saveReviewDraft()
					} else {
						return false
					}
					return true
				})
				box.PackStart(view, false, false, 0)
				actions := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 4))
				hint := must(gtk.LabelNew("Markdown · Ctrl+Enter to save · Esc to cancel"))
				hintContext, _ := hint.GetStyleContext()
				hintContext.AddClass("giti-secondary-label")
				actions.PackStart(hint, false, false, 0)
				app.reviewDraftSave = reviewButton("Save", "giti-review-primary", app.saveReviewDraft)
				app.reviewDraftSave.SetSensitive(strings.TrimSpace(comment.Text) != "")
				actions.PackEnd(app.reviewDraftSave, false, false, 0)
				actions.PackEnd(reviewButton("Cancel", "giti-review-secondary", app.cancelReviewDraft), false, false, 0)
				box.PackStart(actions, false, false, 0)
				app.reviewDraftView = view
			} else {
				box.PackStart(renderMarkdown(comment.Text), false, false, 0)
				var remove *gtk.Button
				remove = reviewButton("Delete", "giti-review-secondary", func() {
					// Ask again in place rather than interrupting with a dialog.
					if label, _ := remove.GetLabel(); label == "Delete" {
						remove.SetLabel("Confirm Delete")
					} else {
						app.deleteReviewComment(path, comment.ID)
					}
				})
				header.PackEnd(remove, false, false, 0)
				header.PackEnd(reviewButton("Edit", "giti-review-secondary", func() { app.startReviewDraft(path, comment) }), false, false, 0)
			}
			card := must(gtk.EventBoxNew())
			card.Add(box)
			card.ShowAll()
			app.diffView.AddChildInWindow(card, gtk.TEXT_WINDOW_TEXT, 0, 0)
			app.reviewCards = append(app.reviewCards, &reviewCard{widget: card, line: line})
		}
		sort.SliceStable(app.reviewCards, func(i, j int) bool { return app.reviewCards[i].line < app.reviewCards[j].line })
	}
	app.layoutReviewCards()
}

func (app *giti) layoutReviewCards() {
	start, end := app.diffBuffer.GetBounds()
	for _, tag := range app.reviewGapTags {
		app.diffBuffer.RemoveTag(tag, start, end)
	}
	app.reviewGaps = make(map[int]int)
	app.reviewCardWidth = max(240, app.diffView.GetVisibleRect().GetWidth()-24)
	for _, card := range app.reviewCards {
		card.widget.SetSizeRequest(app.reviewCardWidth, -1)
		_, card.height = card.widget.GetPreferredHeightForWidth(app.reviewCardWidth)
		app.reviewGaps[card.line] += card.height + reviewCardGap
	}
	index := 0
	for line, gap := range app.reviewGaps {
		if index == len(app.reviewGapTags) {
			app.reviewGapTags = append(app.reviewGapTags, app.diffBuffer.CreateTag(fmt.Sprintf("review-gap-%d", index), nil))
		}
		tag := app.reviewGapTags[index]
		index++
		app.reviewGaps[line] = gap + reviewCardGap
		above, below, at := 0, gap+reviewCardGap, line
		if line < 0 {
			above, below, at = below, 0, 0
		}
		tag.SetProperty("pixels-above-lines", above)
		tag.SetProperty("pixels-below-lines", below)
		app.diffBuffer.ApplyTag(tag, app.diffBuffer.GetIterAtLine(at), app.diffBuffer.GetIterAtLine(at+1))
	}
	app.positionReviewCards()
	app.diffGutter.QueueDraw()
}

func reviewActionMarkup(label, color, key string) string {
	return `<span foreground="` + color + `" weight="bold">` + label + `</span>  <span foreground="#9ca3af" size="small">` + key + `</span>`
}

// selectionReviewed reports whether every changed line in lines is already reviewed.
func (app *giti) selectionReviewed(lines []reviewLine) bool {
	file := app.review.Files[app.currentFile.path]
	commented := file.commented()
	for _, line := range lines {
		if line.changed() && (file == nil || !file.lineReviewed(line, commented)) {
			return false
		}
	}
	return true
}

// actOnReviewSelection backs the selection toolbar and its C / M shortcuts.
func (app *giti) actOnReviewSelection(comment bool) {
	lines := app.selectedReviewLines()
	if app.currentFile == nil || len(lines) == 0 {
		return
	}
	if comment {
		app.startReviewDraft(app.currentFile.path, reviewComment{Lines: lines})
	} else {
		app.markReviewLines(lines, !app.selectionReviewed(lines))
	}
}

// placeReviewFloaters puts the selection toolbar just below where a selection
// ends, or else the gutter + on the hovered line, following the painted layout.
func (app *giti) placeReviewFloaters() {
	if app.reviewAddButton == nil {
		return
	}
	overlayWidth := app.diffOverlay.GetAllocatedWidth()
	place := func(widget *gtk.Widget, bufferX, bufferY int, position func(x, y, width, height int) (int, int)) {
		windowX, windowY := app.diffView.BufferToWindowCoords(gtk.TEXT_WINDOW_WIDGET, bufferX, bufferY)
		x, y, err := app.diffView.TranslateCoordinates(app.diffOverlay, windowX, windowY)
		if err != nil || windowY < 0 || windowY > app.diffView.GetAllocatedHeight() {
			widget.Hide()
			return
		}
		// Preferred sizes include the margins used for placement, so take them back out.
		_, width := widget.GetPreferredWidth()
		_, height := widget.GetPreferredHeight()
		x, y = position(x, y, width-widget.GetMarginStart(), height-widget.GetMarginTop())
		widget.SetMarginStart(max(0, x))
		widget.SetMarginTop(max(0, y))
		widget.Show()
	}
	// One control at a time: a selection gets the toolbar, otherwise the hovered line gets the +.
	_, _, selected := app.diffBuffer.GetSelectionBounds()
	line := app.reviewHoverLine
	if app.reviewActive && app.currentFile != nil && !app.diffSelecting && !selected && line >= 0 && len(reviewAnchors(app.reviewLines, line, line+1)) > 0 {
		lineY, lineHeight := app.diffView.GetLineYrange(app.diffBuffer.GetIterAtLine(line))
		if lineHeight -= app.reviewGaps[line]; line == 0 {
			lineY, lineHeight = lineY+app.reviewGaps[-1], lineHeight-app.reviewGaps[-1]
		}
		place(&app.reviewAddButton.Widget, 0, lineY, func(x, y, width, height int) (int, int) {
			return x - width/2, y + (lineHeight-height)/2 // Straddle the gutter edge.
		})
	} else {
		app.reviewAddButton.Hide()
	}
	if !selected || !app.reviewActive || app.diffSelecting || len(app.selectedReviewLines()) == 0 {
		app.reviewToolbar.Hide()
		return
	}
	// This runs on every paint; GTK rebuilds a button's label even when it is
	// unchanged, which would repaint forever, so only set a different one.
	markup := reviewActionMarkup("Mark reviewed", "#174d22", "M")
	if app.selectionReviewed(app.selectedReviewLines()) {
		markup = reviewActionMarkup("Mark unreviewed", "#4b5563", "M")
	}
	if markup != app.reviewMarkLabel.GetLabel() {
		app.reviewMarkLabel.SetMarkup(markup)
	}
	// Sit above the selection's first line so the selected lines stay visible;
	// when that would leave the view, drop below its last line instead.
	first, last, _ := app.diffBuffer.GetSelectionBounds()
	above, below := app.diffView.GetIterLocation(first), app.diffView.GetIterLocation(last)
	_, top := app.diffView.BufferToWindowCoords(gtk.TEXT_WINDOW_WIDGET, 0, above.GetY())
	_, height := app.reviewToolbar.GetPreferredHeight()
	anchorY, offset := above.GetY(), -(height-app.reviewToolbar.GetMarginTop())-4
	if top+offset < 0 {
		anchorY, offset = below.GetY()+below.GetHeight(), 4
	}
	place(&app.reviewToolbar.Widget, above.GetX(), anchorY, func(x, y, width, _ int) (int, int) {
		return min(x+8, overlayWidth-width-8), y + offset
	})
}

func (app *giti) positionReviewCards() {
	if len(app.reviewCards) == 0 {
		return
	}
	x, offsets := int(app.diffScroller.GetHAdjustment().GetValue())+12, make(map[int]int)
	for _, card := range app.reviewCards {
		y := 0
		if card.line >= 0 {
			lineY, height := app.diffView.GetLineYrange(app.diffBuffer.GetIterAtLine(card.line))
			y = lineY + height - app.reviewGaps[card.line]
		}
		app.diffView.MoveChild(card.widget, x, y+reviewCardGap+offsets[card.line])
		offsets[card.line] += card.height + reviewCardGap
	}
}

func (review *commitReview) markdown(files []changedFile) string {
	var output strings.Builder
	reviewed, total, states := review.progress(files)
	complete, comments := states[reviewDone]+states[reviewCommented], 0
	for _, file := range review.Files {
		comments += len(file.Comments)
	}
	title := review.Subject
	if title == "" {
		title = "commit " + review.Commit[:min(12, len(review.Commit))]
	}
	fmt.Fprintf(&output, "# Review: %s\n\n- **Commit:** `%s`\n", title, review.Commit)
	if review.Author != "" {
		fmt.Fprintf(&output, "- **Author:** %s", review.Author)
		if review.Date != "" {
			fmt.Fprintf(&output, ", %s", review.Date)
		}
		output.WriteString("\n")
	}
	parents := make([]string, len(review.Parents))
	for index, parent := range review.Parents {
		parents[index] = "`" + parent[:min(12, len(parent))] + "`"
	}
	switch {
	case review.Comparison == "merge-resolution":
		fmt.Fprintf(&output, "- **Compared with:** merge resolution against %s\n", strings.Join(parents, ", "))
	case len(parents) > 0:
		fmt.Fprintf(&output, "- **Compared with:** first parent %s\n", parents[0])
	default:
		output.WriteString("- **Compared with:** empty tree (root commit)\n")
	}
	status := "In progress"
	if complete == len(files) {
		status = "Complete"
	}
	fmt.Fprintf(&output, "- **Repository:** `%s`\n- **Status:** %s — %d of %d files, %d of %d changed lines reviewed · %s\n\n", review.Repository, status, complete, len(files), reviewed, total, plural(comments, "comment"))
	allPaths := make(map[string]bool)
	for _, file := range files {
		allPaths[file.path] = true
	}
	for path := range review.Files {
		allPaths[path] = true
	}
	paths := make([]string, 0, len(allPaths))
	for path := range allPaths {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	if comments > 0 {
		output.WriteString("## Comments\n\n")
	}
	for _, path := range paths {
		file := review.Files[path]
		if file == nil || len(file.Comments) == 0 {
			continue
		}
		fmt.Fprintf(&output, "### `%s`\n\n", path)
		ordered := append([]reviewComment(nil), file.Comments...)
		position := func(comment reviewComment) int {
			if len(comment.Lines) == 0 {
				return 0 // File comments come first.
			}
			return max(comment.Lines[0].New, comment.Lines[0].Old)
		}
		sort.SliceStable(ordered, func(i, j int) bool { return position(ordered[i]) < position(ordered[j]) })
		for _, comment := range ordered {
			if text, removed := reviewRange(comment.Lines); removed {
				fmt.Fprintf(&output, "**`%s` removed lines %s**\n", path, text)
			} else if text != "" {
				fmt.Fprintf(&output, "**`%s:%s`**\n", path, text)
			}
			if len(comment.Lines) > 0 {
				writeReviewExcerpt(&output, comment.Lines)
			}
			output.WriteString("> " + strings.ReplaceAll(strings.TrimRight(comment.Text, "\n"), "\n", "\n> ") + "\n\n")
		}
	}
	output.WriteString("## Files\n\n| File | Status | Comments |\n| --- | --- | --- |\n")
	for _, path := range paths {
		changed := changedFile{path: path}
		for _, file := range files {
			if file.path == path {
				changed = file
			}
		}
		state, count, file := review.state(changed), "", review.Files[path]
		status := reviewStates[state].file
		if state == reviewPartial {
			status += fmt.Sprintf(" (%d of %d lines)", file.reviewedLines(changed.additions+changed.deletions), changed.additions+changed.deletions)
		}
		if file != nil && len(file.Comments) > 0 {
			count = strconv.Itoa(len(file.Comments))
		}
		fmt.Fprintf(&output, "| `%s` | %s | %s |\n", strings.ReplaceAll(path, "|", "\\|"), status, count)
	}
	return output.String()
}

func writeReviewExcerpt(output *strings.Builder, lines []reviewLine) {
	fence := "```"
	for _, line := range lines {
		for strings.Contains(line.Text, fence) {
			fence += "`"
		}
	}
	output.WriteString("\n" + fence + "diff\n")
	for _, line := range lines {
		output.WriteString(line.Text + "\n")
	}
	output.WriteString(fence + "\n\n")
}

func (app *giti) showReviewPreview() {
	if app.review == nil {
		return
	}
	if app.diffFindBox.GetVisible() {
		app.closeDiffFind()
	}
	children := app.reviewDocument.GetChildren()
	children.Foreach(func(child any) { child.(*gtk.Widget).Destroy() })
	children.Free()
	app.reviewDocument.PackStart(renderMarkdown(app.review.markdown(app.reviewFiles)), false, false, 0)
	app.reviewDocument.ShowAll()
	app.diffStack.SetVisibleChildName("review-preview")
	app.updateReviewFileActions()
}

// buildReviewControls creates the review split button, file arrows, and file
// actions; buildContentPane places them in the header rows.
func (app *giti) buildReviewControls() {
	app.reviewFilesLabel = must(gtk.LabelNew(""))
	app.reviewFilesLabel.SetNoShowAll(true)
	app.reviewFilesLabel.SetXAlign(1)
	app.reviewModeButton = must(gtk.ButtonNewWithLabel("Review"))
	app.reviewModeButton.Connect("clicked", func() {
		if app.review != nil {
			app.reviewActive = !app.reviewActive
			app.showDiffPage()
			app.updateReviewUI()
		}
	})
	menu := must(gtk.MenuNew())
	add := func(label string, activate func()) *gtk.MenuItem {
		item := must(gtk.MenuItemNewWithLabel(label))
		item.Connect("activate", activate)
		menu.Append(item)
		return item
	}
	app.reviewSummaryItem = add("", func() {})
	app.reviewSummaryItem.SetSensitive(false)
	menu.Append(must(gtk.SeparatorMenuItemNew()))
	app.reviewCommentItem = add("Add file comment", func() {
		if app.currentFile != nil {
			app.startReviewDraft(app.currentFile.path, reviewComment{})
		}
	})
	app.reviewPreviewItem = add("Preview review", app.showReviewPreview)
	app.reviewExportItem = add("Export review…", func() {
		document := app.review.markdown(app.reviewFiles)
		dialog := must(gtk.FileChooserDialogNewWith2Buttons("Export review", app.window, gtk.FILE_CHOOSER_ACTION_SAVE, "Cancel", gtk.RESPONSE_CANCEL, "Export", gtk.RESPONSE_ACCEPT))
		dialog.SetModal(true)
		dialog.SetDoOverwriteConfirmation(true)
		dialog.SetCurrentFolder(app.repository.path)
		dialog.SetCurrentName("giti-review-" + app.review.Commit[:12] + ".md")
		if dialog.Run() == gtk.RESPONSE_ACCEPT {
			if err := os.WriteFile(dialog.GetFilename(), []byte(document), 0o600); err != nil {
				app.showError(err)
			} else {
				app.showNotification("Review exported.", 3*time.Second)
			}
		}
		dialog.Destroy()
	})
	app.reviewCopyItem = add("Copy review as Markdown", func() {
		app.copyToClipboard(app.review.markdown(app.reviewFiles), "Copied review to clipboard.")
	})
	menu.ShowAll()
	more := must(gtk.MenuButtonNew())
	more.SetPopup(menu)
	more.SetImage(chevron("pan-down-symbolic", 12))
	more.SetTooltipText("Review actions")
	setAccessibility(&more.Widget, "Review actions", "Comment on the file, preview, export, or copy the review")
	app.reviewSessionActions = must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 0))
	app.reviewSessionActions.SetVAlign(gtk.ALIGN_CENTER)
	for _, widget := range []gtk.IWidget{app.reviewModeButton, more} {
		app.reviewSessionActions.PackStart(widget, false, false, 0)
	}
	// Flat like the rest of the header: bold text marks the primary action.
	for _, button := range []*gtk.Button{app.reviewModeButton, &more.ToggleButton.Button} {
		button.SetRelief(gtk.RELIEF_NONE)
		context, _ := button.GetStyleContext()
		context.AddClass("giti-review-primary")
	}
	app.reviewSessionActions.ShowAll()
	app.reviewSessionActions.SetNoShowAll(true)
	// The next-file chevron matches the go-to-parent button above it in size.
	app.reviewNextButton = must(gtk.ButtonNew())
	app.reviewNextButton.SetImage(chevron("pan-end-symbolic", 14))
	app.reviewNextButton.SetVAlign(gtk.ALIGN_CENTER)
	setAccessibility(&app.reviewNextButton.Widget, "Next file", "Go to the next file, wrapping to the first; while reviewing, skip reviewed files")
	app.reviewNextButton.Connect("clicked", func() {
		if path := app.reviewFileTarget(); path != nil {
			app.fileView.ExpandToPath(path)
			app.fileView.SetCursor(path, app.fileColumn, false)
			app.fileView.ScrollToCell(path, nil, false, 0, 0)
		}
	})
	app.reviewLinesLabel = must(gtk.LabelNew(""))
	app.reviewLinesLabel.SetNoShowAll(true)
	app.reviewLinesLabel.SetXAlign(1)
	app.reviewLinesLabel.SetTooltipText("Changed lines reviewed in this file, including whitespace changes")
	app.reviewFileButton = must(gtk.ButtonNewWithMnemonic("Mark _Reviewed"))
	app.reviewFileButton.SetNoShowAll(true)
	app.reviewFileButton.SetRelief(gtk.RELIEF_NONE)
	app.reviewFileButton.SetVAlign(gtk.ALIGN_CENTER)
	app.reviewFileButton.Connect("clicked", func() {
		if app.reviewActive && app.currentFile != nil {
			app.toggleFileReviewed(app.currentFile.path)
		}
	})
	fileContext, _ := app.reviewFileButton.GetStyleContext()
	fileContext.AddClass("giti-review-primary")
	squareButton(app.reviewNextButton)
	app.reviewDocument = must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 0))
	app.reviewDocument.SetMarginStart(20)
	app.reviewDocument.SetMarginEnd(20)
	app.reviewDocument.SetMarginTop(8)
	app.reviewDocument.SetMarginBottom(20)
	// The bar's height is always reserved, so starting a review never moves the diff.
	app.reviewProgressBar = must(gtk.DrawingAreaNew())
	app.reviewProgressBar.SetSizeRequest(-1, 3)
	app.reviewProgressBar.Connect("draw", func(area *gtk.DrawingArea, context *cairo.Context) bool {
		files := len(app.reviewFiles)
		if !app.reviewActive || app.review == nil || files == 0 {
			return false
		}
		width, height, x := float64(area.GetAllocatedWidth()), float64(area.GetAllocatedHeight()), 0.0
		context.SetSourceRGB(.898, .906, .922)
		context.Rectangle(0, 0, width, height)
		context.Fill()
		for _, segment := range []struct {
			state   reviewState
			r, g, b float64
		}{{reviewDone, .18, .55, .28}, {reviewCommented, .15, .39, .92}, {reviewPartial, .61, .64, .69}} {
			segmentWidth := width * float64(app.reviewStateCounts[segment.state]) / float64(files)
			context.SetSourceRGB(segment.r, segment.g, segment.b)
			context.Rectangle(x, 0, segmentWidth, height)
			context.Fill()
			x += segmentWidth
		}
		return false
	})
}

func (app *giti) populateFileReviewMenu(menu *gtk.Menu, path string) {
	if !app.reviewActive {
		return
	}
	label := "Mark Reviewed"
	if app.reviewState(path) >= reviewDone {
		label = "Unmark Reviewed"
	}
	mark := must(gtk.MenuItemNewWithLabel(label))
	mark.Connect("activate", func() { app.toggleFileReviewed(path) })
	menu.Append(mark)
	comment := must(gtk.MenuItemNewWithLabel("Add file comment…"))
	comment.Connect("activate", func() { app.startReviewDraft(path, reviewComment{}) })
	menu.Append(comment)
	menu.Append(must(gtk.SeparatorMenuItemNew()))
}

func (app *giti) populateReviewMenu(menu *gtk.Menu) {
	if !app.reviewActive {
		return
	}
	menu.Append(must(gtk.SeparatorMenuItemNew()))
	path := app.currentFile.path
	add := func(label string, lines []reviewLine, activate func([]reviewLine)) {
		item := must(gtk.MenuItemNewWithLabel(label))
		item.SetSensitive(len(lines) > 0)
		item.Connect("activate", func() { activate(lines) })
		menu.Append(item)
	}
	mark := func(reviewed bool) func([]reviewLine) {
		return func(lines []reviewLine) { app.markReviewLines(lines, reviewed) }
	}
	comment := func(lines []reviewLine) { app.startReviewDraft(path, reviewComment{Lines: lines}) }
	lines := app.selectedReviewLines()
	add("Mark selected lines reviewed", lines, mark(true))
	add("Mark selected lines unreviewed", lines, mark(false))
	add("Comment on selected lines…", lines, comment)
	// A hunk is only a selection shortcut. Persisted review anchors remain lines.
	first := app.diffContextLine
	for first >= 0 && first < len(app.reviewLines) && app.reviewLines[first].tag != "hunk" {
		first--
	}
	if first < 0 || first >= len(app.reviewLines) {
		return
	}
	last := first + 1
	for last < len(app.reviewLines) && app.reviewLines[last].tag != "hunk" {
		last++
	}
	hunk := reviewAnchors(app.reviewLines, first+1, last)
	menu.Append(must(gtk.SeparatorMenuItemNew()))
	add("Mark hunk reviewed", hunk, mark(true))
	add("Comment on hunk…", hunk, comment)
	add("Select hunk", hunk, func([]reviewLine) {
		app.diffBuffer.SelectRange(app.diffBuffer.GetIterAtLine(first+1), app.diffBuffer.GetIterAtLine(last))
	})
}
