package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/gotk3/gotk3/gtk"
	"github.com/gotk3/gotk3/pango"
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
	root := repo.path
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	data := os.Getenv("XDG_DATA_HOME")
	if !filepath.IsAbs(data) {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, "", err
		}
		data = filepath.Join(home, ".local", "share")
		if runtime.GOOS == "darwin" {
			data = filepath.Join(home, "Library", "Application Support")
		} else if runtime.GOOS == "windows" {
			data, err = os.UserConfigDir()
			if err != nil {
				return nil, "", err
			}
		}
	}
	identity := sha256.Sum256([]byte(root))
	path := filepath.Join(data, "giti", "reviews", fmt.Sprintf("%x", identity[:16]), row.revision+"-"+comparison+".json")
	review := &commitReview{Repository: root, Commit: row.revision, Comparison: comparison, Parents: row.parents, Files: make(map[string]*reviewFile)}
	contents, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return review, path, nil
	}
	if err == nil {
		review = &commitReview{}
		err = json.Unmarshal(contents, review)
	}
	if err == nil && !review.matches(root, row.revision, comparison) {
		err = fmt.Errorf("invalid review metadata in %s", path)
	}
	return review, path, err
}

func saveCommitReview(path string, seed *commitReview, change func(*commitReview)) (*commitReview, error) {
	if path == "" || seed == nil {
		return nil, fmt.Errorf("no committed review is selected")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	// Reload under the lock, so independent windows never overwrite each other's notes.
	review := &commitReview{Repository: seed.Repository, Commit: seed.Commit, Comparison: seed.Comparison, Parents: seed.Parents, Files: make(map[string]*reviewFile)}
	contents, err := os.ReadFile(path)
	if err == nil {
		review = &commitReview{}
		err = json.Unmarshal(contents, review)
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if !review.matches(seed.Repository, seed.Commit, seed.Comparison) {
		return nil, fmt.Errorf("invalid review metadata in %s", path)
	}
	change(review)
	temporary, err := os.CreateTemp(filepath.Dir(path), "review-*.tmp")
	if err != nil {
		return nil, err
	}
	defer os.Remove(temporary.Name())
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	err = encoder.Encode(review)
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temporary.Name(), path)
	}
	return review, err
}

func (review *commitReview) file(path string) *reviewFile {
	file := review.Files[path]
	if file == nil {
		file = &reviewFile{Lines: make(map[string]reviewLine)}
		review.Files[path] = file
	}
	if file.Lines == nil {
		file.Lines = make(map[string]reviewLine)
	}
	return file
}

func (review *commitReview) matches(repository, commit, comparison string) bool {
	if review.Repository != repository || review.Commit != commit || review.Comparison != comparison || review.Files == nil {
		return false
	}
	for _, file := range review.Files {
		if file == nil {
			return false
		}
	}
	return true
}

func (display displayLine) reviewAnchor() reviewLine {
	kind := display.tag
	if kind == "" {
		kind = "context"
	}
	return reviewLine{Old: display.old, New: display.new, Kind: kind, Text: strings.TrimSuffix(display.text, "\n")}
}

func reviewLineKey(line reviewLine) string {
	return fmt.Sprintf("%s:%d:%d:%x", line.Kind, line.Old, line.New, sha256.Sum256([]byte(line.Text)))
}

func (file *reviewFile) status() (complete, partial bool) {
	if file == nil {
		return false, false
	}
	complete = file.Reviewed
	for _, line := range file.Lines {
		if line.Kind != "added" && line.Kind != "removed" {
			continue
		}
		partial = partial || line.Reviewed || file.Reviewed
		if file.Reviewed && !line.Reviewed {
			complete = false
		}
	}
	return complete, !complete && partial
}

func (app *giti) changeReview(change func(*commitReview)) {
	if app.review == nil || app.reviewPath == "" {
		return
	}
	review, err := saveCommitReview(app.reviewPath, app.review, change)
	if err != nil {
		app.showError(fmt.Errorf("save review: %w", err))
		return
	}
	app.review = review
	app.updateReviewUI()
	if app.diffStack.GetVisibleChildName() == "review-notes" {
		app.showReviewNotes()
	}
}

