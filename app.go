package main

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotk3/gotk3/glib"
	"github.com/gotk3/gotk3/gtk"
	"github.com/gotk3/gotk3/pango"
)

const idleDuration = 12 * time.Hour

type giti struct {
	repository              *repository
	resident, busy          bool
	idleDeadline            time.Time
	stateMu                 sync.Mutex
	server                  *residentServer
	historyLimit            int
	selectionGeneration     uint64
	historyRows             []historyRow
	files                   []changedFile
	currentRow              *historyRow
	currentFile             *changedFile
	window                  *gtk.Window
	historyStore, fileStore *gtk.ListStore
	historyView, fileView   *gtk.TreeView
	diffBuffer              *gtk.TextBuffer
	diffView                *gtk.TextView
	whitespaceToggle        *gtk.CheckButton
	fullFileToggle          *gtk.CheckButton
	fullFileHandler         glib.SignalHandle
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func newGiti(repo *repository, resident bool) (*giti, error) {
	app := &giti{repository: repo, resident: resident, busy: true, historyLimit: 10}
	if resident {
		app.server = newResidentServer(app)
		if err := app.server.start(); err != nil {
			return nil, err
		}
	}
	app.historyStore = must(gtk.ListStoreNew(glib.TYPE_STRING, glib.TYPE_STRING))
	app.fileStore = must(gtk.ListStoreNew(glib.TYPE_STRING, glib.TYPE_STRING))
	app.buildWindow()
	if resident {
		glib.TimeoutSecondsAdd(60, app.expireIfIdle)
	}
	return app, nil
}

func (app *giti) buildWindow() {
	app.window = must(gtk.WindowNew(gtk.WINDOW_TOPLEVEL))
	app.window.SetTitle("Giti — " + filepath.Base(app.repository.path))
	app.window.SetDefaultSize(1200, 760)
	app.window.Maximize()
	app.window.Connect("delete-event", func() bool {
		if !app.resident {
			gtk.MainQuit()
			return false
		}
		app.hideResident()
		return true
	})

	app.historyView = must(gtk.TreeViewNewWithModel(app.historyStore))
	app.historyView.SetHeadersVisible(false)
	historyRenderer := must(gtk.CellRendererTextNew())
	historyRenderer.SetProperty("family", "monospace")
	historyRenderer.SetProperty("ellipsize", pango.ELLIPSIZE_END)
	historyColumn := must(gtk.TreeViewColumnNewWithAttribute("History", historyRenderer, "text", 0))
	historyColumn.SetExpand(true)
	app.historyView.AppendColumn(historyColumn)
	historySelection, _ := app.historyView.GetSelection()
	historySelection.SetSelectFunction(func(_ *gtk.TreeSelection, model *gtk.TreeModel, path *gtk.TreePath, _ bool) bool {
		iter, err := model.GetIter(path)
		if err != nil {
			return false
		}
		value, err := model.GetValue(iter, 1)
		if err != nil {
			return false
		}
		kind, _ := value.GetString()
		return kind != "connector"
	})
	historySelection.Connect("changed", app.onHistorySelected)

	app.fileView = must(gtk.TreeViewNewWithModel(app.fileStore))
	app.fileView.SetHeadersVisible(false)
	fileRenderer := must(gtk.CellRendererTextNew())
	fileRenderer.SetProperty("family", "monospace")
	fileRenderer.SetProperty("ellipsize", pango.ELLIPSIZE_MIDDLE)
	fileColumn := must(gtk.TreeViewColumnNewWithAttribute("Files", fileRenderer, "text", 0))
	fileColumn.SetExpand(true)
	app.fileView.AppendColumn(fileColumn)
	fileSelection, _ := app.fileView.GetSelection()
	fileSelection.Connect("changed", app.onFileSelected)

	app.diffBuffer = must(gtk.TextBufferNew(nil))
	app.diffBuffer.CreateTag("added", map[string]any{"background": "#d7f5dd", "foreground": "#174d22"})
	app.diffBuffer.CreateTag("removed", map[string]any{"background": "#f9d7d9", "foreground": "#682126"})
	app.diffView = must(gtk.TextViewNewWithBuffer(app.diffBuffer))
	app.diffView.SetEditable(false)
	app.diffView.SetCursorVisible(false)
	app.diffView.SetMonospace(true)
	app.diffView.SetWrapMode(gtk.WRAP_NONE)

	app.whitespaceToggle = must(gtk.CheckButtonNewWithLabel("Show whitespace changes"))
	app.whitespaceToggle.SetTooltipText("Off by default: diffs use git --ignore-all-space")
	app.whitespaceToggle.Connect("toggled", app.onWhitespaceToggled)
	app.fullFileToggle = must(gtk.CheckButtonNewWithLabel("Show full file"))
	app.fullFileHandler = app.fullFileToggle.Connect("toggled", app.onFullFileToggled)
	loadButton := must(gtk.ButtonNewWithLabel("Load more"))
	loadButton.Connect("clicked", func() {
		app.historyLimit += 100
		app.loadHistory()
	})

	graphBox := must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 4))
	graphBox.PackStart(scroller(app.historyView), true, true, 0)
	graphBox.PackStart(loadButton, false, false, 0)
	left := must(gtk.PanedNew(gtk.ORIENTATION_VERTICAL))
	left.Pack1(graphBox, true, false)
	left.Pack2(scroller(app.fileView), true, false)
	left.SetPosition(240)

	toolbar := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 0))
	toolbar.PackEnd(app.whitespaceToggle, false, false, 8)
	toolbar.PackEnd(app.fullFileToggle, false, false, 0)
	diffBox := must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 4))
	diffBox.PackStart(toolbar, false, false, 4)
	diffBox.PackStart(scroller(app.diffView), true, true, 0)
	main := must(gtk.PanedNew(gtk.ORIENTATION_HORIZONTAL))
	main.Pack1(left, false, false)
	main.Pack2(diffBox, true, false)
	main.SetPosition(280)
	app.window.Add(main)
	app.window.ShowAll()
	context, _ := app.window.GetStyleContext()
	for _, renderer := range []*gtk.CellRendererText{historyRenderer, fileRenderer} {
		renderer.SetProperty("foreground", context.GetColor(gtk.STATE_FLAG_NORMAL).String())
	}
	app.loadHistory()
}

