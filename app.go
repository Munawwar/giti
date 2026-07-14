package main

import (
	"context"
	_ "embed"
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

const (
	idleDuration        = 12 * time.Hour
	initialHistoryLimit = 50
)

//go:embed logo/giti-logo.png
var appIconPNG []byte

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
	repository                 *repository
	resident, busy, diffLoaded bool
	idleDeadline               time.Time
	stateMu                    sync.Mutex
	server                     *residentServer
	historyLimit               int
	graphWidth                 int
	selectionGeneration        uint64
	diffGeneration             uint64
	notificationGeneration     uint64
	selectionCancel            context.CancelFunc
	diffCancel                 context.CancelFunc
	statePath                  string
	panesReady                 bool
	stateSavePending           bool
	styleProvider              *gtk.CssProvider
	diffScroll                 map[string]scrollPosition
	historyRows                []historyRow
	searchMatches              []historyRow
	files                      []changedFile
	currentRow                 *historyRow
	currentFile                *changedFile
	window                     *gtk.Window
	historyStore, fileStore    *gtk.ListStore
	historyView, fileView      *gtk.TreeView
	historyScroller            *gtk.ScrolledWindow
	graphColumn                *gtk.TreeViewColumn
	mainPane, repositoryPane   *gtk.Paned
	historySearch              *gtk.SearchEntry
	searchSettings             *gtk.MenuButton
	searchMessages             *gtk.CheckButton
	searchReferences           *gtk.CheckButton
	historyStack               *gtk.Stack
	searchResults              *gtk.ListBox
	commitHeader               *gtk.Box
	diffBuffer                 *gtk.TextBuffer
	diffView                   *gtk.TextView
	diffScroller               *gtk.ScrolledWindow
	diffStack                  *gtk.Stack
	referencesPage             *gtk.Box
	whitespaceToggle           *gtk.CheckButton
	fullFileToggle             *gtk.CheckButton
	loadButton                 *gtk.Button
	notification               *gtk.InfoBar
	notificationLabel          *gtk.Label
	fullFileHandler            glib.SignalHandle
	application                *gtk.Application
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

func newGiti(repo *repository, resident bool, applications ...*gtk.Application) (*giti, error) {
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
	var application *gtk.Application
	if len(applications) > 0 {
		application = applications[0]
	}
	app := &giti{repository: repo, resident: resident, application: application, busy: true, historyLimit: initialHistoryLimit, diffScroll: make(map[string]scrollPosition), statePath: uiStatePath()}
	if resident {
		app.server = newResidentServer(app)
		if err := app.server.start(); err != nil {
			return nil, err
		}
	}
	app.historyStore = must(gtk.ListStoreNew(gdk.PixbufGetType(), glib.TYPE_STRING, glib.TYPE_STRING))
	app.fileStore = must(gtk.ListStoreNew(glib.TYPE_STRING, glib.TYPE_STRING))
	app.buildWindow(application)
	if resident {
		addMainSource(60, app.expireIfIdle)
	}
	return app, nil
}

func (app *giti) buildWindow(application *gtk.Application) {
	state := loadUIState(app.statePath)
	if application == nil {
		app.window = must(gtk.WindowNew(gtk.WINDOW_TOPLEVEL))
	} else {
		window := must(gtk.ApplicationWindowNew(application))
		window.SetShowMenubar(true)
		app.window = &window.Window
	}
	iconLoader := must(gdk.PixbufLoaderNewWithType("png"))
	app.window.SetIcon(must(iconLoader.WriteAndReturnPixbuf(appIconPNG)))
	app.window.SetTitle("Giti — " + filepath.Base(app.repository.path))
	app.window.SetDefaultSize(1200, 760)
	app.window.Maximize()
	app.window.Connect("delete-event", func() bool {
		if !app.resident {
			app.quit()
			return false
		}
		app.hideResident()
		return true
	})
	app.window.Connect("destroy", func() { app.panesReady = false })

	app.historyView = must(gtk.TreeViewNewWithModel(app.historyStore))
	app.historyView.SetHeadersVisible(false)
	historyContext, _ := app.historyView.GetStyleContext()
	historyContext.AddClass("giti-list")
	graphRenderer := must(gtk.CellRendererPixbufNew())
	historyRenderer := must(gtk.CellRendererTextNew())
	historyColumn := must(gtk.TreeViewColumnNewWithAttribute("Graph", graphRenderer, "pixbuf", 0))
	historyColumn.SetMinWidth(48)
	historyColumn.SetSizing(gtk.TREE_VIEW_COLUMN_FIXED)
	app.graphColumn = historyColumn
	app.historyView.AppendColumn(historyColumn)
	historyColumn = must(gtk.TreeViewColumnNewWithAttribute("History", historyRenderer, "markup", 1))
	historyColumn.SetSizing(gtk.TREE_VIEW_COLUMN_AUTOSIZE)
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
	app.historyView.Connect("button-release-event", func(_ *gtk.TreeView, event *gdk.Event) bool {
		button := gdk.EventButtonNewFromEvent(event)
		if button.Button() != gdk.BUTTON_PRIMARY {
			return false
		}
		path, column, cellX, cellY, ok := app.historyView.GetPathAtPos(int(button.X()), int(button.Y()))
		if !ok || column == nil || column.GetTitle() != "History" || path == nil {
			return false
		}
		indices := path.GetIndices()
		if len(indices) == 1 && indices[0] < len(app.historyRows) {
			row := app.historyRows[indices[0]]
			branches, tags := referenceLists(row.refs)
			if row.kind != "commit" || len(branches) <= 2 && len(tags) <= 2 {
				return false
			}
			prefix := "<b>" + html.EscapeString(row.subject) + "</b>"
			measure := func(markup string) (int, int) {
				label := must(gtk.LabelNew(""))
				label.SetMarkup(markup)
				_, width := label.GetPreferredWidth()
				_, height := label.GetPreferredHeight()
				label.Destroy()
				return width, height
			}
			for _, part := range referenceParts(referenceLists(row.refs)) {
				prefix += "  "
				if !part.overflow {
					prefix += part.markup
					continue
				}
				start, height := measure(prefix)
				prefix += part.markup
				end, _ := measure(prefix)
				if cellY <= height && cellX >= start-3 && cellX <= end+3 {
					app.showReferences(branches, tags)
					return true
				}
			}
		}
		return false
	})
	app.historySearch = must(gtk.SearchEntryNew())
	app.historySearch.SetPlaceholderText("Search loaded commits")
	app.historySearch.SetTooltipText("Case-insensitive: exact phrases rank above separate word matches")
	app.historySearch.Connect("changed", app.updateGraphSearch)
	app.searchMessages = must(gtk.CheckButtonNewWithLabel("Also match commit description"))
	app.searchMessages.SetActive(state.SearchCommitMessages)
	app.searchReferences = must(gtk.CheckButtonNewWithLabel("Also match branches and tags"))
	app.searchReferences.SetActive(state.SearchReferences)
	searchOptions := must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 6))
	searchOptions.SetMarginStart(10)
	searchOptions.SetMarginEnd(10)
	searchOptions.SetMarginTop(10)
	searchOptions.SetMarginBottom(10)
	searchOptions.PackStart(app.searchMessages, false, false, 0)
	searchOptions.PackStart(app.searchReferences, false, false, 0)
	app.searchSettings = must(gtk.MenuButtonNew())
	app.searchSettings.SetImage(must(gtk.ImageNewFromIconName("preferences-system-symbolic", gtk.ICON_SIZE_BUTTON)))
	app.searchSettings.SetTooltipText("Search options")
	app.searchSettings.SetRelief(gtk.RELIEF_NONE)
	searchPopover := must(gtk.PopoverNew(app.searchSettings))
	searchPopover.Add(searchOptions)
	searchOptions.ShowAll()
	app.searchSettings.SetPopover(searchPopover)
	app.searchMessages.Connect("toggled", func() {
		app.persistUIState()
		if app.searchMessages.GetActive() {
			app.loadHistory()
		} else {
			app.updateGraphSearch()
		}
	})
	app.searchReferences.Connect("toggled", func() {
		app.persistUIState()
		app.updateGraphSearch()
	})
	app.searchResults = must(gtk.ListBoxNew())
	app.searchResults.SetActivateOnSingleClick(true)
	app.searchResults.SetPlaceholder(must(gtk.LabelNew("No loaded commits match this search.")))
	app.searchResults.Connect("row-activated", func(_ *gtk.ListBox, result *gtk.ListBoxRow) {
		app.openSearchResult(result.GetIndex())
	})
	app.historyStack = must(gtk.StackNew())
	app.historyScroller = scroller(app.historyView)
	app.historyStack.AddNamed(app.historyScroller, "graph")
	app.historyStack.AddNamed(scroller(app.searchResults), "search")

	app.fileView = must(gtk.TreeViewNewWithModel(app.fileStore))
	app.fileView.SetHeadersVisible(false)
	app.fileView.SetTooltipColumn(0)
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
	app.loadButton = must(gtk.ButtonNewWithLabel("Load more"))
	app.loadButton.Connect("clicked", func() {
		app.historyLimit += 100
		app.loadHistory()
	})

	graphBox := must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 4))
	searchBox := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 0))
	searchBox.PackStart(app.historySearch, true, true, 0)
	searchBox.PackStart(app.searchSettings, false, false, 0)
	graphBox.PackStart(searchBox, false, false, 0)
	graphBox.PackStart(app.historyStack, true, true, 0)
	graphBox.PackStart(app.loadButton, false, false, 0)
	app.repositoryPane = must(gtk.PanedNew(gtk.ORIENTATION_VERTICAL))
	app.repositoryPane.SetWideHandle(true)
	app.repositoryPane.Pack1(graphBox, false, true)
	app.repositoryPane.Pack2(scroller(app.fileView), true, true)

	toolbar := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 0))
	toolbar.PackEnd(app.whitespaceToggle, false, false, 8)
	toolbar.PackEnd(app.fullFileToggle, false, false, 0)
	if application != nil {
		refresh := glib.SimpleActionNew("refresh", nil)
		refresh.Connect("activate", func() { app.loadHistory() })
		application.AddAction(refresh)
		application.SetAccelsForAction("app.refresh", []string{"F5"})
		appMenu := glib.MenuNew()
		appMenu.Append("Refresh", "app.refresh")
		application.SetAppMenu(&appMenu.MenuModel)
		viewMenu := glib.MenuNew()
		viewMenu.Append("Refresh", "app.refresh")
		menubar := glib.MenuNew()
		menubar.AppendSubmenu("View", &viewMenu.MenuModel)
		application.SetMenubar(&menubar.MenuModel)
	}
	app.commitHeader = must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 2))
	app.commitHeader.SetMarginStart(12)
	app.commitHeader.SetMarginEnd(12)
	app.commitHeader.SetMarginTop(8)
	app.commitHeader.SetMarginBottom(8)
	diffBox := must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 4))
	diffBox.PackStart(toolbar, false, false, 4)
	diffBox.PackStart(app.commitHeader, false, false, 0)
	app.diffScroller = scroller(app.diffView)
	app.referencesPage = must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 8))
	app.referencesPage.SetMarginStart(16)
	app.referencesPage.SetMarginEnd(16)
	app.referencesPage.SetMarginTop(16)
	app.referencesPage.SetMarginBottom(16)
	app.diffStack = must(gtk.StackNew())
	app.diffStack.AddNamed(app.diffScroller, "diff")
	app.diffStack.AddNamed(scroller(app.referencesPage), "references")
	diffBox.PackStart(app.diffStack, true, true, 0)
	app.mainPane = must(gtk.PanedNew(gtk.ORIENTATION_HORIZONTAL))
	app.mainPane.SetWideHandle(true)
	app.mainPane.Pack1(app.repositoryPane, false, true)
	app.mainPane.Pack2(diffBox, true, true)
	initialized := 0
	initializePane := func(pane *gtk.Paned, vertical bool, position, fallback int) {
		var handler glib.SignalHandle
		handler = pane.Connect("size-allocate", func() {
			size := pane.GetAllocatedWidth()
			if vertical {
				size = pane.GetAllocatedHeight()
			}
			if size <= 1 {
				return
			}
			if position <= 0 {
				position = fallback
				if position < 0 {
					position = size / 2
				}
			}
			margin := min(80, size/4)
			pane.SetPosition(max(margin, min(position, size-margin)))
			pane.HandlerDisconnect(handler)
			initialized++
			app.panesReady = initialized == 2
		})
		pane.Connect("notify::position", func() {
			if !app.panesReady || app.stateSavePending {
				return
			}
			app.stateSavePending = true
			addMainSource(1, func() bool {
				app.stateSavePending = false
				app.persistUIState()
				return false
			})
		})
	}
	initializePane(app.mainPane, false, state.MainPanePosition, 440)
	initializePane(app.repositoryPane, true, state.RepositoryPanePosition, -1)
	app.notification = must(gtk.InfoBarNew())
	app.notification.SetMessageType(gtk.MESSAGE_INFO)
	app.notification.SetShowCloseButton(true)
	app.notificationLabel = must(gtk.LabelNew(""))
	app.notificationLabel.SetXAlign(0)
	must(app.notification.GetContentArea()).PackStart(app.notificationLabel, true, true, 0)
	app.notification.Connect("response", func() {
		app.notificationGeneration++
		app.notification.Hide()
	})
	root := must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 0))
	root.PackStart(app.notification, false, false, 0)
	root.PackStart(app.mainPane, true, true, 0)
	app.window.Add(root)
	app.window.ShowAll()
	app.notification.Hide()
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
	if app.diffLoaded && app.currentRow != nil && app.currentFile != nil {
		app.diffScroll[diffKey(*app.currentRow, *app.currentFile)] = scrollPosition{app.diffScroller.GetHAdjustment().GetValue(), app.diffScroller.GetVAdjustment().GetValue()}
	}
}