func (app *giti) toggleFileReviewed(path string) {
	if app.review == nil {
		return
	}
	app.changeReview(func(review *commitReview) {
		file := review.file(path)
		complete, _ := file.status()
		file.Reviewed, file.Lines = !complete, make(map[string]reviewLine)
	})
}

func (app *giti) selectedReviewLines() []reviewLine {
	if app.review == nil || app.currentFile == nil || !app.diffLoaded {
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
	var result []reviewLine
	for index := max(0, first); index <= last && index < len(app.reviewLines); index++ {
		line := app.reviewLines[index]
		if line.old == 0 && line.new == 0 {
			continue
		}
		result = append(result, line.reviewAnchor())
	}
	return result
}

func (app *giti) markReviewLines(lines []reviewLine, reviewed bool) {
	if app.currentFile == nil || len(lines) == 0 {
		return
	}
	path := app.currentFile.path
	app.changeReview(func(review *commitReview) {
		file := review.file(path)
		for _, line := range lines {
			line.Reviewed = reviewed
			file.Lines[reviewLineKey(line)] = line
		}
	})
}

func (app *giti) updateReviewUI() {
	if app.reviewBar == nil {
		return
	}
	available := app.review != nil
	app.reviewBar.SetVisible(available && app.diffStack.GetVisibleChildName() == "diff")
	app.reviewSelector.SetVisible(available)
	app.reviewColumn.SetVisible(available)
	app.reviewFileButton.SetSensitive(available && app.currentFile != nil && app.diffLoaded)
	app.reviewCommentButton.SetSensitive(available && app.currentFile != nil && app.diffLoaded)
	app.reviewFileButton.SetLabel("Mark reviewed")
	file := (*reviewFile)(nil)
	if available && app.currentFile != nil {
		file = app.review.Files[app.currentFile.path]
	}
	complete, partial := file.status()
	if complete {
		app.reviewFileButton.SetLabel("Reviewed ✓")
	}
	counts, changed, done := 0, 0, 0
	if available {
		for _, reviewedFile := range app.review.Files {
			counts += len(reviewedFile.Comments)
		}
	}
	noun := "comments"
	if counts == 1 {
		noun = "comment"
	}
	app.reviewSelector.SetLabel(fmt.Sprintf("Review notes · %d %s", counts, noun))
	for modelIndex, model := range []*gtk.TreeModel{&app.fileStore.TreeModel, &app.fileTreeStore.TreeModel} {
		model.ForEach(func(model *gtk.TreeModel, _ *gtk.TreePath, iter *gtk.TreeIter) bool {
			value, _ := model.GetValue(iter, fileIndexColumn)
			index, _ := value.GoValue()
			fileIndex, ok := index.(int)
			checked, mixed := false, false
			if available && ok && fileIndex >= 0 && fileIndex < len(app.files) {
				checked, mixed = app.review.Files[app.files[fileIndex].path].status()
			}
			if modelIndex == 0 {
				app.fileStore.Set(iter, []int{fileReviewedColumn, filePartialColumn}, []any{checked, mixed})
			} else {
				app.fileTreeStore.SetValue(iter, fileReviewedColumn, checked)
				app.fileTreeStore.SetValue(iter, filePartialColumn, mixed)
			}
			return false
		})
	}
	start, end := app.diffBuffer.GetBounds()
	for _, tag := range []string{"reviewed-added", "reviewed-removed", "reviewed-context", "review-comment"} {
		app.diffBuffer.RemoveTagByName(tag, start, end)
	}
	if !available {
		return
	}
	commented := make(map[string]bool)
	if file != nil {
		for _, comment := range file.Comments {
			for _, anchor := range comment.Lines {
				commented[reviewLineKey(anchor)] = true
			}
		}
	}
	for index, display := range app.reviewLines {
		line := display.reviewAnchor()
		if line.Old == 0 && line.New == 0 {
			continue
		}
		key := reviewLineKey(line)
		reviewed := file != nil && file.Reviewed && (line.Kind == "added" || line.Kind == "removed")
		if file != nil {
			if mark, ok := file.Lines[key]; ok {
				reviewed = mark.Reviewed
			}
		}
		if line.Kind == "added" || line.Kind == "removed" {
			changed++
			if reviewed {
				done++
			}
		}
		from, to := app.diffBuffer.GetIterAtLine(index), app.diffBuffer.GetIterAtLine(index+1)
		if reviewed {
			tag := "reviewed-context"
			if line.Kind == "added" || line.Kind == "removed" {
				tag = "reviewed-" + line.Kind
			}
			app.diffBuffer.ApplyTagByName(tag, from, to)
		}
		if commented[key] {
			app.diffBuffer.ApplyTagByName("review-comment", from, to)
		}
	}
	label := "Select a file to review"
	if app.currentFile != nil {
		label = app.currentFile.path
		if changed > 0 {
			label += fmt.Sprintf(" · %d/%d displayed changes reviewed", done, changed)
		} else if complete {
			label += " · Reviewed"
		} else if partial {
			label += " · Partially reviewed"
		}
	}
	app.reviewFileLabel.SetText(label)
	app.fileView.QueueDraw()
}

func (app *giti) editReviewComment(path string, comment reviewComment) {
	if app.review == nil {
		return
	}
	newComment := comment.ID == ""
	title := "File comment — " + path
	if len(comment.Lines) > 0 {
		title = fmt.Sprintf("Comment on %d selected lines — %s", len(comment.Lines), path)
	}
	dialog := must(gtk.DialogNew())
	dialog.SetTitle(title)
	dialog.SetTransientFor(app.window)
	dialog.SetModal(true)
	dialog.SetDestroyWithParent(true)
	dialog.SetDefaultSize(640, 320)
	dialog.AddButton("Cancel", gtk.RESPONSE_CANCEL)
	if !newComment {
		dialog.AddButton("Delete comment", gtk.RESPONSE_REJECT)
	}
	dialog.AddButton("Save comment", gtk.RESPONSE_OK)
	buffer := must(gtk.TextBufferNew(nil))
	buffer.SetText(comment.Text)
	buffer.Connect("changed", func() {
		start, end := buffer.GetBounds()
		text, _ := buffer.GetText(start, end, true)
		dialog.SetResponseSensitive(gtk.RESPONSE_OK, strings.TrimSpace(text) != "")
	})
	dialog.SetResponseSensitive(gtk.RESPONSE_OK, strings.TrimSpace(comment.Text) != "")
	view := must(gtk.TextViewNewWithBuffer(buffer))
	view.SetWrapMode(gtk.WRAP_WORD_CHAR)
	view.SetMarginStart(12)
	view.SetMarginEnd(12)
	area := must(dialog.GetContentArea())
	if len(comment.Lines) > 0 {
		area.PackStart(reviewExcerpt(comment.Lines), false, true, 0)
	}
	area.PackStart(scroller(view), true, true, 8)
	dialog.SetDefaultResponse(gtk.RESPONSE_OK)
	dialog.ShowAll()
	view.GrabFocus()
	response := dialog.Run()
	start, end := buffer.GetBounds()
	text, _ := buffer.GetText(start, end, true)
	dialog.Destroy()
	if response != gtk.RESPONSE_OK && response != gtk.RESPONSE_REJECT {
		return
	}
	if response == gtk.RESPONSE_OK && strings.TrimSpace(text) == "" {
		return
	}
	if newComment {
		comment.ID = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	comment.Text = text
	app.changeReview(func(review *commitReview) {
		file := review.file(path)
		for index, existing := range file.Comments {
			if existing.ID == comment.ID {
				if response == gtk.RESPONSE_REJECT {
					file.Comments = append(file.Comments[:index], file.Comments[index+1:]...)
				} else {
					file.Comments[index] = comment
				}
				return
			}
		}
		if response == gtk.RESPONSE_OK {
			file.Comments = append(file.Comments, comment)
		}
	})
}

func (review *commitReview) markdown() string {
	var output strings.Builder
	fmt.Fprintf(&output, "# Review %s\n\nRepository: %s\n\nCommit: %s\n\nComparison: %s\n\n", review.Commit, review.Repository, review.Commit, review.Comparison)
	if len(review.Parents) > 0 {
		fmt.Fprintf(&output, "Parent commits: %s\n\n", strings.Join(review.Parents, ", "))
	}
	paths := make([]string, 0, len(review.Files))
	for path := range review.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		file := review.Files[path]
		complete, partial := file.status()
		status := "Unreviewed"
		if complete {
			status = "Reviewed"
		} else if partial {
			status = "Partially reviewed"
		}
		fmt.Fprintf(&output, "## %s\n\nStatus: %s\n\n", path, status)
		marks := make([]reviewLine, 0, len(file.Lines))
		for _, line := range file.Lines {
			marks = append(marks, line)
		}
		sort.Slice(marks, func(i, j int) bool {
			if marks[i].New != marks[j].New {
				return marks[i].New < marks[j].New
			}
			return marks[i].Old < marks[j].Old
		})
		for _, line := range marks {
			fmt.Fprintf(&output, "- Old line %d, new line %d (%s): reviewed=%t\n", line.Old, line.New, line.Kind, line.Reviewed)
		}
		if len(marks) > 0 {
			writeReviewExcerpt(&output, marks)
		}

		for _, comment := range file.Comments {
			if len(comment.Lines) == 0 {
				output.WriteString("### File comment\n\n")
			} else {
				output.WriteString("### Line comment\n\n")
				for _, line := range comment.Lines {
					fmt.Fprintf(&output, "- Old line %d, new line %d (%s)\n", line.Old, line.New, line.Kind)
				}
				writeReviewExcerpt(&output, comment.Lines)
			}
			output.WriteString(comment.Text + "\n\n")
		}
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

func reviewExcerpt(lines []reviewLine) *gtk.ScrolledWindow {
	var excerpt strings.Builder
	for _, line := range lines {
		fmt.Fprintf(&excerpt, "old %d / new %d: %s\n", line.Old, line.New, line.Text)
	}
	preview := must(gtk.TextViewNew())
	preview.SetEditable(false)
	preview.SetCursorVisible(false)
	preview.SetMonospace(true)
	must(preview.GetBuffer()).SetText(excerpt.String())
	pane := scroller(preview)
	pane.SetMinContentHeight(60)
	pane.SetMaxContentHeight(180)
	pane.SetPropagateNaturalHeight(true)
	return pane
}

func (app *giti) showReviewNotes() {
	if app.review == nil {
		return
	}
	if app.diffFindBox.GetVisible() {
		app.closeDiffFind()
	}
	children := app.reviewNotes.GetChildren()
	children.Foreach(func(child any) { child.(*gtk.Widget).Destroy() })
	children.Free()
	heading := must(gtk.LabelNew("Review notes"))
	heading.SetXAlign(0)
	app.reviewNotes.PackStart(heading, false, false, 0)
	export := must(gtk.ButtonNewWithLabel("Export Markdown…"))
	export.SetHAlign(gtk.ALIGN_START)
	export.Connect("clicked", func() {
		dialog := must(gtk.FileChooserDialogNewWith2Buttons("Export review notes", app.window, gtk.FILE_CHOOSER_ACTION_SAVE, "Cancel", gtk.RESPONSE_CANCEL, "Export", gtk.RESPONSE_ACCEPT))
		dialog.SetDoOverwriteConfirmation(true)
		dialog.SetCurrentFolder(app.repository.path)
		dialog.SetCurrentName("giti-review-" + app.review.Commit[:min(12, len(app.review.Commit))] + ".md")
		if dialog.Run() == gtk.RESPONSE_ACCEPT {
			if err := os.WriteFile(dialog.GetFilename(), []byte(app.review.markdown()), 0o600); err != nil {
				app.showError(err)
			} else {
				app.showNotification("Review notes exported.", 3*time.Second)
			}
		}
		dialog.Destroy()
	})
	app.reviewNotes.PackStart(export, false, false, 0)
	paths := make([]string, 0, len(app.review.Files))
	for path := range app.review.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		file := app.review.Files[path]
		complete, partial := file.status()
		status := "Unreviewed"
		if complete {
			status = "Reviewed ✓"
		} else if partial {
			status = "Partially reviewed"
		}
		label := must(gtk.LabelNew(path + " · " + status))
		label.SetXAlign(0)
		label.SetSelectable(true)
		app.reviewNotes.PackStart(label, false, false, 8)
		for _, comment := range file.Comments {
			comment := comment
			card := must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 4))
			if len(comment.Lines) > 0 {
				card.PackStart(reviewExcerpt(comment.Lines), false, true, 0)
			}
			text := must(gtk.LabelNew(comment.Text))
			text.SetXAlign(0)
			text.SetLineWrap(true)
			text.SetLineWrapMode(pango.WRAP_WORD_CHAR)
			text.SetSelectable(true)
			card.PackStart(text, false, false, 0)
			edit := must(gtk.ButtonNewWithLabel("Edit comment…"))
			edit.SetHAlign(gtk.ALIGN_START)
			edit.Connect("clicked", func() { app.editReviewComment(path, comment) })
			card.PackStart(edit, false, false, 0)
			app.reviewNotes.PackStart(card, false, false, 4)
		}
	}
	if len(paths) == 0 {
		app.reviewNotes.PackStart(must(gtk.LabelNew("No review marks or comments yet. Select a file to begin.")), false, false, 8)
	}
	app.reviewNotes.ShowAll()
	app.diffStack.SetVisibleChildName("review-notes")
	app.reviewBar.Hide()
}

