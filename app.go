package main

import (
	"context"
	"fmt"
	"html"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotk3/gotk3/gdk"
	"github.com/gotk3/gotk3/glib"
	"github.com/gotk3/gotk3/gtk"
	"github.com/gotk3/gotk3/pango"
)

const idleDuration = 12 * time.Hour

const appCSS = `
treeview.giti-list.view {
  -GtkTreeView-vertical-separator: 0;
}
treeview.giti-list.view:selected,
treeview.giti-list.view:selected:focus {
  background-color: #ffe2d2;
  color: #2d1b12;
}
treeview.giti-list.view:selected:backdrop {
  background-color: #ffecdf;
  color: #2d1b12;
}`

type giti struct {
	repository              *repository
	resident, busy          bool
	idleDeadline            time.Time
	stateMu                 sync.Mutex
	server                  *residentServer
	historyLimit            int
	graphWidth              int
	selectionGeneration     uint64
	diffGeneration          uint64
	selectionCancel         context.CancelFunc
	diffCancel              context.CancelFunc
	styleProvider           *gtk.CssProvider
	diffScroll              map[string]scrollPosition
	historyRows             []historyRow
	searchMatches           []historyRow
	files                   []changedFile
	currentRow              *historyRow
	currentFile             *changedFile
	window                  *gtk.Window
	historyStore, fileStore *gtk.ListStore
	historyView, fileView   *gtk.TreeView
	historySearch           *gtk.SearchEntry
	historyStack            *gtk.Stack
	searchResults           *gtk.ListBox
	commitHeader            *gtk.Box
	diffBuffer              *gtk.TextBuffer
	diffView                *gtk.TextView
	diffScroller            *gtk.ScrolledWindow
	whitespaceToggle        *gtk.CheckButton
	fullFileToggle          *gtk.CheckButton
	fullFileHandler         glib.SignalHandle
}

type scrollPosition struct{ horizontal, vertical float64 }

func diffKey(row historyRow, file changedFile) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s", row.kind, row.revision, file.status, file.oldPath, file.path)
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func newGiti(repo *repository, resident bool) (*giti, error) {
	settings, err := gtk.SettingsGetDefault()
	if err != nil {
		return nil, err
	}
	if err = settings.SetProperty("gtk-application-prefer-dark-theme", false); err != nil {
		return nil, err
	}
	if value, getErr := settings.GetProperty("gtk-theme-name"); getErr == nil {
		if theme, ok := value.(string); ok && strings.HasSuffix(theme, "-dark") {
			if err = settings.SetProperty("gtk-theme-name", strings.TrimSuffix(theme, "-dark")); err != nil {
				return nil, err
			}
		}
	}
	app := &giti{repository: repo, resident: resident, busy: true, historyLimit: 10, diffScroll: make(map[string]scrollPosition)}
	if resident {
		app.server = newResidentServer(app)
		if err := app.server.start(); err != nil {
			return nil, err
		}
	}
	app.historyStore = must(gtk.ListStoreNew(gdk.PixbufGetType(), glib.TYPE_STRING, glib.TYPE_STRING))
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
	historyContext, _ := app.historyView.GetStyleContext()
	historyContext.AddClass("giti-list")
	graphRenderer := must(gtk.CellRendererPixbufNew())
	historyRenderer := must(gtk.CellRendererTextNew())
	historyRenderer.SetProperty("ellipsize", pango.ELLIPSIZE_END)
	historyColumn := must(gtk.TreeViewColumnNewWithAttribute("Graph", graphRenderer, "pixbuf", 0))
	historyColumn.SetMinWidth(48)
	app.historyView.AppendColumn(historyColumn)
	historyColumn = must(gtk.TreeViewColumnNewWithAttribute("History", historyRenderer, "markup", 1))
	historyColumn.SetExpand(true)
	app.historyView.AppendColumn(historyColumn)
	historySelection, _ := app.historyView.GetSelection()
	historySelection.SetSelectFunction(func(_ *gtk.TreeSelection, model *gtk.TreeModel, path *gtk.TreePath, _ bool) bool {
		iter, err := model.GetIter(path)
		if err != nil {
			return false
		}
		value, err := model.GetValue(iter, 2)
		if err != nil {
			return false
		}
		kind, _ := value.GetString()
		return kind != ""
	})
	historySelection.Connect("changed", app.onHistorySelected)
	app.historySearch = must(gtk.SearchEntryNew())
	app.historySearch.SetPlaceholderText("Search loaded commits")
	app.historySearch.SetTooltipText("Case-insensitive: exact phrases rank above separate word matches")
	app.historySearch.Connect("changed", app.updateGraphSearch)
	app.searchResults = must(gtk.ListBoxNew())
	app.searchResults.SetActivateOnSingleClick(true)
	app.searchResults.SetPlaceholder(must(gtk.LabelNew("No loaded commits match this search.")))
	app.searchResults.Connect("row-activated", func(_ *gtk.ListBox, result *gtk.ListBoxRow) {
		app.openSearchResult(result.GetIndex())
	})
	app.historyStack = must(gtk.StackNew())
	app.historyStack.AddNamed(scroller(app.historyView), "graph")
	app.historyStack.AddNamed(scroller(app.searchResults), "search")

	app.fileView = must(gtk.TreeViewNewWithModel(app.fileStore))
	app.fileView.SetHeadersVisible(false)
	fileContext, _ := app.fileView.GetStyleContext()
	fileContext.AddClass("giti-list")
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
	graphBox.PackStart(app.historySearch, false, false, 0)
	graphBox.PackStart(app.historyStack, true, true, 0)
	graphBox.PackStart(loadButton, false, false, 0)
	left := must(gtk.PanedNew(gtk.ORIENTATION_VERTICAL))
	left.Pack1(graphBox, true, false)
	left.Pack2(scroller(app.fileView), true, false)
	left.SetPosition(250)

	toolbar := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 0))
	toolbar.PackEnd(app.whitespaceToggle, false, false, 8)
	toolbar.PackEnd(app.fullFileToggle, false, false, 0)
	app.commitHeader = must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 2))
	app.commitHeader.SetMarginStart(12)
	app.commitHeader.SetMarginEnd(12)
	app.commitHeader.SetMarginTop(8)
	app.commitHeader.SetMarginBottom(8)
	diffBox := must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 4))
	diffBox.PackStart(toolbar, false, false, 4)
	diffBox.PackStart(app.commitHeader, false, false, 0)
	app.diffScroller = scroller(app.diffView)
	diffBox.PackStart(app.diffScroller, true, true, 0)
	main := must(gtk.PanedNew(gtk.ORIENTATION_HORIZONTAL))
	main.Pack1(left, false, false)
	main.Pack2(diffBox, true, false)
	main.SetPosition(440)
	app.window.Add(main)
	app.window.ShowAll()
	app.styleProvider = must(gtk.CssProviderNew())
	if err := app.styleProvider.LoadFromData(appCSS); err != nil {
		panic(err)
	}
	gtk.AddProviderForScreen(must(gdk.ScreenGetDefault()), app.styleProvider, uint(gtk.STYLE_PROVIDER_PRIORITY_APPLICATION))
	app.loadHistory()
}

