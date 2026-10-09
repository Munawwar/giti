package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	Started    bool                   `json:"started,omitempty"`
	Completed  bool                   `json:"completed,omitempty"`
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
	data := os.Getenv("XDG_DATA_HOME")
	if !filepath.IsAbs(data) {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, "", err
		}
		data = filepath.Join(home, ".local", "share")
	}
	identity := sha256.Sum256([]byte(root))
	path := filepath.Join(data, "giti", "reviews", fmt.Sprintf("%x", identity[:16]), row.revision+"-"+comparison+".json")
	seed := &commitReview{Repository: root, Commit: row.revision, Comparison: comparison, Parents: row.parents}
	review, err := readCommitReview(path, seed)
	return review, path, err
}

func saveCommitReview(path string, seed *commitReview, change func(*commitReview)) (*commitReview, error) {
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

func (file *reviewFile) status() (complete, partial bool) {
	if file == nil {
		return false, false
	}
	complete, partial = file.Reviewed, file.Reviewed
	for _, line := range file.Lines {
		if !line.changed() {
			continue
		}
		complete = complete && line.Reviewed
		partial = partial || line.Reviewed
	}
	return complete, !complete && partial
}

func (file *reviewFile) statusText() string {
	complete, partial := file.status()
	if complete {
		return "Reviewed"
	}
	if partial {
		return "Partially reviewed"
	}
	return "Unreviewed"
}

func (review *commitReview) paths() []string {
	paths := make([]string, 0, len(review.Files))
	for path := range review.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
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
	if app.diffStack.GetVisibleChildName() == "review-notes" {
		app.showReviewNotes()
	}
	return true
}

func (app *giti) toggleFileReviewed(path string) {
	app.changeReview(func(review *commitReview) {
		file := review.file(path)
		complete, _ := file.status()
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
	if app.reviewButton == nil {
		return
	}
	available := app.review != nil
	active := available && app.reviewActive
	app.reviewActions.SetVisible(available)
	app.reviewPauseButton.SetVisible(active)
	app.reviewCommentButton.SetVisible(active)
	app.reviewCommentButton.SetSensitive(active && app.currentFile != nil && app.diffLoaded && app.diffStack.GetVisibleChildName() == "diff")
	app.reviewColumn.SetVisible(available)
	app.headerMetaStack.SetVisibleChildName("author")
	app.reviewButton.SetLabel("Start Review")
	if available && (app.review.Started || len(app.review.Files) > 0) {
		app.reviewButton.SetLabel("Continue Review")
	}
	if active {
		app.headerMetaStack.SetVisibleChildName("progress")
		app.reviewButton.SetLabel("Complete Review")
	}
	comments, reviewedFiles, reviewedLines, totalLines := 0, 0, 0, 0
	directories := make(map[string]bool)
	if available {
		for _, file := range app.review.Files {
			comments += len(file.Comments)
		}
		for _, changed := range app.reviewFiles {
			file := app.review.Files[changed.path]
			complete, _ := file.status()
			if complete {
				reviewedFiles++
			}
			for directory := filepath.Dir(changed.path); directory != "."; directory = filepath.Dir(directory) {
				checked, exists := directories[directory]
				directories[directory] = complete && (!exists || checked)
			}
			lines := changed.additions + changed.deletions
			totalLines += lines
			if file != nil {
				if file.Reviewed {
					reviewedLines += lines
				}
				for _, mark := range file.Lines {
					if mark.changed() && mark.Reviewed != file.Reviewed {
						if mark.Reviewed {
							reviewedLines++
						} else {
							reviewedLines--
						}
					}
				}
			}
		}
	}
	app.reviewProgress.SetMarkup(fmt.Sprintf("<b>%d of %d lines reviewed</b>", reviewedLines, totalLines))
	app.reviewFileProgress.SetText(fmt.Sprintf("%d of %d files reviewed ·", reviewedFiles, len(app.reviewFiles)))
	noun := "comments"
	if comments == 1 {
		noun = "comment"
	}
	app.reviewSelector.SetLabel(fmt.Sprintf("%d %s", comments, noun))
	app.reviewButton.SetTooltipText("Start or continue reviewing this commit")
	if active {
		app.reviewButton.SetTooltipText("Save this review as complete; file and line marks remain as you set them")
	} else if available && app.review.Completed {
		app.reviewButton.SetTooltipText("This review was completed; continue to revise marks or comments")
	}
	for _, store := range []struct {
		model *gtk.TreeModel
		set   func(*gtk.TreeIter, int, any) error
	}{{&app.fileStore.TreeModel, app.fileStore.SetValue}, {&app.fileTreeStore.TreeModel, app.fileTreeStore.SetValue}} {
		store.model.ForEach(func(model *gtk.TreeModel, _ *gtk.TreePath, iter *gtk.TreeIter) bool {
			value, _ := model.GetValue(iter, fileIndexColumn)
			index, _ := value.GoValue()
			fileIndex, ok := index.(int)
			complete := false
			if available && ok {
				if fileIndex >= 0 && fileIndex < len(app.files) {
					complete, _ = app.review.Files[app.files[fileIndex].path].status()
				} else if fileIndex < -1 {
					complete = directories[app.fileDirectoryReviewPaths[fileIndex]]
				}
			}
			mark := ""
			if complete {
				mark = "✓"
			}
			store.set(iter, fileReviewMarkColumn, mark)
			return false
		})
	}
	start, end := app.diffBuffer.GetBounds()
	for _, tag := range []string{"reviewed-added", "reviewed-removed", "reviewed-context", "review-comment"} {
		app.diffBuffer.RemoveTagByName(tag, start, end)
	}
	if !active {
		return
	}
	var file *reviewFile
	if app.currentFile != nil {
		file = app.review.Files[app.currentFile.path]
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
		reviewed := file != nil && file.Reviewed && line.changed()
		if file != nil {
			if mark, ok := file.Lines[key]; ok {
				reviewed = mark.Reviewed
			}
		}
		from, to := app.diffBuffer.GetIterAtLine(index), app.diffBuffer.GetIterAtLine(index+1)
		if reviewed {
			tag := "reviewed-context"
			if line.changed() {
				tag = "reviewed-" + line.Kind
			}
			app.diffBuffer.ApplyTagByName(tag, from, to)
		}
		if commented[key] {
			app.diffBuffer.ApplyTagByName("review-comment", from, to)
		}
	}
	app.fileView.QueueDraw()
}

func (app *giti) editReviewComment(path string, comment reviewComment) {
	if !app.reviewActive {
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
	if review.Completed {
		output.WriteString("Review status: Complete\n\n")
	}
	if len(review.Parents) > 0 {
		fmt.Fprintf(&output, "Parent commits: %s\n\n", strings.Join(review.Parents, ", "))
	}
	paths := review.paths()
	for _, path := range paths {
		file := review.Files[path]
		fmt.Fprintf(&output, "## %s\n\nStatus: %s\n\n", path, file.statusText())
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
	back := must(gtk.ButtonNewWithLabel("Back to diff"))
	back.SetHAlign(gtk.ALIGN_START)
	back.Connect("clicked", app.showDiffPage)
	app.reviewNotes.PackStart(back, false, false, 0)
	heading := must(gtk.LabelNew("Review notes"))
	heading.SetXAlign(0)
	app.reviewNotes.PackStart(heading, false, false, 0)
	export := must(gtk.ButtonNewWithLabel("Export Markdown…"))
	export.SetHAlign(gtk.ALIGN_START)
	export.Connect("clicked", func() {
		review := app.review
		dialog := must(gtk.FileChooserDialogNewWith2Buttons("Export review notes", app.window, gtk.FILE_CHOOSER_ACTION_SAVE, "Cancel", gtk.RESPONSE_CANCEL, "Export", gtk.RESPONSE_ACCEPT))
		dialog.SetModal(true)
		dialog.SetDoOverwriteConfirmation(true)
		dialog.SetCurrentFolder(app.repository.path)
		dialog.SetCurrentName("giti-review-" + review.Commit[:12] + ".md")
		if dialog.Run() == gtk.RESPONSE_ACCEPT {
			if err := os.WriteFile(dialog.GetFilename(), []byte(review.markdown()), 0o600); err != nil {
				app.showError(err)
			} else {
				app.showNotification("Review notes exported.", 3*time.Second)
			}
		}
		dialog.Destroy()
	})
	app.reviewNotes.PackStart(export, false, false, 0)
	paths := app.review.paths()
	for _, path := range paths {
		file := app.review.Files[path]
		label := must(gtk.LabelNew(path + " · " + file.statusText()))
		label.SetXAlign(0)
		label.SetSelectable(true)
		app.reviewNotes.PackStart(label, false, false, 8)
		for _, comment := range file.Comments {
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
	app.reviewCommentButton.SetSensitive(false)
}

func (app *giti) buildReviewControls() *gtk.Box {
	row := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 12))
	app.headerAuthor = must(gtk.LabelNew(""))
	app.headerAuthor.SetXAlign(0)
	app.headerAuthor.SetEllipsize(pango.ELLIPSIZE_END)
	app.headerMetaStack = must(gtk.StackNew())
	app.headerMetaStack.SetHHomogeneous(false)
	app.headerMetaStack.SetVHomogeneous(true)
	app.headerMetaStack.SetTransitionDuration(0)
	app.headerMetaStack.AddNamed(app.headerAuthor, "author")
	progress := must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 0))
	app.reviewProgress = must(gtk.LabelNew(""))
	app.reviewProgress.SetXAlign(0)
	app.reviewProgress.SetEllipsize(pango.ELLIPSIZE_END)
	app.reviewProgress.SetTooltipText("Progress covers all changed lines in this comparison, including whitespace")
	progress.PackStart(app.reviewProgress, false, false, 0)
	details := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 4))
	app.reviewFileProgress = must(gtk.LabelNew(""))
	app.reviewFileProgress.SetXAlign(0)
	app.reviewFileProgress.SetEllipsize(pango.ELLIPSIZE_END)
	details.PackStart(app.reviewFileProgress, false, false, 0)
	app.reviewSelector = must(gtk.ButtonNewWithLabel("0 comments"))
	app.reviewSelector.SetHAlign(gtk.ALIGN_START)
	app.reviewSelector.SetRelief(gtk.RELIEF_NONE)
	app.reviewSelector.SetTooltipText("Read, edit, and export review comments")
	app.reviewSelector.Connect("clicked", app.showReviewNotes)
	context, _ := app.reviewSelector.GetStyleContext()
	context.AddClass("giti-parent-button")
	details.PackStart(app.reviewSelector, false, false, 0)
	progress.PackStart(details, false, false, 0)
	app.headerMetaStack.AddNamed(progress, "progress")
	row.PackStart(app.headerMetaStack, true, true, 0)
	app.reviewActions = must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 4))
	app.reviewActions.SetVAlign(gtk.ALIGN_CENTER)
	app.reviewCommentButton = must(gtk.ButtonNewWithLabel("Add Comment"))
	app.reviewCommentButton.Connect("clicked", func() {
		if app.currentFile != nil {
			app.editReviewComment(app.currentFile.path, reviewComment{})
		}
	})
	app.reviewPauseButton = must(gtk.ButtonNewWithLabel("Pause Review"))
	app.reviewPauseButton.SetTooltipText("Return to browsing; your review is saved")
	app.reviewPauseButton.Connect("clicked", func() {
		app.reviewActive = false
		app.showDiffPage()
		app.updateReviewUI()
	})
	for _, button := range []*gtk.Button{app.reviewCommentButton, app.reviewPauseButton} {
		button.SetRelief(gtk.RELIEF_NONE)
		context, _ := button.GetStyleContext()
		context.AddClass("giti-review-secondary")
		button.SetVAlign(gtk.ALIGN_CENTER)
		button.SetNoShowAll(true)
		app.reviewActions.PackStart(button, false, false, 0)
	}
	app.reviewButton = must(gtk.ButtonNewWithLabel("Start Review"))
	app.reviewButton.SetRelief(gtk.RELIEF_NONE)
	context, _ = app.reviewButton.GetStyleContext()
	context.AddClass("giti-review-primary")
	app.reviewButton.Connect("clicked", func() {
		active := app.reviewActive
		if app.changeReview(func(review *commitReview) {
			review.Started, review.Completed = true, active
		}) {
			app.reviewActive = !active
			app.showDiffPage()
			app.updateReviewUI()
		}
	})
	app.reviewActions.PackStart(app.reviewButton, false, false, 0)
	app.reviewActions.SetNoShowAll(true)
	row.PackEnd(app.reviewActions, false, false, 8)
	app.reviewNotes = must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 8))
	app.reviewNotes.SetMarginStart(16)
	app.reviewNotes.SetMarginEnd(16)
	app.reviewNotes.SetMarginTop(16)
	app.reviewNotes.SetMarginBottom(16)
	app.reviewActions.Show()
	app.reviewButton.ShowAll()
	return row
}