func (app *giti) loadHistory() error {
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
	rows, hasMore, err := app.repository.history(app.historyLimit, !app.whitespaceToggle.GetActive(), app.searchMessages.GetActive())
	if err != nil {
		app.showError(err)
		return err
	}
	app.loadButton.SetVisible(hasMore)
	app.historyRows = rows
	app.historyStore.Clear()
	graphWidth := 48
	for _, row := range rows {
		graphWidth = max(graphWidth, max(len(row.graph.lanes), len(row.graph.next))*graphLaneWidth)
	}
	app.graphWidth = graphWidth
	app.graphColumn.SetFixedWidth(graphWidth)
	target := -1
	for index, row := range rows {
		graph, graphErr := renderGraph(row, graphWidth, graphRowHeight)
		if graphErr != nil {
			app.showError(graphErr)
			return graphErr
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
	addMainSource(0, func() bool {
		if generation == app.selectionGeneration {
			app.fitGraphRows()
		}
		return false
	})
	if target >= 0 {
		addMainSource(0, func() bool {
			if generation == app.selectionGeneration && target < len(app.historyRows) {
				path := must(gtk.TreePathNewFromIndicesv([]int{target}))
				selection, _ := app.historyView.GetSelection()
				selection.SelectPath(path)
			}
			return false
		})
	}
	app.updateGraphSearch()
	return nil
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

func referenceLists(refs string) (branches, tags []string) {
	for _, decoration := range strings.Split(refs, ", ") {
		decoration = strings.TrimSpace(decoration)
		switch {
		case strings.HasPrefix(decoration, "tag: "):
			tags = append(tags, strings.TrimPrefix(strings.TrimPrefix(decoration, "tag: "), "refs/tags/"))
		case strings.HasPrefix(decoration, "HEAD -> "):
			branch := strings.TrimPrefix(decoration, "HEAD -> ")
			branch = strings.TrimPrefix(strings.TrimPrefix(branch, "refs/heads/"), remoteRefPrefix)
			branches = append(branches, branch+headRefSuffix)
		case strings.HasPrefix(decoration, "refs/heads/"):
			branches = append(branches, strings.TrimPrefix(decoration, "refs/heads/"))
		case strings.HasPrefix(decoration, remoteRefPrefix):
			branches = append(branches, decoration)
		case decoration != "":
			branches = append(branches, decoration)
		}
	}
	return sortedReferences(branches, tags)
}

func referenceBadge(value, kind string) string {
	background, foreground := "#d8f0dd", "#1f5131"
	if kind == "tag" {
		background, foreground = "#f8e7a3", "#594600"
	} else if strings.HasPrefix(value, remoteRefPrefix) {
		background, foreground = "#dce8f8", "#244e7a"
		value = strings.TrimPrefix(value, remoteRefPrefix)
	} else if value == "HEAD" {
		background, foreground = "#e5e7eb", "#374151"
	}
	if kind == "branch" {
		value = strings.Replace(value, headRefSuffix, " ← HEAD", 1)
	}
	return fmt.Sprintf(`<span background="%s" foreground="%s" weight="bold"> %s </span>`, background, foreground, html.EscapeString(value))
}

type referencePart struct {
	markup   string
	overflow bool
}

func referenceParts(branches, tags []string) []referencePart {
	parts := make([]referencePart, 0, 6)
	for _, branch := range branches[:min(2, len(branches))] {
		parts = append(parts, referencePart{markup: referenceBadge(branch, "branch")})
	}
	if len(branches) > 2 {
		parts = append(parts, referencePart{markup: referenceBadge("+ more branches", "branch"), overflow: true})
	}
	for _, tag := range tags[:min(2, len(tags))] {
		parts = append(parts, referencePart{markup: referenceBadge(tag, "tag")})
	}
	if len(tags) > 2 {
		parts = append(parts, referencePart{markup: referenceBadge("+ more tags", "tag"), overflow: true})
	}
	return parts
}

func historyLabel(row historyRow) string {
	if row.kind != "commit" {
		return "<b>" + html.EscapeString(row.subject) + "</b>"
	}
	var refs strings.Builder
	for _, part := range referenceParts(referenceLists(row.refs)) {
		refs.WriteString("  ")
		refs.WriteString(part.markup)
	}
	topology := ""
	if len(row.parents) > 1 {
		topology = fmt.Sprintf("  ·  merge  ·  %d parents", len(row.parents))
	}
	return fmt.Sprintf("<b>%s</b>%s\n<span foreground=\"#374151\"><tt>%s</tt>  ·  %s%s</span>", html.EscapeString(row.subject), refs.String(), html.EscapeString(row.revision[:7]), html.EscapeString(row.author), topology)
}

type searchMatch struct {
	row                historyRow
	branches, tags     []string
	score, index       int
	matchesDescription bool
}

type searchOptions struct {
	messages, references bool
}

func searchHistory(rows []historyRow, query string, options searchOptions) []searchMatch {
	phrase := strings.ToLower(strings.TrimSpace(query))
	if phrase == "" {
		return nil
	}
	words := strings.Fields(phrase)
	matches := make([]searchMatch, 0, len(rows))
	type field struct {
		text               string
		phraseScore, score int
	}
	for index, row := range rows {
		if row.kind != "commit" {
			continue
		}
		fields := []field{{row.subject, 1000, 100}}
		if options.references {
			fields = append(fields, field{row.refs, 750, 50})
		}
		if options.messages {
			fields = append(fields, field{row.body, 500, 25})
		}
		score, descriptionScore := 0, 0
		for fieldIndex, field := range fields {
			text := strings.ToLower(field.text)
			fieldScore := 0
			if strings.Contains(text, phrase) {
				fieldScore += field.phraseScore
			}
			for _, word := range words {
				fieldScore += strings.Count(text, word) * field.score
			}
			score += fieldScore
			if options.messages && fieldIndex == len(fields)-1 {
				descriptionScore = fieldScore
			}
		}
		if score > 0 {
			match := searchMatch{row: row, score: score, index: index, matchesDescription: descriptionScore > 0}
			if options.references {
				branches, tags := referenceLists(row.refs)
				matchesQuery := func(value string) bool {
					value = strings.ToLower(value)
					if strings.Contains(value, phrase) {
						return true
					}
					for _, word := range words {
						if strings.Contains(value, word) {
							return true
						}
					}
					return false
				}
				for _, branch := range branches {
					if matchesQuery(branch) {
						match.branches = append(match.branches, branch)
					}
				}
				for _, tag := range tags {
					if matchesQuery(tag) {
						match.tags = append(match.tags, tag)
					}
				}
			}
			matches = append(matches, match)
		}
	}
	sort.SliceStable(matches, func(left, right int) bool {
		if matches[left].score != matches[right].score {
			return matches[left].score > matches[right].score
		}
		if matches[left].row.timestamp != matches[right].row.timestamp {
			return matches[left].row.timestamp > matches[right].row.timestamp
		}
		return matches[left].index < matches[right].index
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
	matches := searchHistory(app.historyRows, query, searchOptions{app.searchMessages.GetActive(), app.searchReferences.GetActive()})
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
		label.SetMarkup(searchResultMarkup(match))
		result.PackStart(label, true, true, 0)
		result.PackEnd(copySHAButton(match.row.revision), false, false, 0)
		app.searchResults.Insert(result, -1)
	}
	app.historyStack.SetVisibleChildName("search")
	app.searchResults.ShowAll()
}

func searchResultMarkup(match searchMatch) string {
	var badges strings.Builder
	for _, part := range referenceParts(match.branches, match.tags) {
		badges.WriteString("  ")
		badges.WriteString(part.markup)
	}
	hint := ""
	if match.matchesDescription {
		hint = "\n<span size=\"small\" foreground=\"#4b5563\">matches commit description</span>"
	}
	return fmt.Sprintf("<b>%s</b>%s\n<span foreground=\"#374151\">%s  ·  %s  ·  <tt>%s</tt></span>%s", html.EscapeString(match.row.subject), badges.String(), html.EscapeString(match.row.date), html.EscapeString(match.row.author), html.EscapeString(match.row.revision[:7]), hint)
}

func (app *giti) selectHistoryRevision(revision string) bool {
	for index, row := range app.historyRows {
		if row.revision == revision {
			path := must(gtk.TreePathNewFromIndicesv([]int{index}))
			selection, _ := app.historyView.GetSelection()
			selection.SelectPath(path)
			app.historyView.ScrollToCell(path, nil, true, 0, .5)
			app.historyView.GrabFocus()
			return true
		}
	}
	return false
}

func (app *giti) revealHistoryRevision(revision string) (bool, error) {
	if app.selectHistoryRevision(revision) {
		return true, nil
	}
	for app.loadButton.GetVisible() {
		previousLimit := app.historyLimit
		app.historyLimit += max(100, app.historyLimit)
		if err := app.loadHistory(); err != nil {
			app.historyLimit = previousLimit
			return false, err
		}
		if app.selectHistoryRevision(revision) {
			repo := app.repository
			addMainSource(0, func() bool {
				if app.repository == repo {
					app.fitGraphRows()
					app.selectHistoryRevision(revision)
				}
				return false
			})
			return true, nil
		}
	}
	return false, nil
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
	if len(details.branches) > 0 || len(details.tags) > 0 {
		refs := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 6))
		addBadge := func(value, kind string) {
			label := must(gtk.LabelNew(""))
			label.SetXAlign(0)
			label.SetEllipsize(pango.ELLIPSIZE_END)
			label.SetMaxWidthChars(28)
			label.SetTooltipText(value)
			label.SetMarkup(referenceBadge(value, kind))
			refs.PackStart(label, false, false, 0)
		}
		for _, branch := range details.branches[:min(2, len(details.branches))] {
			addBadge(branch, "branch")
		}
		if len(details.branches) > 2 {
			more := must(gtk.ButtonNewWithLabel("+ more branches"))
			more.SetRelief(gtk.RELIEF_NONE)
			more.SetTooltipText("Show all branches for this commit")
			more.Connect("clicked", func() { app.showReferences(details.branches, details.tags) })
			refs.PackStart(more, false, false, 0)
		}
		for _, tag := range details.tags[:min(2, len(details.tags))] {
			addBadge(tag, "tag")
		}
		if len(details.tags) > 2 {
			more := must(gtk.ButtonNewWithLabel("+ more tags"))
			more.SetRelief(gtk.RELIEF_NONE)
			more.SetTooltipText("Show all tags for this commit")
			more.Connect("clicked", func() { app.showReferences(details.branches, details.tags) })
			refs.PackStart(more, false, false, 0)
		}
		app.commitHeader.PackStart(refs, false, false, 0)
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
				app.historySearch.SetText("")
				found, err := app.revealHistoryRevision(parent)
				if err != nil || found {
					return
				}
				app.notificationGeneration++
				generation := app.notificationGeneration
				app.notificationLabel.SetText("Parent " + parent[:7] + " is not present in this repository history.")
				app.notification.Show()
				addMainSource(5, func() bool {
					if generation == app.notificationGeneration {
						app.notification.Hide()
					}
					return false
				})
			})
			parents.PackStart(button, false, false, 0)
		}
		app.commitHeader.PackStart(parents, false, false, 0)
	}
	if details.body != "" {
		expander := must(gtk.ExpanderNew("Commit description"))
		body := must(gtk.LabelNew(details.body))
		body.SetXAlign(0)
		body.SetYAlign(0)
		body.SetSelectable(true)
		body.SetLineWrap(true)
		body.SetLineWrapMode(pango.WRAP_WORD_CHAR)
		body.SetMarginStart(20)
		body.SetMarginEnd(8)
		message := scroller(body)
		message.SetPolicy(gtk.POLICY_NEVER, gtk.POLICY_AUTOMATIC)
		message.SetMinContentHeight(min(180, max(48, (strings.Count(details.body, "\n")+1)*20+12)))
		message.SetMaxContentHeight(180)
		message.SetPropagateNaturalHeight(true)
		expander.Add(message)
		app.commitHeader.PackStart(expander, false, false, 4)
	}
	app.commitHeader.ShowAll()
}