func (app *giti) buildReviewControls() *gtk.Box {
	app.reviewBar = must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 8))
	app.reviewBar.SetMarginStart(12)
	app.reviewBar.SetMarginEnd(12)
	app.reviewFileLabel = must(gtk.LabelNew(""))
	app.reviewFileLabel.SetXAlign(0)
	app.reviewFileLabel.SetEllipsize(pango.ELLIPSIZE_MIDDLE)
	app.reviewBar.PackStart(app.reviewFileLabel, true, true, 0)
	app.reviewFileButton = must(gtk.ButtonNewWithLabel("Mark reviewed"))
	app.reviewFileButton.SetTooltipText("Mark all changes in this file reviewed; click again to clear its review marks")
	app.reviewFileButton.Connect("clicked", func() {
		if app.currentFile != nil {
			app.toggleFileReviewed(app.currentFile.path)
		}
	})
	app.reviewBar.PackStart(app.reviewFileButton, false, false, 0)
	app.reviewCommentButton = must(gtk.ButtonNewWithLabel("Add comment"))
	app.reviewCommentButton.Connect("clicked", func() {
		if app.currentFile != nil {
			app.editReviewComment(app.currentFile.path, reviewComment{})
		}
	})
	app.reviewBar.PackStart(app.reviewCommentButton, false, false, 0)
	app.reviewNotes = must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 8))
	app.reviewNotes.SetMarginStart(16)
	app.reviewNotes.SetMarginEnd(16)
	app.reviewNotes.SetMarginTop(16)
	app.reviewNotes.SetMarginBottom(16)
	app.diffStack.AddNamed(scroller(app.reviewNotes), "review-notes")
	app.reviewBar.ShowAll()
	app.reviewBar.SetNoShowAll(true)
	return app.reviewBar
}