func scroller(child gtk.IWidget) *gtk.ScrolledWindow {
	scroll := must(gtk.ScrolledWindowNew(nil, nil))
	scroll.SetPolicy(gtk.POLICY_AUTOMATIC, gtk.POLICY_AUTOMATIC)
	scroll.Add(child)
	return scroll
}

func (app *giti) loadHistory() bool {
	app.selectionGeneration++
	generation := app.selectionGeneration
	preferredKind, preferredRevision := "", ""
	if app.currentRow != nil {
		preferredKind, preferredRevision = app.currentRow.kind, app.currentRow.revision
	}
	rows, err := app.repository.history(app.historyLimit, !app.whitespaceToggle.GetActive())
	if err != nil {
		app.showError(err)
		return false
	}
	app.historyRows = rows
	app.historyStore.Clear()
	target := -1
	for index, row := range rows {
		refs := ""
		if row.refs != "" {
			refs = " (" + row.refs + ")"
		}
		label := strings.TrimRight(fmt.Sprintf("%-8s %s%s", row.graph, row.subject, refs), " ")
		iter := app.historyStore.Append()
		app.historyStore.Set(iter, []int{0, 1}, []any{label, row.kind})
		if target < 0 && row.kind != "connector" {
			target = index
		}
		if row.kind == preferredKind && (preferredRevision == "" || preferredRevision == row.revision) {
			target, preferredKind = index, ""
		}
	}
	if target >= 0 {
		glib.IdleAdd(func() bool {
			if generation == app.selectionGeneration && target < len(app.historyRows) {
				path := must(gtk.TreePathNewFromIndicesv([]int{target}))
				selection, _ := app.historyView.GetSelection()
				selection.SelectPath(path)
			}
			return false
		})
	}
	return false
}

func (app *giti) onHistorySelected() {
	selection, _ := app.historyView.GetSelection()
	_, iter, ok := selection.GetSelected()
	if !ok {
		return
	}
	path, err := app.historyStore.GetPath(iter)
	if err != nil || len(path.GetIndices()) == 0 {
		return
	}
	index := path.GetIndices()[0]
	if index >= len(app.historyRows) || app.historyRows[index].kind == "connector" {
		return
	}
	app.selectionGeneration++
	generation := app.selectionGeneration
	app.resetFullFile()
	app.diffBuffer.SetText("")
	previousPath := ""
	if app.currentFile != nil {
		previousPath = app.currentFile.path
	}
	app.currentRow = &app.historyRows[index]
	files, err := app.repository.changedFiles(*app.currentRow, !app.whitespaceToggle.GetActive())
	if err != nil {
		app.showError(err)
		return
	}
	app.files = files
	app.fileStore.Clear()
	target := 0
	for fileIndex, file := range files {
		iter := app.fileStore.Append()
		app.fileStore.Set(iter, []int{0, 1}, []any{file.label(), strconv.Itoa(fileIndex)})
		if file.path == previousPath {
			target = fileIndex
		}
	}
	if len(files) == 0 {
		app.currentFile = nil
		return
	}
	glib.IdleAdd(func() bool {
		if generation == app.selectionGeneration && target < len(app.files) {
			path := must(gtk.TreePathNewFromIndicesv([]int{target}))
			selection, _ := app.fileView.GetSelection()
			selection.SelectPath(path)
		}
		return false
	})
}