func (app *giti) showDiffPage() {
	if app.diffStack != nil {
		app.diffStack.SetVisibleChildName("diff")
	}
}

func (app *giti) showReferences(branches, tags []string) {
	if app.diffStack == nil || app.referencesPage == nil {
		return
	}
	branches, tags = sortedReferences(branches, tags)
	if children := app.referencesPage.GetChildren(); children != nil {
		children.Foreach(func(child any) { app.referencesPage.Remove(child.(gtk.IWidget)) })
		children.Free()
	}
	back := must(gtk.ButtonNewWithLabel("← Back to diff"))
	back.SetRelief(gtk.RELIEF_NONE)
	back.SetHAlign(gtk.ALIGN_START)
	back.Connect("clicked", app.showDiffPage)
	app.referencesPage.PackStart(back, false, false, 0)
	title := must(gtk.LabelNew(""))
	title.SetXAlign(0)
	title.SetMarkup("<span size=\"large\" weight=\"bold\">Branches and tags</span>")
	app.referencesPage.PackStart(title, false, false, 0)
	addSection := func(name string, values []string, kind string) {
		if len(values) == 0 {
			return
		}
		section := must(gtk.LabelNew(""))
		section.SetXAlign(0)
		section.SetMarkup("<b>" + name + "</b>")
		app.referencesPage.PackStart(section, false, false, 8)
		for _, value := range values {
			label := must(gtk.LabelNew(""))
			label.SetXAlign(0)
			label.SetLineWrap(true)
			label.SetMarkup(referenceBadge(value, kind))
			app.referencesPage.PackStart(label, false, false, 0)
		}
	}
	addSection("Branches", branches, "branch")
	addSection("Tags", tags, "tag")
	if len(branches) == 0 && len(tags) == 0 {
		app.referencesPage.PackStart(must(gtk.LabelNew("No branches or tags.")), false, false, 8)
	}
	app.referencesPage.ShowAll()
	app.diffStack.SetVisibleChildName("references")
}