func scroller(child gtk.IWidget) *gtk.ScrolledWindow {
	scroll := must(gtk.ScrolledWindowNew(nil, nil))
	scroll.SetPolicy(gtk.POLICY_AUTOMATIC, gtk.POLICY_AUTOMATIC)
	scroll.Add(child)
	return scroll
}

func copySHAButton(sha string) *gtk.Button {
	button := must(gtk.ButtonNewFromIconName("edit-copy-symbolic", gtk.ICON_SIZE_BUTTON))
	button.SetTooltipText("Copy SHA: " + sha)
	button.Connect("clicked", func() {
		clipboard := must(gtk.ClipboardGet(gdk.SELECTION_CLIPBOARD))
		clipboard.SetText(sha)
	})
	return button
}

func (app *giti) rememberDiffScroll() {
	if app.currentRow != nil && app.currentFile != nil {
		app.diffScroll[diffKey(*app.currentRow, *app.currentFile)] = scrollPosition{app.diffScroller.GetHAdjustment().GetValue(), app.diffScroller.GetVAdjustment().GetValue()}
	}
}

func (app *giti) loadHistory() bool {
	app.selectionGeneration++
	app.diffGeneration++
	if app.selectionCancel != nil {
		app.selectionCancel()
	}
	if app.diffCancel != nil {
		app.diffCancel()
	}
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
	graphWidth := 48
	for _, row := range rows {
		graphWidth = max(graphWidth, max(len(row.graph.lanes), len(row.graph.next))*graphLaneWidth)
	}
	app.graphWidth = graphWidth
	target := -1
	for index, row := range rows {
		graph, graphErr := renderGraph(row, graphWidth, graphRowHeight)
		if graphErr != nil {
			app.showError(graphErr)
			return false
		}
		iter := app.historyStore.Append()
		app.historyStore.Set(iter, []int{0, 1, 2}, []any{graph, historyLabel(row), row.kind})
		if target < 0 {
			target = index
		}
		if row.kind == preferredKind && (preferredRevision == "" || preferredRevision == row.revision) {
			target, preferredKind = index, ""
		}
	}
	glib.IdleAdd(func() bool {
		if generation == app.selectionGeneration {
			app.fitGraphRows()
		}
		return false
	})
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
	app.updateGraphSearch()
	return false
}