func (app *giti) populateReviewMenu(menu *gtk.Menu) {
	if app.review == nil {
		return
	}
	menu.Append(must(gtk.SeparatorMenuItemNew()))
	lines := app.selectedReviewLines()
	for _, action := range []struct {
		label string
		done  bool
	}{{"Mark selected lines reviewed", true}, {"Mark selected lines unreviewed", false}} {
		item := must(gtk.MenuItemNewWithLabel(action.label))
		item.SetSensitive(len(lines) > 0)
		item.Connect("activate", func() { app.markReviewLines(lines, action.done) })
		menu.Append(item)
	}
	comment := must(gtk.MenuItemNewWithLabel("Add line comment…"))
	comment.SetSensitive(len(lines) > 0)
	comment.Connect("activate", func() { app.editReviewComment(app.currentFile.path, reviewComment{Lines: lines}) })
	menu.Append(comment)
	// A hunk is only a selection shortcut. Persisted review anchors remain lines.
	first := app.diffContextLine
	for first >= 0 && first < len(app.reviewLines) && app.reviewLines[first].tag != "hunk" {
		first--
	}
	last := first + 1
	for last > 0 && last < len(app.reviewLines) && app.reviewLines[last].tag != "hunk" {
		last++
	}
	hunk := must(gtk.MenuItemNewWithLabel("Select hunk"))
	hunk.SetSensitive(first >= 0 && first < len(app.reviewLines))
	hunk.Connect("activate", func() {
		app.diffBuffer.SelectRange(app.diffBuffer.GetIterAtLine(first+1), app.diffBuffer.GetIterAtLine(last))
	})
	menu.Append(hunk)
}