func (app *giti) onHistorySelected() {
	app.showDiffPage()
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
	app.currentRow, app.currentFile, app.files, app.diffLoaded = &app.historyRows[index], nil, nil, false
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
		addMainSource(0, func() bool {
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
	app.showDiffPage()
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
	app.diffLoaded = false
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
		addMainSource(0, func() bool {
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
			app.diffLoaded = true
			addMainSource(0, func() bool {
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

func (app *giti) persistUIState() {
	state := loadUIState(app.statePath)
	if app.panesReady && app.mainPane != nil && app.repositoryPane != nil && app.mainPane.GetAllocatedWidth() > 1 && app.repositoryPane.GetAllocatedHeight() > 1 {
		state.MainPanePosition, state.RepositoryPanePosition = app.mainPane.GetPosition(), app.repositoryPane.GetPosition()
	}
	if app.searchMessages != nil {
		state.SearchCommitMessages, state.SearchReferences = app.searchMessages.GetActive(), app.searchReferences.GetActive()
	}
	_ = saveUIState(app.statePath, state)
}

func (app *giti) clearRepositoryView() {
	app.showDiffPage()
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
	app.currentRow, app.currentFile, app.diffLoaded = nil, nil, false
	app.historySearch.SetText("")
	app.historyStore.Clear()
	app.fileStore.Clear()
	app.setCommitHeader(commitDetails{})
	app.diffBuffer.SetText("")
	app.resetFullFile()
}

func (app *giti) hideResident() {
	app.persistUIState()
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
	app.repository, app.historyLimit = repo, initialHistoryLimit
	app.clearRepositoryView()
	app.whitespaceToggle.SetActive(false)
	app.window.SetTitle("Giti — " + filepath.Base(repo.path))
	app.window.ShowAll()
	app.notificationGeneration++
	app.notification.Hide()
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
		app.quit()
		return false
	}
	return true
}

func (app *giti) quit() {
	app.persistUIState()
	if app.application != nil {
		app.application.Quit()
		return
	}
	gtk.MainQuit()
}

func (app *giti) showError(err error) {
	dialog := gtk.MessageDialogNew(app.window, gtk.DIALOG_MODAL, gtk.MESSAGE_ERROR, gtk.BUTTONS_CLOSE, "Giti could not load the repository\n\n%s", err)
	dialog.Run()
	dialog.Destroy()
}