func (app *giti) populateFileReviewMenu(menu *gtk.Menu, path string) {
	if !app.reviewActive {
		return
	}
	label := "Mark Reviewed"
	if complete, _ := app.review.Files[path].status(); complete {
		label = "Mark Unreviewed"
	}
	mark := must(gtk.MenuItemNewWithLabel(label))
	mark.Connect("activate", func() { app.toggleFileReviewed(path) })
	menu.Append(mark)
	comment := must(gtk.MenuItemNewWithLabel("Add file comment…"))
	comment.Connect("activate", func() { app.editReviewComment(path, reviewComment{}) })
	menu.Append(comment)
	menu.Append(must(gtk.SeparatorMenuItemNew()))
}

func (app *giti) populateReviewMenu(menu *gtk.Menu) {
	if !app.reviewActive {
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
	if first < 0 || first >= len(app.reviewLines) {
		return
	}
	last := first + 1
	for last < len(app.reviewLines) && app.reviewLines[last].tag != "hunk" {
		last++
	}
	hunk := must(gtk.MenuItemNewWithLabel("Select hunk"))
	hunk.Connect("activate", func() {
		app.diffBuffer.SelectRange(app.diffBuffer.GetIterAtLine(first+1), app.diffBuffer.GetIterAtLine(last))
	})
	menu.Append(hunk)
}