func (app *giti) onFileSelected() {
	selection, _ := app.fileView.GetSelection()
	_, iter, ok := selection.GetSelected()
	if !ok || app.currentRow == nil {
		return
	}
	path, err := app.fileStore.GetPath(iter)
	if err != nil || len(path.GetIndices()) == 0 {
		return
	}
	index := path.GetIndices()[0]
	if index >= len(app.files) {
		return
	}
	file := &app.files[index]
	if app.currentFile == nil || *file != *app.currentFile {
		app.resetFullFile()
	}
	app.currentFile = file
	allowed := app.repository.fileSize(*app.currentRow, *file) <= fullFileLimit
	app.fullFileToggle.HandlerBlock(app.fullFileHandler)
	app.fullFileToggle.SetSensitive(allowed)
	if !allowed {
		app.fullFileToggle.SetActive(false)
		app.fullFileToggle.SetTooltipText("Disabled for files larger than 2 MiB")
	} else {
		app.fullFileToggle.SetTooltipText("")
	}
	app.fullFileToggle.HandlerUnblock(app.fullFileHandler)
	app.diffBuffer.SetText("")
	patch, err := app.repository.diff(*app.currentRow, *file, !app.whitespaceToggle.GetActive(), app.fullFileToggle.GetActive())
	if err != nil {
		app.showError(err)
		return
	}
	app.setDiff(patch)
}

func (app *giti) setDiff(patch string) {
	app.diffBuffer.SetText("")
	for _, line := range displayLines(patch) {
		iter := app.diffBuffer.GetEndIter()
		if line.tag == "" {
			app.diffBuffer.Insert(iter, line.text)
		} else {
			app.diffBuffer.InsertWithTagByName(iter, line.text, line.tag)
		}
	}
}

type displayLine struct {
	text, tag string
}

func displayLines(patch string) []displayLine {
	lines := make([]displayLine, 0)
	inHeader := true
	for _, line := range splitAfterLines(patch) {
		if strings.HasPrefix(line, "@@") {
			inHeader = false
		} else if inHeader && (strings.HasPrefix(line, "diff --git ") || strings.HasPrefix(line, "index ") ||
			strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ")) {
			continue
		}
		tag := ""
		switch {
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			tag = "added"
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			tag = "removed"
		}
		lines = append(lines, displayLine{text: line, tag: tag})
	}
	return lines
}

func splitAfterLines(text string) []string {
	lines := strings.SplitAfter(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		return lines[:len(lines)-1]
	}
	return lines
}

func (app *giti) onWhitespaceToggled() {
	if app.currentRow != nil {
		app.loadHistory()
	}
}

func (app *giti) onFullFileToggled() {
	if app.currentFile != nil {
		app.onFileSelected()
	}
}

func (app *giti) resetFullFile() {
	app.fullFileToggle.HandlerBlock(app.fullFileHandler)
	app.fullFileToggle.SetActive(false)
	app.fullFileToggle.HandlerUnblock(app.fullFileHandler)
}

func (app *giti) clearRepositoryView() {
	app.selectionGeneration++
	app.historyRows, app.files = nil, nil
	app.currentRow, app.currentFile = nil, nil
	app.historyStore.Clear()
	app.fileStore.Clear()
	app.diffBuffer.SetText("")
	app.resetFullFile()
}

func (app *giti) hideResident() {
	app.clearRepositoryView()
	app.window.Hide()
	app.stateMu.Lock()
	app.busy, app.idleDeadline = false, time.Now().Add(idleDuration)
	app.stateMu.Unlock()
}

func (app *giti) openRepository(path, revision string) bool {
	repo, err := newRepository(path, revision)
	if err != nil {
		app.showError(err)
		app.stateMu.Lock()
		app.busy, app.idleDeadline = false, time.Now().Add(idleDuration)
		app.stateMu.Unlock()
		return false
	}
	app.repository, app.historyLimit = repo, 10
	app.clearRepositoryView()
	app.whitespaceToggle.SetActive(false)
	app.window.SetTitle("Giti — " + filepath.Base(repo.path))
	app.window.ShowAll()
	app.window.Maximize()
	app.window.Present()
	app.loadHistory()
	return false
}

func (app *giti) expireIfIdle() bool {
	app.stateMu.Lock()
	expired := !app.busy && !app.idleDeadline.IsZero() && time.Now().After(app.idleDeadline)
	app.stateMu.Unlock()
	if expired {
		app.server.stop()
		gtk.MainQuit()
		return false
	}
	return true
}

func (app *giti) showError(err error) {
	dialog := gtk.MessageDialogNew(app.window, gtk.DIALOG_MODAL, gtk.MESSAGE_ERROR, gtk.BUTTONS_CLOSE, "Giti could not load the repository\n\n%s", err)
	dialog.Run()
	dialog.Destroy()
}