func (app *giti) fitGraphRows() {
	column := app.historyView.GetColumn(0)
	height := graphRowHeight
	for index := range app.historyRows {
		path := must(gtk.TreePathNewFromIndicesv([]int{index}))
		height = max(height, app.historyView.GetCellArea(path, column).GetHeight())
	}
	if height == graphRowHeight {
		return
	}
	for index, row := range app.historyRows {
		path := must(gtk.TreePathNewFromIndicesv([]int{index}))
		iter, err := app.historyStore.GetIter(path)
		if err != nil {
			continue
		}
		value, err := app.historyStore.GetValue(iter, 0)
		if err == nil {
			current, valueErr := value.GoValue()
			if pixbuf, ok := current.(*gdk.Pixbuf); valueErr == nil && ok && pixbuf.GetHeight() == height {
				continue
			}
		}
		graph, err := renderGraph(row, app.graphWidth, height)
		if err == nil {
			app.historyStore.SetValue(iter, 0, graph)
		}
	}
}

func historyLabel(row historyRow) string {
	if row.kind != "commit" {
		return "<b>" + html.EscapeString(row.subject) + "</b>"
	}
	refs, topology := "", ""
	if row.refs != "" {
		refs = "  <span foreground=\"#355070\">" + html.EscapeString(strings.ReplaceAll(row.refs, "tag: ", "🏷 ")) + "</span>"
	}
	if len(row.parents) > 1 {
		topology = fmt.Sprintf("  ·  merge  ·  %d parents", len(row.parents))
	}
	return fmt.Sprintf("<b>%s</b>%s\n<span foreground=\"#374151\"><tt>%s</tt>  ·  %s%s</span>", html.EscapeString(row.subject), refs, html.EscapeString(row.revision[:7]), html.EscapeString(row.author), topology)
}

type searchMatch struct {
	row          historyRow
	score, index int
}

func searchHistory(rows []historyRow, query string) []searchMatch {
	phrase := strings.ToLower(strings.TrimSpace(query))
	words := strings.Fields(phrase)
	matches := make([]searchMatch, 0, len(rows))
	for index, row := range rows {
		if row.kind != "commit" {
			continue
		}
		subject, score := strings.ToLower(row.subject), 0
		if strings.Contains(subject, phrase) {
			score = 1000
		}
		for _, word := range words {
			score += strings.Count(subject, word) * 100
		}
		if score > 0 {
			matches = append(matches, searchMatch{row: row, score: score, index: index})
		}
	}
	sort.SliceStable(matches, func(left, right int) bool {
		return matches[left].score > matches[right].score || matches[left].score == matches[right].score && matches[left].index < matches[right].index
	})
	return matches
}

func (app *giti) updateGraphSearch() {
	query, _ := app.historySearch.GetText()
	if strings.TrimSpace(query) == "" {
		app.searchMatches = nil
		app.historyStack.SetVisibleChildName("graph")
		return
	}
	if children := app.searchResults.GetChildren(); children != nil {
		children.Foreach(func(child any) { app.searchResults.Remove(child.(gtk.IWidget)) })
		children.Free()
	}
	matches := searchHistory(app.historyRows, query)
	app.searchMatches = make([]historyRow, len(matches))
	for index, match := range matches {
		app.searchMatches[index] = match.row
		result := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 8))
		result.SetMarginStart(8)
		result.SetMarginEnd(8)
		result.SetMarginTop(6)
		result.SetMarginBottom(6)
		label := must(gtk.LabelNew(""))
		label.SetXAlign(0)
		label.SetLineWrap(true)
		label.SetMarkup(fmt.Sprintf("<b>%s</b>\n<span foreground=\"#374151\">%s  ·  %s  ·  <tt>%s</tt></span>", html.EscapeString(match.row.subject), html.EscapeString(match.row.date), html.EscapeString(match.row.author), html.EscapeString(match.row.revision[:7])))
		result.PackStart(label, true, true, 0)
		result.PackEnd(copySHAButton(match.row.revision), false, false, 0)
		app.searchResults.Insert(result, -1)
	}
	app.historyStack.SetVisibleChildName("search")
	app.searchResults.ShowAll()
}

func (app *giti) selectHistoryRevision(revision string) bool {
	for index, row := range app.historyRows {
		if row.revision == revision {
			selection, _ := app.historyView.GetSelection()
			selection.SelectPath(must(gtk.TreePathNewFromIndicesv([]int{index})))
			app.historyView.GrabFocus()
			return true
		}
	}
	return false
}

func (app *giti) openSearchResult(index int) {
	if index >= 0 && index < len(app.searchMatches) {
		revision := app.searchMatches[index].revision
		app.historySearch.SetText("")
		app.selectHistoryRevision(revision)
	}
}

func (app *giti) setCommitHeader(details commitDetails) {
	if children := app.commitHeader.GetChildren(); children != nil {
		children.Foreach(func(child any) { app.commitHeader.Remove(child.(gtk.IWidget)) })
		children.Free()
	}
	title := must(gtk.LabelNew(""))
	title.SetXAlign(0)
	title.SetMarkup("<span size=\"large\" weight=\"bold\">" + html.EscapeString(details.subject) + "</span>")
	app.commitHeader.PackStart(title, false, false, 0)
	if details.sha == "" {
		app.commitHeader.ShowAll()
		return
	}
	commit := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 8))
	commitLabel := must(gtk.LabelNew(""))
	commitLabel.SetXAlign(0)
	commitLabel.SetMarkup(fmt.Sprintf("<span foreground=\"#4b5563\"><b>Commit</b> <tt>%s</tt></span>", html.EscapeString(details.sha)))
	commit.PackStart(commitLabel, false, false, 0)
	commit.PackStart(copySHAButton(details.sha), false, false, 0)
	app.commitHeader.PackStart(commit, false, false, 0)
	meta := must(gtk.LabelNew(""))
	meta.SetXAlign(0)
	meta.SetLineWrap(true)
	meta.SetMarkup(fmt.Sprintf("<span foreground=\"#4b5563\"><b>Author</b> %s &lt;%s&gt;  ·  %s\n<b>Committer</b> %s &lt;%s&gt;  ·  %s</span>", html.EscapeString(details.author), html.EscapeString(details.authorEmail), html.EscapeString(details.authored), html.EscapeString(details.committer), html.EscapeString(details.committerEmail), html.EscapeString(details.committed)))
	app.commitHeader.PackStart(meta, false, false, 0)
	refs := make([]string, 0, len(details.branches)+4)
	for _, branch := range details.branches {
		refs = append(refs, "⎇ "+html.EscapeString(branch))
	}
	for index, tag := range details.tags {
		if index == 3 {
			refs = append(refs, fmt.Sprintf("and %d more tags", len(details.tags)-index))
			break
		}
		refs = append(refs, "🏷 "+html.EscapeString(tag))
	}
	if len(refs) > 0 {
		refLabel := must(gtk.LabelNew(""))
		refLabel.SetXAlign(0)
		refLabel.SetLineWrap(true)
		refLabel.SetMarkup("<span foreground=\"#355070\">" + strings.Join(refs, "  ·  ") + "</span>")
		app.commitHeader.PackStart(refLabel, false, false, 0)
	}
	if len(details.parents) > 0 {
		parents := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 4))
		parents.PackStart(must(gtk.LabelNew("Parents:")), false, false, 0)
		for _, parent := range details.parents {
			parent := parent
			button := must(gtk.ButtonNewWithLabel("↖ " + parent[:7]))
			button.SetRelief(gtk.RELIEF_NONE)
			button.SetTooltipText("Open parent " + parent)
			button.Connect("clicked", func() {
				if app.selectHistoryRevision(parent) {
					return
				}
				repo, err := newRepository(app.repository.path, parent)
				if err != nil {
					app.showError(err)
					return
				}
				app.repository, app.historyLimit = repo, 10
				app.clearRepositoryView()
				app.loadHistory()
			})
			parents.PackStart(button, false, false, 0)
		}
		app.commitHeader.PackStart(parents, false, false, 0)
	}
	app.commitHeader.ShowAll()
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
	if index >= len(app.historyRows) {
		return
	}
	app.selectionGeneration++
	generation := app.selectionGeneration
	if app.selectionCancel != nil {
		app.selectionCancel()
	}
	if app.diffCancel != nil {
		app.diffCancel()
	}
	app.diffGeneration++
	app.resetFullFile()
	app.rememberDiffScroll()
	app.diffBuffer.SetText("")
	app.setCommitHeader(commitDetails{subject: "Loading commit details…"})
	previousPath := ""
	if app.currentFile != nil {
		previousPath = app.currentFile.path
	}
	app.currentRow = &app.historyRows[index]
	app.currentFile, app.files = nil, nil
	app.fileStore.Clear()
	ctx, cancel := context.WithCancel(context.Background())
	app.selectionCancel = cancel
	repo, row := app.repository, *app.currentRow
	ignoreWhitespace := !app.whitespaceToggle.GetActive()
	go func() {
		details, detailsErr := commitDetails{}, error(nil)
		if row.kind == "commit" {
			details, detailsErr = repo.commitDetailsContext(ctx, row.revision)
		}
		files, loadErr := repo.changedFilesContext(ctx, row, ignoreWhitespace)
		glib.IdleAdd(func() bool {
			if ctx.Err() != nil || generation != app.selectionGeneration || repo != app.repository {
				return false
			}
			app.selectionCancel = nil
			if detailsErr != nil {
				app.showError(detailsErr)
				return false
			}
			if loadErr != nil {
				app.showError(loadErr)
				return false
			}
			if row.kind == "commit" {
				app.setCommitHeader(details)
			} else {
				app.setCommitHeader(commitDetails{subject: row.subject})
			}
			app.files = files
			target := 0
			for fileIndex, file := range files {
				iter := app.fileStore.Append()
				app.fileStore.Set(iter, []int{0, 1}, []any{file.label(), strconv.Itoa(fileIndex)})
				if file.path == previousPath {
					target = fileIndex
				}
			}
			if len(files) == 0 {
				return false
			}
			path := must(gtk.TreePathNewFromIndicesv([]int{target}))
			selection, _ := app.fileView.GetSelection()
			selection.SelectPath(path)
			return false
		})
	}()
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
	app.diffGeneration++
	generation, selectionGeneration := app.diffGeneration, app.selectionGeneration
	if app.diffCancel != nil {
		app.diffCancel()
	}
	file := &app.files[index]
	app.rememberDiffScroll()
	if app.currentFile == nil || *file != *app.currentFile {
		app.resetFullFile()
	}
	app.currentFile = file
	app.fullFileToggle.SetSensitive(false)
	app.diffBuffer.SetText("")
	app.diffScroller.GetHAdjustment().SetValue(0)
	app.diffScroller.GetVAdjustment().SetValue(0)
	ctx, cancel := context.WithCancel(context.Background())
	app.diffCancel = cancel
	repo, row, selectedFile := app.repository, *app.currentRow, *file
	position := app.diffScroll[diffKey(row, selectedFile)]
	ignoreWhitespace, fullFile := !app.whitespaceToggle.GetActive(), app.fullFileToggle.GetActive()
	go func() {
		size := repo.fileSizeContext(ctx, row, selectedFile)
		patch, loadErr := repo.diffContext(ctx, row, selectedFile, ignoreWhitespace, fullFile)
		glib.IdleAdd(func() bool {
			if ctx.Err() != nil || generation != app.diffGeneration || selectionGeneration != app.selectionGeneration || repo != app.repository {
				return false
			}
			app.diffCancel = nil
			if loadErr != nil {
				app.showError(loadErr)
				return false
			}
			allowed := size <= fullFileLimit
			app.fullFileToggle.HandlerBlock(app.fullFileHandler)
			app.fullFileToggle.SetSensitive(allowed)
			if !allowed {
				app.fullFileToggle.SetActive(false)
				app.fullFileToggle.SetTooltipText("Disabled for files larger than 2 MiB")
			} else {
				app.fullFileToggle.SetTooltipText("")
			}
			app.fullFileToggle.HandlerUnblock(app.fullFileHandler)
			app.setDiff(patch)
			glib.IdleAdd(func() bool {
				if generation == app.diffGeneration && selectionGeneration == app.selectionGeneration {
					horizontal, vertical := app.diffScroller.GetHAdjustment(), app.diffScroller.GetVAdjustment()
					horizontal.SetValue(min(position.horizontal, max(0, horizontal.GetUpper()-horizontal.GetPageSize())))
					vertical.SetValue(min(position.vertical, max(0, vertical.GetUpper()-vertical.GetPageSize())))
				}
				return false
			})
			return false
		})
	}()
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
	app.diffGeneration++
	if app.selectionCancel != nil {
		app.selectionCancel()
	}
	if app.diffCancel != nil {
		app.diffCancel()
	}
	app.historyRows, app.files, app.searchMatches = nil, nil, nil
	app.diffScroll = make(map[string]scrollPosition)
	app.currentRow, app.currentFile = nil, nil
	app.historySearch.SetText("")
	app.historyStore.Clear()
	app.fileStore.Clear()
	app.setCommitHeader(commitDetails{})
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
