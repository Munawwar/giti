package main

import (
	"context"
	_ "embed"
	"fmt"
	"html"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gotk3/gotk3/cairo"
	"github.com/gotk3/gotk3/gdk"
	"github.com/gotk3/gotk3/glib"
	"github.com/gotk3/gotk3/gtk"
	"github.com/gotk3/gotk3/pango"
)

const (
	idleDuration        = 12 * time.Hour
	initialHistoryLimit = 50
	maxAutoHistory      = 5000
	maxDiffFindMatches  = 5000
	diffFindTag         = "find-match"
	diffFindCurrentTag  = "find-match-current"
)

//go:embed logo/giti-logo.png
var appIconPNG []byte

const appCSS = `
treeview.giti-list.view {
  -GtkTreeView-vertical-separator: 0;
}
treeview.giti-list.view:selected,
treeview.giti-list.view:selected:focus {
  background-color: #fff0e8;
  color: #2d1b12;
}
treeview.giti-list.view:selected:backdrop {
  background-color: #fff7f2;
  color: #2d1b12;
}
label selection {
  background-color: @theme_selected_bg_color;
  color: @theme_selected_fg_color;
}
button.giti-ref-copy {
  padding: 0;
  min-height: 0;
  min-width: 0;
}
button.giti-ref-copy.giti-ref-joined {
  border-width: 0;
  margin: 0;
}
label.giti-stat {
  border-radius: 4px;
  font-weight: bold;
  padding: 2px 6px;
}
label.giti-additions {
  background-color: #d7f5dd;
  color: #174d22;
}
label.giti-deletions {
  background-color: #f9d7d9;
  color: #682126;
}
label.giti-untracked {
  background-color: #eef0f2;
  color: #4b5563;
}
textview.giti-references text selection {
  background-color: @theme_selected_bg_color;
  color: @theme_selected_fg_color;
}
infobar.giti-toast {
  border-radius: 6px;
  box-shadow: 0 4px 12px alpha(#000000, 0.24);
}
box.giti-diff-find {
  background-color: @theme_base_color;
  border: 1px solid alpha(#000000, 0.18);
  border-radius: 6px;
  box-shadow: 0 3px 10px alpha(#000000, 0.22);
  padding: 4px;
}`

type giti struct {
	repository                 *repository
	resident, busy, diffLoaded bool
	idleDeadline               time.Time
	stateMu                    sync.Mutex
	server                     *residentServer
	historyLimit               int
	searchLimit                int
	graphWidth                 int
	selectionGeneration        uint64
	diffGeneration             uint64
	historyGeneration          uint64
	searchGeneration           uint64
	diffFindGeneration         uint64
	notificationGeneration     uint64
	selectionCancel            context.CancelFunc
	diffCancel                 context.CancelFunc
	historyCancel              context.CancelFunc
	searchCancel               context.CancelFunc
	statePath                  string
	panesReady                 bool
	stateSavePending           bool
	searchViewingResult        bool
	historyHasMore             bool
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
	fileSummary                *gtk.Label
	graphColumn                *gtk.TreeViewColumn
	mainPane, repositoryPane   *gtk.Paned
	historySearch              *gtk.SearchEntry
	searchSettings             *gtk.MenuButton
	searchTextMode             *gtk.RadioButton
	searchFileMode             *gtk.RadioButton
	searchMessages             *gtk.CheckButton
	searchReferences           *gtk.CheckButton
	searchFollow               *gtk.CheckButton
	searchTextOptions          *gtk.Box
	searchFileOptions          *gtk.Box
	searchBack                 *gtk.Button
	historyStack               *gtk.Stack
	searchResults              *gtk.ListBox
	searchPlaceholder          *gtk.Label
	searchLoadButton           *gtk.Button
	commitHeader               *gtk.Box
	diffBuffer                 *gtk.TextBuffer
	diffView                   *gtk.TextView
	diffScroller               *gtk.ScrolledWindow
	diffFind                   *gtk.SearchEntry
	diffFindBox                *gtk.Box
	diffFindCount              *gtk.Label
	diffFindPrevious           *gtk.Button
	diffFindNext               *gtk.Button
	diffOverview               *gtk.DrawingArea
	diffOverviewReveal         *gtk.Revealer
	diffStack                  *gtk.Stack
	referencesPage             *gtk.Box
	referencesView             *gtk.TextView
	headerReferenceButtons     []*gtk.Button
	whitespaceToggle           *gtk.CheckButton
	fullFileToggle             *gtk.CheckButton
	fullMergeToggle            *gtk.CheckButton
	fullFilePreferred          bool
	loadButton                 *gtk.Button
	notification               *gtk.InfoBar
	notificationLabel          *gtk.Label
	overviewMarkers            []overviewMarker
	diffFindMatches            []diffFindMatch
	overviewLines              int
	diffFindIndex              int
	diffFindLimited            bool
	fullFileHandler            glib.SignalHandle
	fullMergeHandler           glib.SignalHandle
	application                *gtk.Application
}

type scrollPosition struct{ horizontal, vertical float64 }

type diffFindMatch struct{ start, end int }

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
	app := &giti{repository: repo, resident: resident, application: application, busy: true, historyLimit: initialHistoryLimit, searchLimit: initialHistoryLimit, diffScroll: make(map[string]scrollPosition), statePath: uiStatePath()}
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
		addMainSource(time.Minute, app.expireIfIdle)
	}
	return app, nil
}

func (app *giti) buildWindow(application *gtk.Application) {
	// Create the long-lived shell first; resident mode hides and reuses this
	// widget tree rather than rebuilding it for every repository request.
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
	app.window.SetTitle(app.repository.windowTitle())
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

	// History graph and commit metadata share a row so graph geometry remains
	// aligned with text after GTK applies font and scale settings.
	app.historyView = must(gtk.TreeViewNewWithModel(app.historyStore))
	setAccessibility(&app.historyView.Widget, "Commit history", "Git commits ordered from newest to oldest; each row states its parent topology")
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
			if row.kind != "commit" {
				return false
			}
			// TreeView markup has no child widgets to receive clicks. Recreate the
			// rendered prefix measurements to make only the inline overflow badge
			// interactive without placing long references in a separate column.
			prefix := ""
			measure := func(markup string) (int, int) {
				label := must(gtk.LabelNew(""))
				label.SetMarkup(markup)
				_, width := label.GetPreferredWidth()
				_, height := label.GetPreferredHeight()
				label.Destroy()
				return width, height
			}
			for _, part := range historyReferenceParts(row) {
				if !part.overflow {
					prefix += part.markup + "  "
					continue
				}
				start, height := measure(prefix)
				prefix += part.markup
				end, _ := measure(prefix)
				if cellY <= height && cellX >= start-3 && cellX <= end+3 {
					app.showReferences(branches, tags)
					return true
				}
				prefix += "  "
			}
		}
		return false
	})
	// Text search ranks loaded rows in memory. File search runs a separate path-
	// limited history query, leaving the regular graph and its topology intact.
	app.historySearch = must(gtk.SearchEntryNew())
	app.historySearch.SetTooltipText("Case-insensitive: exact phrases rank above separate word matches")
	app.historySearch.Connect("changed", func() {
		app.searchViewingResult = false
		app.searchLimit = initialHistoryLimit
		app.updateGraphSearch()
	})
	app.searchTextMode = must(gtk.RadioButtonNewWithLabel(nil, "Search commit text"))
	app.searchFileMode = must(gtk.RadioButtonNewWithLabelFromWidget(app.searchTextMode, "Search file or directory history"))
	app.searchTextMode.SetActive(app.repository.searchPath == "")
	app.searchFileMode.SetActive(app.repository.searchPath != "")
	app.searchMessages = must(gtk.CheckButtonNewWithLabel("Also match commit description"))
	app.searchMessages.SetActive(state.SearchCommitMessages)
	app.searchReferences = must(gtk.CheckButtonNewWithLabel("Also match branches and tags"))
	app.searchReferences.SetActive(state.SearchReferences)
	app.searchFollow = must(gtk.CheckButtonNewWithLabel("Follow file across renames"))
	app.searchFollow.SetActive(app.repository.follow)
	searchOptions := must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 6))
	searchOptions.SetMarginStart(10)
	searchOptions.SetMarginEnd(10)
	searchOptions.SetMarginTop(10)
	searchOptions.SetMarginBottom(10)
	searchOptions.PackStart(app.searchTextMode, false, false, 0)
	searchOptions.PackStart(app.searchFileMode, false, false, 0)
	app.searchTextOptions = must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 6))
	app.searchTextOptions.SetMarginStart(18)
	app.searchTextOptions.PackStart(app.searchMessages, false, false, 0)
	app.searchTextOptions.PackStart(app.searchReferences, false, false, 0)
	app.searchFileOptions = must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 6))
	app.searchFileOptions.SetMarginStart(18)
	app.searchFileOptions.PackStart(app.searchFollow, false, false, 0)
	searchOptions.PackStart(app.searchTextOptions, false, false, 0)
	searchOptions.PackStart(app.searchFileOptions, false, false, 0)
	app.searchSettings = must(gtk.MenuButtonNew())
	setAccessibility(&app.searchSettings.Widget, "Search options", "Choose text or file history search and its options")
	app.searchSettings.SetImage(must(gtk.ImageNewFromIconName("preferences-system-symbolic", gtk.ICON_SIZE_BUTTON)))
	app.searchSettings.SetTooltipText("Search options")
	app.searchSettings.SetRelief(gtk.RELIEF_NONE)
	searchPopover := must(gtk.PopoverNew(app.searchSettings))
	searchPopover.Add(searchOptions)
	searchOptions.ShowAll()
	app.searchSettings.SetPopover(searchPopover)
	app.searchMessages.Connect("toggled", func() {
		app.persistUIState()
		if app.searchTextMode.GetActive() && app.searchMessages.GetActive() {
			app.loadHistory(false)
		} else {
			app.updateGraphSearch()
		}
	})
	app.searchReferences.Connect("toggled", func() {
		app.persistUIState()
		app.updateGraphSearch()
	})
	app.searchFollow.Connect("toggled", func() {
		app.searchLimit = initialHistoryLimit
		app.updateGraphSearch()
	})
	app.searchTextMode.Connect("toggled", app.updateSearchMode)
	app.searchResults = must(gtk.ListBoxNew())
	app.searchResults.SetActivateOnSingleClick(true)
	app.searchPlaceholder = must(gtk.LabelNew("No loaded commits match this search."))
	setAccessibilityRoleAlert(&app.searchPlaceholder.Widget)
	app.searchResults.SetPlaceholder(app.searchPlaceholder)
	setAccessibility(&app.searchResults.Widget, "Search results", "Commits matching the current search")
	app.searchResults.Connect("row-activated", func(_ *gtk.ListBox, result *gtk.ListBoxRow) {
		app.openSearchResult(result.GetIndex())
	})
	app.historyStack = must(gtk.StackNew())
	app.historyScroller = scroller(app.historyView)
	app.historyStack.AddNamed(app.historyScroller, "graph")
	searchPage := must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 4))
	searchPage.PackStart(scroller(app.searchResults), true, true, 0)
	app.searchLoadButton = must(gtk.ButtonNewWithLabel("Load more search results"))
	app.searchLoadButton.Connect("clicked", func() {
		app.searchLimit += 100
		app.updateGraphSearch()
	})
	searchPage.PackStart(app.searchLoadButton, false, false, 0)
	app.historyStack.AddNamed(searchPage, "search")
	app.searchBack = must(gtk.ButtonNewFromIconName("go-previous-symbolic", gtk.ICON_SIZE_BUTTON))
	setAccessibility(&app.searchBack.Widget, "Back to search results", "Return from the selected commit to the current search results")
	app.searchBack.SetTooltipText("Back to search results")
	app.searchBack.SetRelief(gtk.RELIEF_NONE)
	app.searchBack.Connect("clicked", func() {
		app.searchViewingResult = false
		app.historyStack.SetVisibleChildName("search")
		app.searchBack.Hide()
		app.loadButton.Hide()
		if result := app.searchResults.GetSelectedRow(); result != nil {
			result.GrabFocus()
		} else if result = app.searchResults.GetRowAtIndex(0); result != nil {
			result.GrabFocus()
		}
	})
	// Changed files keep paths and compact statistics in separate renderers so
	// long paths ellipsize without displacing the right-aligned counts.
	app.fileView = must(gtk.TreeViewNewWithModel(app.fileStore))
	setAccessibility(&app.fileView.Widget, "Changed files", "Files changed by the selected history entry")
	app.fileView.SetHeadersVisible(false)
	app.fileView.AddEvents(int(gdk.BUTTON_PRESS_MASK))
	app.fileView.Connect("button-press-event", func(_ *gtk.TreeView, event *gdk.Event) bool {
		button := gdk.EventButtonNewFromEvent(event)
		if button.Button() != gdk.BUTTON_SECONDARY {
			return false
		}
		path, _, _, _, ok := app.fileView.GetPathAtPos(int(button.X()), int(button.Y()))
		if !ok || app.repository == nil {
			return false
		}
		selection, _ := app.fileView.GetSelection()
		selection.SelectPath(path)
		indices := path.GetIndices()
		if len(indices) == 0 || indices[0] >= len(app.files) {
			return false
		}
		relativePath := app.files[indices[0]].path
		menu := must(gtk.MenuNew())
		for _, action := range fileCopyActions(app.repository.path, relativePath) {
			action := action
			item := must(gtk.MenuItemNewWithLabel(action.label))
			item.Connect("activate", func() { app.copyToClipboard(action.text, action.message) })
			menu.Append(item)
		}
		menu.Connect("selection-done", menu.Destroy)
		menu.ShowAll()
		menu.PopupAtPointer(event)
		return true
	})
	app.fileView.SetTooltipColumn(0)
	fileContext, _ := app.fileView.GetStyleContext()
	fileContext.AddClass("giti-list")
	fileRenderer := must(gtk.CellRendererTextNew())
	fileRenderer.SetProperty("family", "monospace")
	fileRenderer.SetProperty("ellipsize", pango.ELLIPSIZE_MIDDLE)
	fileColumn := must(gtk.TreeViewColumnNewWithAttribute("Files", fileRenderer, "text", 0))
	fileColumn.SetExpand(true)
	app.fileView.AppendColumn(fileColumn)
	statRenderer := must(gtk.CellRendererTextNew())
	statRenderer.SetProperty("xalign", 1.0)
	statColumn := must(gtk.TreeViewColumnNewWithAttribute("Changes", statRenderer, "markup", 1))
	app.fileView.AppendColumn(statColumn)
	fileSelection, _ := app.fileView.GetSelection()
	fileSelection.Connect("changed", app.onFileSelected)

	// The diff pane owns both the text rendering and the optional full-file
	// overview; selection changes update them as a single unit.
	app.diffBuffer = must(gtk.TextBufferNew(nil))
	app.diffBuffer.CreateTag("added", map[string]any{"background": "#d7f5dd", "foreground": "#174d22"})
	app.diffBuffer.CreateTag("removed", map[string]any{"background": "#f9d7d9", "foreground": "#682126"})
	app.diffBuffer.CreateTag(diffFindTag, map[string]any{"background": "#fff2a8"})
	app.diffBuffer.CreateTag(diffFindCurrentTag, map[string]any{"background": "#f6bd4f", "foreground": "#2d2100"})
	app.diffView = must(gtk.TextViewNewWithBuffer(app.diffBuffer))
	setAccessibility(&app.diffView.Widget, "Commit diff", "Patch for the selected file; additions begin with plus and removals with minus")
	app.diffView.SetEditable(false)
	app.diffView.SetCursorVisible(false)
	app.diffView.SetMonospace(true)
	app.diffView.SetWrapMode(gtk.WRAP_NONE)
	app.buildDiffOverview()

	app.whitespaceToggle = must(gtk.CheckButtonNewWithLabel("Show whitespace changes"))
	app.whitespaceToggle.SetTooltipText("Off by default: diffs use git --ignore-all-space")
	app.whitespaceToggle.Connect("toggled", app.onWhitespaceToggled)
	app.fullFileToggle = must(gtk.CheckButtonNewWithLabel("Show full file"))
	app.fullFileHandler = app.fullFileToggle.Connect("toggled", func() {
		app.fullFilePreferred = app.fullFileToggle.GetActive()
		if app.currentFile != nil {
			app.onFileSelected()
		}
	})
	app.fullMergeToggle = must(gtk.CheckButtonNewWithLabel("Show full merge"))
	app.fullMergeToggle.SetTooltipText("Off: show the compact combined merge-resolution diff; on: compare the merge with its first parent")
	app.fullMergeToggle.SetVisible(false)
	app.fullMergeHandler = app.fullMergeToggle.Connect("toggled", func() {
		if app.currentRow != nil && app.currentRow.kind == "commit" && len(app.currentRow.parents) > 1 {
			app.onHistorySelected()
		}
	})
	app.loadButton = must(gtk.ButtonNewWithLabel("Load more"))
	app.loadButton.Connect("clicked", func() {
		app.historyLimit += 100
		app.loadHistory(false)
	})

	// Compose the three principal regions only after their controls and signal
	// handlers are ready, avoiding visible partially initialized state.
	graphBox := must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 4))
	searchBox := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 0))
	searchBox.PackStart(app.searchBack, false, false, 0)
	searchBox.PackStart(app.historySearch, true, true, 0)
	searchBox.PackStart(app.searchSettings, false, false, 0)
	graphBox.PackStart(searchBox, false, false, 0)
	graphBox.PackStart(app.historyStack, true, true, 0)
	graphBox.PackStart(app.loadButton, false, false, 0)
	app.repositoryPane = must(gtk.PanedNew(gtk.ORIENTATION_VERTICAL))
	app.repositoryPane.SetWideHandle(true)
	app.repositoryPane.Pack1(graphBox, false, true)
	app.fileSummary = must(gtk.LabelNew("Select a history entry to see changed files"))
	app.fileSummary.SetXAlign(0)
	app.fileSummary.SetEllipsize(pango.ELLIPSIZE_END)
	app.fileSummary.SetMarginStart(8)
	app.fileSummary.SetMarginEnd(8)
	app.fileSummary.SetMarginTop(6)
	app.fileSummary.SetMarginBottom(4)
	setAccessibility(&app.fileSummary.Widget, "Changed file summary", "Numbers of added, deleted, updated, and untracked files")
	fileBox := must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 0))
	fileBox.PackStart(app.fileSummary, false, false, 0)
	fileBox.PackStart(scroller(app.fileView), true, true, 0)
	app.repositoryPane.Pack2(fileBox, true, true)

	toolbar := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 0))
	toolbar.PackEnd(app.whitespaceToggle, false, false, 8)
	toolbar.PackEnd(app.fullFileToggle, false, false, 0)
	toolbar.PackEnd(app.fullMergeToggle, false, false, 8)
	if application != nil {
		refresh := glib.SimpleActionNew("refresh", nil)
		refresh.Connect("activate", func() { app.loadHistory(true) })
		application.AddAction(refresh)
		application.SetAccelsForAction("app.refresh", []string{"F5"})
		findDiff := glib.SimpleActionNew("find-diff", nil)
		findDiff.Connect("activate", func() { app.handleDiffFindKey(gdk.KEY_f, gdk.CONTROL_MASK) })
		application.AddAction(findDiff)
		application.SetAccelsForAction("app.find-diff", []string{"<Primary>f"})
		appMenu := glib.MenuNew()
		appMenu.Append("Find in Diff", "app.find-diff")
		appMenu.Append("Refresh", "app.refresh")
		application.SetAppMenu(&appMenu.MenuModel)
		viewMenu := glib.MenuNew()
		viewMenu.Append("Find in Diff", "app.find-diff")
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
	app.diffOverviewReveal = must(gtk.RevealerNew())
	app.diffOverviewReveal.SetTransitionDuration(0)
	app.diffOverviewReveal.Add(app.diffOverview)
	diffPage := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 0))
	diffPage.PackStart(app.diffScroller, true, true, 0)
	diffPage.PackStart(app.diffOverviewReveal, false, true, 0)
	app.referencesPage = must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 8))
	app.referencesPage.SetMarginStart(16)
	app.referencesPage.SetMarginEnd(16)
	app.referencesPage.SetMarginTop(16)
	app.referencesPage.SetMarginBottom(16)
	app.diffStack = must(gtk.StackNew())
	app.diffStack.AddNamed(app.buildDiffFind(diffPage), "diff")
	app.diffStack.AddNamed(scroller(app.referencesPage), "references")
	diffBox.PackStart(app.diffStack, true, true, 0)
	app.mainPane = must(gtk.PanedNew(gtk.ORIENTATION_HORIZONTAL))
	app.mainPane.SetWideHandle(true)
	app.mainPane.Pack1(app.repositoryPane, false, true)
	app.mainPane.Pack2(diffBox, true, true)
	// Pane sizes are only meaningful after the first allocation. Apply saved
	// positions once, then debounce subsequent user-driven persistence.
	initialized := 0
	initializePane := func(pane *gtk.Paned, vertical bool, position, fallback int) {
		var handler glib.SignalHandle
		handler = pane.Connect("size-allocate", func() {
			pane.HandlerDisconnect(handler)
			addMainSource(50*time.Millisecond, func() bool {
				size := pane.GetAllocatedWidth()
				if vertical {
					size = pane.GetAllocatedHeight()
				}
				if position <= 0 {
					position = fallback
					if position < 0 {
						position = size / 2
					}
				}
				margin := min(80, size/4)
				pane.SetPosition(max(margin, min(position, size-margin)))
				initialized++
				app.panesReady = initialized == 2
				return false
			})
		})
		pane.Connect("notify::position", func() {
			if !app.panesReady || app.stateSavePending {
				return
			}
			app.stateSavePending = true
			addMainSource(time.Second, func() bool {
				app.stateSavePending = false
				app.persistUIState()
				return false
			})
		})
	}
	initializePane(app.mainPane, false, state.MainPanePosition, 440)
	initializePane(app.repositoryPane, true, state.RepositoryPanePosition, -1)
	// Notifications live in an overlay so transient feedback never changes the
	// pane allocation or diff scroll position.
	app.notification = must(gtk.InfoBarNew())
	app.notification.SetMessageType(gtk.MESSAGE_INFO)
	app.notification.SetShowCloseButton(true)
	app.notification.SetHAlign(gtk.ALIGN_CENTER)
	app.notification.SetVAlign(gtk.ALIGN_START)
	app.notification.SetMarginTop(12)
	notificationContext, _ := app.notification.GetStyleContext()
	notificationContext.AddClass("giti-toast")
	app.notificationLabel = must(gtk.LabelNew(""))
	app.notificationLabel.SetXAlign(0)
	must(app.notification.GetContentArea()).PackStart(app.notificationLabel, true, true, 0)
	app.notification.Connect("response", func() {
		app.notificationGeneration++
		app.notification.Hide()
	})
	overlay := must(gtk.OverlayNew())
	overlay.Add(app.mainPane)
	overlay.AddOverlay(app.notification)
	app.window.Add(overlay)
	app.window.ShowAll()
	app.notification.Hide()
	app.searchBack.Hide()
	app.historySearch.SetText(app.repository.searchPath)
	app.updateSearchMode()
	app.styleProvider = must(gtk.CssProviderNew())
	if err := app.styleProvider.LoadFromData(appCSS); err != nil {
		panic(err)
	}
	gtk.AddProviderForScreen(must(gdk.ScreenGetDefault()), app.styleProvider, uint(gtk.STYLE_PROVIDER_PRIORITY_APPLICATION))
	app.loadHistory(false)
}

func (app *giti) buildDiffFind(diffPage gtk.IWidget) *gtk.Overlay {
	// Keep find controls inside the diff page: the stack naturally hides them
	// on the references page, while the overlay preserves diff viewport space.
	app.diffFind = must(gtk.SearchEntryNew())
	app.diffFind.SetPlaceholderText("Find in diff")
	app.diffFind.SetWidthChars(24)
	setAccessibility(&app.diffFind.Widget, "Find in diff", "Case-insensitive search within the displayed diff")
	app.diffFindCount = must(gtk.LabelNew(""))
	app.diffFindCount.SetWidthChars(12)
	setAccessibility(&app.diffFindCount.Widget, "Diff search match count", "Current match and total matches")
	iconButton := func(icon, name, tooltip string) *gtk.Button {
		button := must(gtk.ButtonNewFromIconName(icon, gtk.ICON_SIZE_BUTTON))
		button.SetRelief(gtk.RELIEF_NONE)
		button.SetTooltipText(tooltip)
		setAccessibility(&button.Widget, name, tooltip)
		return button
	}
	app.diffFindPrevious = iconButton("go-up-symbolic", "Previous diff match", "Previous match (Shift+Enter)")
	app.diffFindNext = iconButton("go-down-symbolic", "Next diff match", "Next match (Enter)")
	closeButton := iconButton("window-close-symbolic", "Close diff search", "Close diff search (Escape)")
	app.diffFindPrevious.SetSensitive(false)
	app.diffFindNext.SetSensitive(false)

	app.diffFindBox = must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 2))
	app.diffFindBox.SetHAlign(gtk.ALIGN_END)
	app.diffFindBox.SetVAlign(gtk.ALIGN_START)
	app.diffFindBox.SetMarginTop(8)
	app.diffFindBox.SetMarginEnd(8)
	findContext, _ := app.diffFindBox.GetStyleContext()
	findContext.AddClass("giti-diff-find")
	app.diffFindBox.PackStart(app.diffFind, true, true, 0)
	app.diffFindBox.PackStart(app.diffFindCount, false, false, 2)
	app.diffFindBox.PackStart(app.diffFindPrevious, false, false, 0)
	app.diffFindBox.PackStart(app.diffFindNext, false, false, 0)
	app.diffFindBox.PackStart(closeButton, false, false, 0)
	// Keep the children ready to display, but exclude the floating bar itself
	// from repository-opening window.ShowAll calls until Ctrl+F explicitly opens it.
	app.diffFindBox.ShowAll()
	app.diffFindBox.Hide()
	app.diffFindBox.SetNoShowAll(true)

	// Clear stale highlights immediately, then debounce the potentially large
	// buffer scan. The generation makes superseded timers harmless.
	app.diffFind.Connect("changed", func() {
		app.diffFindGeneration++
		generation := app.diffFindGeneration
		app.clearDiffFindMatches()
		query, _ := app.diffFind.GetText()
		if query != "" {
			addMainSource(120*time.Millisecond, func() bool {
				if generation == app.diffFindGeneration && app.diffFindBox.GetVisible() {
					app.updateDiffFind()
				}
				return false
			})
		}
	})
	// Route entry and toplevel events through one handler so Ctrl+F works from
	// any pane while Enter, Shift+Enter, and Escape remain browser-like.
	app.diffFind.Connect("key-press-event", func(_ *gtk.SearchEntry, event *gdk.Event) bool {
		key := gdk.EventKeyNewFromEvent(event)
		return app.handleDiffFindKey(key.KeyVal(), gdk.ModifierType(key.State()))
	})
	app.diffFindPrevious.Connect("clicked", func() { app.moveDiffFind(-1) })
	app.diffFindNext.Connect("clicked", func() { app.moveDiffFind(1) })
	closeButton.Connect("clicked", app.closeDiffFind)
	app.diffFind.Connect("stop-search", app.closeDiffFind)
	app.window.Connect("key-press-event", func(_ any, event *gdk.Event) bool {
		key := gdk.EventKeyNewFromEvent(event)
		return app.handleDiffFindKey(key.KeyVal(), gdk.ModifierType(key.State()))
	})
	overlay := must(gtk.OverlayNew())
	overlay.Add(diffPage)
	overlay.AddOverlay(app.diffFindBox)
	return overlay
}

func (app *giti) handleDiffFindKey(key uint, modifiers gdk.ModifierType) bool {
	switch {
	case modifiers&gdk.CONTROL_MASK != 0 && (key == gdk.KEY_f || key == gdk.KEY_F):
		if app.diffStack == nil || app.diffStack.GetVisibleChildName() != "diff" {
			return false
		}
		app.diffFindBox.Show()
		app.updateDiffFind()
		app.diffFind.GrabFocus()
		app.diffFind.SelectRegion(0, -1)
	case (key == gdk.KEY_Return || key == gdk.KEY_KP_Enter) && app.diffFind.IsFocus():
		direction := 1
		if modifiers&gdk.SHIFT_MASK != 0 {
			direction = -1
		}
		app.moveDiffFind(direction)
	case key == gdk.KEY_Escape && app.diffFindBox.GetVisible():
		app.closeDiffFind()
	default:
		return false
	}
	return true
}

func (app *giti) clearDiffFindMatches() {
	start, end := app.diffBuffer.GetBounds()
	app.diffBuffer.RemoveTagByName(diffFindTag, start, end)
	app.diffBuffer.RemoveTagByName(diffFindCurrentTag, start, end)
	app.diffFindMatches, app.diffFindIndex, app.diffFindLimited = app.diffFindMatches[:0], -1, false
	app.diffFindCount.SetText("")
	app.diffFindPrevious.SetSensitive(false)
	app.diffFindNext.SetSensitive(false)
}

func (app *giti) updateDiffFind() {
	app.diffFindGeneration++
	app.clearDiffFindMatches()
	query, _ := app.diffFind.GetText()
	if query == "" || !app.diffFindBox.GetVisible() {
		return
	}
	flags := gtk.TEXT_SEARCH_CASE_INSENSITIVE | gtk.TEXT_SEARCH_VISIBLE_ONLY | gtk.TEXT_SEARCH_TEXT_ONLY
	for cursor := app.diffBuffer.GetStartIter(); ; {
		matchStart, matchEnd, found := cursor.ForwardSearch(query, flags, nil)
		if !found {
			break
		}
		if len(app.diffFindMatches) == maxDiffFindMatches {
			app.diffFindLimited = true
			break
		}
		// Applying millions of tags can freeze GTK for common one-character
		// queries; report the bounded result as 5000+ and keep navigation usable.
		app.diffFindMatches = append(app.diffFindMatches, diffFindMatch{matchStart.GetOffset(), matchEnd.GetOffset()})
		app.diffBuffer.ApplyTagByName(diffFindTag, matchStart, matchEnd)
		cursor = matchEnd
	}
	if len(app.diffFindMatches) == 0 {
		app.diffFindCount.SetText("0 / 0")
		return
	}
	app.diffFindPrevious.SetSensitive(true)
	app.diffFindNext.SetSensitive(true)
	app.selectDiffFindMatch(0)
}

func (app *giti) selectDiffFindMatch(index int) {
	if len(app.diffFindMatches) == 0 {
		return
	}
	start, end := app.diffBuffer.GetBounds()
	app.diffBuffer.RemoveTagByName(diffFindCurrentTag, start, end)
	app.diffFindIndex = (index%len(app.diffFindMatches) + len(app.diffFindMatches)) % len(app.diffFindMatches)
	match := app.diffFindMatches[app.diffFindIndex]
	start, end = app.diffBuffer.GetIterAtOffset(match.start), app.diffBuffer.GetIterAtOffset(match.end)
	app.diffBuffer.ApplyTagByName(diffFindCurrentTag, start, end)
	limited := ""
	if app.diffFindLimited {
		limited = "+"
	}
	app.diffFindCount.SetText(fmt.Sprintf("%d / %d%s", app.diffFindIndex+1, len(app.diffFindMatches), limited))
	app.diffView.ScrollToIter(start, .2, true, .5, .35)
}

func (app *giti) moveDiffFind(direction int) {
	if len(app.diffFindMatches) > 0 {
		app.selectDiffFindMatch(app.diffFindIndex + direction)
	}
}

func (app *giti) closeDiffFind() {
	app.diffFindGeneration++
	app.diffFindBox.Hide()
	app.clearDiffFindMatches()
	app.diffView.GrabFocus()
}

func (app *giti) buildDiffOverview() {
	const targetWidth, trackWidth = 24, 10
	app.diffOverview = must(gtk.DrawingAreaNew())
	app.diffOverview.SetSizeRequest(targetWidth, -1)
	app.diffOverview.SetVAlign(gtk.ALIGN_FILL)
	app.diffOverview.SetVExpand(true)
	app.diffOverview.SetTooltipText("Change overview — click or drag to scroll")
	app.diffOverview.AddEvents(int(gdk.BUTTON_PRESS_MASK | gdk.BUTTON1_MOTION_MASK))
	setAccessibility(&app.diffOverview.Widget, "Diff change overview", "Added and removed lines across the full file; click or drag to scroll")
	app.diffOverview.Connect("draw", func(_ *gtk.DrawingArea, context *cairo.Context) bool {
		width, height := float64(app.diffOverview.GetAllocatedWidth()), float64(app.diffOverview.GetAllocatedHeight())
		trackX := (width - trackWidth) / 2
		context.SetSourceRGB(.97, .97, .97)
		context.Paint()
		context.SetSourceRGB(.94, .95, .96)
		context.Rectangle(trackX, 0, trackWidth, height)
		context.Fill()
		if app.overviewLines < 1 || height < 1 {
			return false
		}
		markerHeight, denominator := math.Max(2, math.Min(5, height/float64(app.overviewLines))), float64(max(1, app.overviewLines-1))
		for _, marker := range app.overviewMarkers {
			x, markerWidth := trackX, trackWidth/2.0
			if marker.added {
				x = markerWidth
				context.SetSourceRGB(.18, .55, .28)
			} else {
				context.SetSourceRGB(.78, .25, .30)
			}
			context.Rectangle(x, float64(marker.line)/denominator*(height-markerHeight), markerWidth, markerHeight)
			context.Fill()
		}
		return false
	})
	app.diffOverview.Connect("button-press-event", func(_ *gtk.DrawingArea, event *gdk.Event) bool {
		button := gdk.EventButtonNewFromEvent(event)
		if button.Button() == gdk.BUTTON_PRIMARY {
			app.scrollDiffOverview(button.Y())
			return true
		}
		return false
	})
	app.diffOverview.Connect("motion-notify-event", func(_ *gtk.DrawingArea, event *gdk.Event) bool {
		motion := gdk.EventMotionNewFromEvent(event)
		if motion.State()&gdk.BUTTON1_MASK != 0 {
			_, y := motion.MotionVal()
			app.scrollDiffOverview(y)
			return true
		}
		return false
	})
}

func scroller(child gtk.IWidget) *gtk.ScrolledWindow {
	scroll := must(gtk.ScrolledWindowNew(nil, nil))
	scroll.SetPolicy(gtk.POLICY_AUTOMATIC, gtk.POLICY_AUTOMATIC)
	scroll.Add(child)
	return scroll
}

func (app *giti) copySHAButton(sha string) *gtk.Button {
	button := must(gtk.ButtonNewFromIconName("edit-copy-symbolic", gtk.ICON_SIZE_BUTTON))
	setAccessibility(&button.Widget, "Copy commit SHA", "Copy "+sha+" to the clipboard")
	button.SetTooltipText("Copy SHA: " + sha)
	button.Connect("clicked", func() {
		app.copyToClipboard(sha, "Copied commit ID to clipboard.")
	})
	return button
}

func (app *giti) rememberDiffScroll() {
	if app.diffLoaded && app.currentRow != nil && app.currentFile != nil {
		app.diffScroll[diffKey(*app.currentRow, *app.currentFile)] = scrollPosition{app.diffScroller.GetHAdjustment().GetValue(), app.diffScroller.GetVAdjustment().GetValue()}
	}
}

// File results are independent of graph pagination and selection-driven loads;
// only an explicit repository refresh needs to query them for new commits.
func (app *giti) loadHistory(refreshFileSearch bool) {
	app.loadHistoryTo("", refreshFileSearch)
}

func (app *giti) loadHistoryTo(reveal string, refreshFileSearch bool) {
	// A history refresh invalidates every downstream selection and diff load.
	// Generation checks protect GTK from callbacks already queued on the main loop.
	app.historyGeneration++
	if app.historyCancel != nil {
		app.historyCancel()
	}
	app.selectionGeneration++
	app.diffGeneration++
	if app.selectionCancel != nil {
		app.selectionCancel()
	}
	if app.diffCancel != nil {
		app.diffCancel()
	}
	historyGeneration := app.historyGeneration
	preferredKind, preferredRevision := "", ""
	if app.currentRow != nil {
		preferredKind, preferredRevision = app.currentRow.kind, app.currentRow.revision
	}
	ctx, cancel := context.WithCancel(context.Background())
	app.historyCancel = cancel
	repo, limit := app.repository, app.historyLimit
	ignoreWhitespace, includeMessages := !app.whitespaceToggle.GetActive(), app.searchMessages.GetActive()
	app.loadButton.SetSensitive(false)
	go func() {
		var rows []historyRow
		var graphs []*gdk.Pixbuf
		var hasMore, found, beyondAutoLimit bool
		var err error
		// Parent navigation first locates the requested revision without building
		// every intervening row, then expands the normal history query just enough.
		autoLimit := max(limit, maxAutoHistory)
		if reveal != "" {
			index := -1
			index, beyondAutoLimit, err = repo.historyIndexContext(ctx, reveal, autoLimit)
			found = index >= 0
			if found {
				limit = max(limit, index+1)
			}
		}
		if err == nil {
			rows, hasMore, err = repo.historyContext(ctx, limit, ignoreWhitespace, includeMessages)
			if reveal != "" && found {
				found = false
				for _, row := range rows {
					found = found || row.revision == reveal
				}
			}
		}
		// Render at the nominal row height off the GTK thread. A measured-height
		// second pass runs after the model is visible when font scaling requires it.
		graphWidth := 48
		for _, row := range rows {
			graphWidth = max(graphWidth, max(len(row.graph.lanes), len(row.graph.next))*graphLaneWidth)
		}
		if err == nil {
			graphs, err = renderGraphs(rows, graphWidth, graphRowHeight, func() bool { return ctx.Err() != nil })
		}
		addMainSource(0, func() bool {
			if ctx.Err() != nil || historyGeneration != app.historyGeneration || repo != app.repository {
				return false
			}
			app.loadButton.SetSensitive(true)
			if err != nil {
				app.historyCancel = nil
				app.showError(err)
				return false
			}
			// Commit the result to GTK only after the stale-work guard above; all
			// model selection and follow-up measurement stays on the main thread.
			app.historyLimit, app.graphWidth, app.historyRows, app.historyHasMore = limit, graphWidth, rows, hasMore
			app.loadButton.SetVisible(hasMore && (app.historyStack.GetVisibleChildName() == "graph" || app.searchViewingResult))
			app.graphColumn.SetFixedWidth(graphWidth)
			app.historyStore.Clear()
			target := -1
			for index, row := range rows {
				iter := app.historyStore.Append()
				app.historyStore.Set(iter, []int{0, 1, 2}, []any{graphs[index], historyLabel(row), row.kind})
				if target < 0 {
					target = index
				}
				if row.kind == preferredKind && (preferredRevision == "" || preferredRevision == row.revision) {
					target, preferredKind = index, ""
				}
				if reveal != "" && row.revision == reveal {
					target = index
				}
			}
			if target >= 0 {
				path := must(gtk.TreePathNewFromIndicesv([]int{target}))
				selection, _ := app.historyView.GetSelection()
				selection.SelectPath(path)
				if reveal != "" {
					app.historyView.ScrollToCell(path, nil, true, 0, .5)
					app.historyView.GrabFocus()
				}
			}
			if app.searchTextMode.GetActive() || refreshFileSearch {
				app.updateGraphSearch()
			}
			addMainSource(0, func() bool {
				if historyGeneration == app.historyGeneration && repo == app.repository {
					app.fitGraphRows(ctx, historyGeneration, repo)
					if reveal != "" {
						app.selectHistoryRevision(reveal)
					}
				}
				return false
			})
			if reveal != "" && !found {
				message := "Parent " + reveal[:7] + " is not present in this repository history."
				if beyondAutoLimit {
					message = fmt.Sprintf("Parent %s was not found in the first %d commits.", reveal[:7], autoLimit)
				}
				app.showNotification(message, 5*time.Second)
			}
			return false
		})
	}()
}

func (app *giti) fitGraphRows(ctx context.Context, historyGeneration uint64, repo *repository) {
	column := app.historyView.GetColumn(0)
	height := graphRowHeight
	for index, row := range app.historyRows {
		if row.kind == "commit" {
			path := must(gtk.TreePathNewFromIndicesv([]int{index}))
			height = max(height, app.historyView.GetCellArea(path, column).GetHeight())
			break
		}
	}
	if height == graphRowHeight {
		app.historyCancel = nil
		return
	}
	rows, width := app.historyRows, app.graphWidth
	go func() {
		graphs, err := renderGraphs(rows, width, height, func() bool { return ctx.Err() != nil })
		if ctx.Err() != nil {
			return
		}
		addMainSource(0, func() bool {
			if ctx.Err() != nil || historyGeneration != app.historyGeneration || repo != app.repository {
				return false
			}
			app.historyCancel = nil
			if err != nil {
				app.showError(err)
				return false
			}
			for index, graph := range graphs {
				path := must(gtk.TreePathNewFromIndicesv([]int{index}))
				if iter, iterErr := app.historyStore.GetIter(path); iterErr == nil {
					app.historyStore.SetValue(iter, 0, graph)
				}
			}
			return false
		})
	}()
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
	value, _, background, foreground := referenceAppearance(value, kind)
	return fmt.Sprintf(`<span background="%s" foreground="%s" weight="bold"> %s </span>`, background, foreground, html.EscapeString(value))
}

func referenceAppearance(value, kind string) (display, style, background, foreground string) {
	style, background, foreground = "giti-ref-local", "#d8f0dd", "#1f5131"
	if kind == "tag" {
		style, background, foreground = "giti-ref-tag", "#f8e7a3", "#594600"
	} else if strings.HasPrefix(value, remoteRefPrefix) {
		style, background, foreground = "giti-ref-remote", "#dce8f8", "#244e7a"
		value = strings.TrimPrefix(value, remoteRefPrefix)
	} else if value == "HEAD" {
		style, background, foreground = "giti-ref-head", "#e5e7eb", "#374151"
	}
	if kind == "branch" {
		value = strings.Replace(value, headRefSuffix, " ← HEAD", 1)
	}
	return value, style, background, foreground
}

type referencePart struct {
	markup   string
	label    string
	branches []string
	segments []string
	synced   bool
	overflow bool
}

type remoteBranch struct {
	value, display, remote, path string
}

func syncedBranchBadges(local string, remote remoteBranch) (combined, remoteBadge, localBadge string) {
	localDisplay, _, localBackground, localForeground := referenceAppearance(local, "branch")
	_, _, remoteBackground, remoteForeground := referenceAppearance(remote.value, "branch")
	prefix := strings.TrimSuffix(remote.display, remote.path)
	remoteBadge = fmt.Sprintf(`<span background="%s" foreground="%s" weight="bold"> %s</span>`, remoteBackground, remoteForeground, html.EscapeString(prefix))
	localBadge = fmt.Sprintf(`<span background="%s" foreground="%s" weight="bold">%s </span>`, localBackground, localForeground, html.EscapeString(localDisplay))
	return remoteBadge + localBadge, remoteBadge, localBadge
}

func branchReferenceParts(branches []string, upstreams map[string]string) []referencePart {
	const displayLimit = 4
	// Normalize local and remote ordering before pairing them; Git decoration
	// order is presentation-oriented and is not stable enough for this layout.
	locals, remotes := make([]string, 0, len(branches)), make([]remoteBranch, 0, len(branches))
	for _, branch := range branches {
		if !strings.HasPrefix(branch, remoteRefPrefix) {
			locals = append(locals, branch)
			continue
		}
		display := strings.TrimPrefix(branch, remoteRefPrefix)
		remote, path := display, ""
		if separator := strings.IndexByte(display, '/'); separator >= 0 {
			remote, path = display[:separator], display[separator+1:]
		}
		remotes = append(remotes, remoteBranch{branch, display, remote, path})
	}
	locals, _ = sortedReferences(locals, nil)
	sort.Slice(remotes, func(left, right int) bool {
		if remotes[left].path != remotes[right].path {
			return remotes[left].path < remotes[right].path
		}
		if remotes[left].remote != remotes[right].remote {
			return remotes[left].remote < remotes[right].remote
		}
		return remotes[left].value < remotes[right].value
	})
	// A remote is emitted once: preferably joined to its configured upstream,
	// otherwise adjacent to a same-path local branch, then as an unmatched ref.
	byValue, used := make(map[string]remoteBranch, len(remotes)), make(map[string]bool, len(remotes))
	for _, remote := range remotes {
		byValue[remote.value] = remote
	}
	parts := make([]referencePart, 0, len(branches)+1)
	for _, local := range locals {
		name := strings.TrimSuffix(local, headRefSuffix)
		upstream, tracks := byValue[upstreams[name]]
		tracks = tracks && !used[upstream.value]
		if tracks && name != "HEAD" && upstream.path == name {
			markup, remoteBadge, localBadge := syncedBranchBadges(local, upstream)
			parts = append(parts, referencePart{markup: markup, branches: []string{upstream.value, local}, segments: []string{remoteBadge, localBadge}, synced: true})
			used[upstream.value] = true
		} else {
			parts = append(parts, referencePart{markup: referenceBadge(local, "branch"), branches: []string{local}})
			if tracks {
				parts = append(parts, referencePart{markup: referenceBadge(upstream.value, "branch"), branches: []string{upstream.value}})
				used[upstream.value] = true
			}
		}
		if name == "HEAD" {
			continue
		}
		for _, remote := range remotes {
			if !used[remote.value] && remote.path == name {
				parts = append(parts, referencePart{markup: referenceBadge(remote.value, "branch"), branches: []string{remote.value}})
				used[remote.value] = true
			}
		}
	}
	for _, remote := range remotes {
		if !used[remote.value] {
			parts = append(parts, referencePart{markup: referenceBadge(remote.value, "branch"), branches: []string{remote.value}})
		}
	}
	// Collapse only after pairing so the overflow count reflects hidden branch
	// names rather than the smaller number of joined visual parts.
	if len(parts) <= displayLimit {
		return parts
	}
	hidden := 0
	for _, part := range parts[displayLimit:] {
		hidden += len(part.branches)
	}
	label := fmt.Sprintf("+%d more branches", hidden)
	return append(parts[:displayLimit], referencePart{markup: `<span foreground="#4b5563" weight="bold">` + label + `</span>`, label: label, overflow: true})
}

func referenceParts(branches, tags []string, upstreams map[string]string) []referencePart {
	parts := branchReferenceParts(branches, upstreams)
	for _, tag := range tags[:min(2, len(tags))] {
		parts = append(parts, referencePart{markup: referenceBadge(tag, "tag")})
	}
	if len(tags) > 2 {
		parts = append(parts, referencePart{markup: referenceBadge("+ more tags", "tag"), overflow: true})
	}
	return parts
}

func historyReferenceParts(row historyRow) []referencePart {
	branches, tags := referenceLists(row.refs)
	parts := make([]referencePart, 0, 6)
	if len(tags) == 1 {
		parts = append(parts, referencePart{markup: referenceBadge(tags[0], "tag")})
	} else if len(tags) > 1 {
		parts = append(parts, referencePart{markup: referenceBadge(fmt.Sprintf("%d tags", len(tags)), "tag"), overflow: true})
	}
	return append(parts, branchReferenceParts(branches, row.upstreams)...)
}

func historyLabel(row historyRow) string {
	if row.kind != "commit" {
		return "<b>" + html.EscapeString(row.subject) + "</b>"
	}
	var refs strings.Builder
	for _, part := range historyReferenceParts(row) {
		refs.WriteString(part.markup)
		refs.WriteString("  ")
	}
	topology := "root commit"
	if len(row.parents) == 1 {
		topology = "1 parent"
	} else if len(row.parents) > 1 {
		topology = fmt.Sprintf("merge · %d parents", len(row.parents))
	}
	return fmt.Sprintf("%s<b>%s</b>\n<span foreground=\"#374151\"><tt>%s</tt>  ·  %s  ·  %s</span>", refs.String(), html.EscapeString(row.subject), html.EscapeString(row.revision[:7]), html.EscapeString(row.author), topology)
}

type searchMatch struct {
	row                            historyRow
	branches, tags                 []string
	score, index                   int
	matchesDescription, matchesSHA bool
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
		subject, body, refs := row.searchSubject, row.searchBody, row.searchRefs
		if subject == "" && row.subject != "" {
			subject = strings.ToLower(row.subject)
		}
		if body == "" && row.body != "" {
			body = strings.ToLower(row.body)
		}
		if refs == "" && row.refs != "" {
			refs = strings.ToLower(row.refs)
		}
		shaMatch := isSHAQuery(phrase) && strings.HasPrefix(strings.ToLower(row.revision), phrase)
		// Exact phrases outrank repeated word hits; subjects outrank references,
		// which outrank descriptions, and an explicit SHA prefix wins overall.
		fields := []field{{subject, 1000, 100}}
		if options.references {
			fields = append(fields, field{refs, 750, 50})
		}
		if options.messages {
			fields = append(fields, field{body, 500, 25})
		}
		score, descriptionScore := 0, 0
		for fieldIndex, field := range fields {
			text := field.text
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
		if shaMatch {
			score += 2000
		}
		if score > 0 {
			match := searchMatch{row: row, score: score, index: index, matchesDescription: descriptionScore > 0, matchesSHA: shaMatch}
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

func isSHAQuery(query string) bool {
	if len(query) < 5 {
		return false
	}
	for _, char := range query {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func (app *giti) updateGraphSearch() {
	query, _ := app.historySearch.GetText()
	app.searchGeneration++
	if app.searchCancel != nil {
		app.searchCancel()
		app.searchCancel = nil
	}
	if strings.TrimSpace(query) == "" {
		app.searchMatches = nil
		app.searchViewingResult = false
		app.searchBack.Hide()
		app.searchLoadButton.Hide()
		app.historyStack.SetVisibleChildName("graph")
		app.loadButton.SetVisible(app.historyHasMore)
		return
	}
	if app.searchFileMode.GetActive() {
		app.searchPlaceholder.SetText("No commits touch this path.")
	} else {
		app.searchPlaceholder.SetText("No loaded commits match this search.")
	}
	app.searchMatches = nil
	generation := app.searchGeneration
	addMainSource(150*time.Millisecond, func() bool {
		if generation == app.searchGeneration {
			app.renderGraphSearch(query)
		}
		return false
	})
}

func (app *giti) renderGraphSearch(query string) {
	generation, repo := app.searchGeneration, app.repository
	if app.searchFileMode.GetActive() {
		ctx, cancel := context.WithCancel(context.Background())
		app.searchCancel = cancel
		follow, limit := app.searchFollow.GetActive(), app.searchLimit
		// Every query owns a generation, context, and repository snapshot. The GTK
		// callback applies results only while all three still identify current work.
		go func() {
			rows, hasMore, err := repo.fileHistoryContext(ctx, query, follow, limit)
			addMainSource(0, func() bool {
				if ctx.Err() != nil || generation != app.searchGeneration || repo != app.repository {
					return false
				}
				app.searchCancel = nil
				if err != nil {
					app.searchPlaceholder.SetText("Could not search: " + err.Error())
					app.searchLoadButton.Hide()
					app.showSearchMatches(nil)
					return false
				}
				matches := make([]searchMatch, len(rows))
				for index, row := range rows {
					branches, tags := referenceLists(row.refs)
					matches[index] = searchMatch{row: row, index: index, branches: branches, tags: tags}
				}
				restoreFocus := !hasMore && app.searchLoadButton.IsFocus()
				app.searchLoadButton.SetVisible(hasMore)
				app.showSearchMatches(matches)
				if restoreFocus {
					if result := app.searchResults.GetRowAtIndex(0); result != nil {
						result.GrabFocus()
					}
				}
				return false
			})
		}()
		return
	}
	app.searchLoadButton.Hide()
	app.showSearchMatches(searchHistory(app.historyRows, query, searchOptions{app.searchMessages.GetActive(), app.searchReferences.GetActive()}))
}

func (app *giti) showSearchMatches(matches []searchMatch) {
	app.clearSearchResults()
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
		result.PackEnd(app.copySHAButton(match.row.revision), false, false, 0)
		app.searchResults.Insert(result, -1)
	}
	if !app.searchViewingResult {
		app.historyStack.SetVisibleChildName("search")
		app.loadButton.Hide()
	}
	app.searchResults.ShowAll()
}

func (app *giti) clearSearchResults() {
	if children := app.searchResults.GetChildren(); children != nil {
		children.Foreach(func(child any) { app.searchResults.Remove(child.(gtk.IWidget)) })
		children.Free()
	}
}

func (app *giti) updateSearchMode() {
	fileMode := app.searchFileMode.GetActive()
	app.searchLimit = initialHistoryLimit
	app.searchViewingResult = false
	app.searchBack.Hide()
	if fileMode {
		app.historySearch.SetPlaceholderText("File or directory path")
		app.historySearch.SetTooltipText("Enter a path relative to the repository root")
		setAccessibility(&app.historySearch.Widget, "File or directory history search", "Enter a literal path relative to the repository root")
		setAccessibility(&app.searchResults.Widget, "File history search results", "Commits that changed the requested file or directory")
		app.searchPlaceholder.SetText("No commits touch this path.")
		app.searchTextOptions.Hide()
		app.searchFileOptions.ShowAll()
	} else {
		app.historySearch.SetPlaceholderText("Search loaded commits")
		app.historySearch.SetTooltipText("Case-insensitive: exact phrases rank above separate word matches")
		setAccessibility(&app.historySearch.Widget, "Commit text search", "Search the commits currently loaded in the graph")
		setAccessibility(&app.searchResults.Widget, "Commit text search results", "Loaded commits matching the search text")
		app.searchPlaceholder.SetText("No loaded commits match this search.")
		app.searchFileOptions.Hide()
		app.searchTextOptions.ShowAll()
	}
	if !fileMode && app.searchMessages.GetActive() && len(app.historyRows) > 0 {
		app.loadHistory(false)
	} else {
		app.updateGraphSearch()
	}
}

func searchResultMarkup(match searchMatch) string {
	var badges strings.Builder
	for _, part := range referenceParts(match.branches, match.tags, match.row.upstreams) {
		badges.WriteString("  ")
		badges.WriteString(part.markup)
	}
	hints := make([]string, 0, 2)
	if match.matchesSHA {
		hints = append(hints, "matches commit SHA")
	}
	if match.matchesDescription {
		hints = append(hints, "matches commit description")
	}
	hint := ""
	if len(hints) > 0 {
		hint = "\n<span size=\"small\" foreground=\"#4b5563\">" + html.EscapeString(strings.Join(hints, " · ")) + "</span>"
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

func (app *giti) revealHistoryRevision(revision string) {
	if !app.selectHistoryRevision(revision) {
		app.loadHistoryTo(revision, false)
	}
}

func (app *giti) openSearchResult(index int) {
	if index >= 0 && index < len(app.searchMatches) {
		revision := app.searchMatches[index].revision
		app.searchViewingResult = true
		app.historyStack.SetVisibleChildName("graph")
		app.searchBack.Show()
		app.loadButton.SetVisible(app.historyHasMore)
		app.revealHistoryRevision(revision)
	}
}

func (app *giti) setCommitHeader(details commitDetails) {
	// Rebuild the header as one snapshot so controls cannot retain callbacks or
	// copy targets from the previously selected commit.
	app.headerReferenceButtons = nil
	if children := app.commitHeader.GetChildren(); children != nil {
		children.Foreach(func(child any) { app.commitHeader.Remove(child.(gtk.IWidget)) })
		children.Free()
	}
	title := must(gtk.LabelNew(""))
	title.SetXAlign(0)
	title.SetSelectable(true)
	title.SetEllipsize(pango.ELLIPSIZE_END)
	title.SetMarkup("<span size=\"large\" weight=\"bold\">" + html.EscapeString(details.subject) + "</span>")
	titleRow := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 8))
	titleRow.PackStart(title, true, true, 0)
	// Keep aggregate statistics outside the ellipsized title so they remain
	// visible even for unusually long subjects.
	if details.statistics {
		additions := must(gtk.LabelNew(fmt.Sprintf("+%d", details.additions)))
		deletions := must(gtk.LabelNew(fmt.Sprintf("−%d", details.deletions)))
		for _, badge := range []*gtk.Label{additions, deletions} {
			context, _ := badge.GetStyleContext()
			context.AddClass("giti-stat")
			badge.SetTooltipText("Line totals exclude binary and untracked files")
			titleRow.PackStart(badge, false, false, 0)
		}
		additionContext, _ := additions.GetStyleContext()
		additionContext.AddClass("giti-additions")
		deletionContext, _ := deletions.GetStyleContext()
		deletionContext.AddClass("giti-deletions")
		if details.untracked > 0 {
			untracked := must(gtk.LabelNew(fmt.Sprintf("%d untracked", details.untracked)))
			context, _ := untracked.GetStyleContext()
			context.AddClass("giti-stat")
			context.AddClass("giti-untracked")
			untracked.SetTooltipText("Untracked files have no line counts")
			titleRow.PackStart(untracked, false, false, 0)
		}
	}
	app.commitHeader.PackStart(titleRow, false, false, 0)
	if details.sha == "" {
		app.commitHeader.ShowAll()
		return
	}
	commit := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 8))
	commitLabel := must(gtk.LabelNew(""))
	commitLabel.SetXAlign(0)
	commitLabel.SetSelectable(true)
	commitLabel.SetMarkup(fmt.Sprintf("<span foreground=\"#4b5563\"><b>Commit</b> <tt>%s</tt></span>", html.EscapeString(details.sha)))
	commit.PackStart(commitLabel, false, false, 0)
	commit.PackStart(app.copySHAButton(details.sha), false, false, 0)
	app.commitHeader.PackStart(commit, false, false, 0)
	meta := must(gtk.LabelNew(""))
	meta.SetXAlign(0)
	meta.SetLineWrap(true)
	meta.SetSelectable(true)
	meta.SetMarkup(fmt.Sprintf("<span foreground=\"#4b5563\"><b>Author</b> %s &lt;%s&gt;  ·  %s\n<b>Committer</b> %s &lt;%s&gt;  ·  %s</span>", html.EscapeString(details.author), html.EscapeString(details.authorEmail), html.EscapeString(details.authored), html.EscapeString(details.committer), html.EscapeString(details.committerEmail), html.EscapeString(details.committed)))
	app.commitHeader.PackStart(meta, false, false, 0)
	if len(details.branches) > 0 || len(details.tags) > 0 {
		refs := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 6))
		// Reference badges are buttons because their visible text may be shortened;
		// each closure retains the complete copy value and relationship description.
		addBadge := func(target *gtk.Box, value, kind, markup, description string) *gtk.Button {
			display, _, _, _ := referenceAppearance(value, kind)
			copyValue := display
			if kind == "branch" && strings.HasSuffix(value, headRefSuffix) {
				copyValue = strings.TrimSuffix(value, headRefSuffix)
			}
			label := must(gtk.LabelNew(""))
			label.SetXAlign(0)
			label.SetEllipsize(pango.ELLIPSIZE_END)
			label.SetMaxWidthChars(28)
			if markup == "" {
				markup = referenceBadge(value, kind)
			}
			label.SetMarkup(markup)
			button := must(gtk.ButtonNew())
			button.SetRelief(gtk.RELIEF_NONE)
			button.SetTooltipText("Copy " + kind + ": " + copyValue)
			if description == "" {
				description = "Copy the complete reference name to the clipboard"
			}
			setAccessibility(&button.Widget, "Copy "+kind+" "+copyValue, description)
			context, _ := button.GetStyleContext()
			context.AddClass("giti-ref-copy")
			button.Add(label)
			button.Connect("clicked", func() {
				app.copyToClipboard(copyValue, "Copied "+kind+" to clipboard.")
			})
			app.headerReferenceButtons = append(app.headerReferenceButtons, button)
			target.PackStart(button, false, false, 0)
			return button
		}
		// Configured upstream pairs render as joined visual segments while keeping
		// independent copy targets for the local and remote names.
		for _, part := range branchReferenceParts(details.branches, details.upstreams) {
			if part.overflow {
				more := must(gtk.ButtonNewWithLabel(part.label))
				more.SetRelief(gtk.RELIEF_NONE)
				more.SetTooltipText("Show all branches for this commit")
				setAccessibility(&more.Widget, "Show hidden branches", "Open the complete branch and tag list")
				more.Connect("clicked", func() { app.showReferences(details.branches, details.tags) })
				refs.PackStart(more, false, false, 0)
				continue
			}
			if !part.synced {
				addBadge(refs, part.branches[0], "branch", "", "")
				continue
			}
			group := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 0))
			remote, local := part.branches[0], part.branches[1]
			remoteDisplay, _, _, _ := referenceAppearance(remote, "branch")
			localDisplay, _, _, _ := referenceAppearance(local, "branch")
			for _, button := range []*gtk.Button{
				addBadge(group, remote, "branch", part.segments[0], "Copy the remote branch "+remoteDisplay+"; it matches local branch "+localDisplay),
				addBadge(group, local, "branch", part.segments[1], "Copy the local branch "+localDisplay+"; it matches configured upstream "+remoteDisplay),
			} {
				context, _ := button.GetStyleContext()
				context.AddClass("giti-ref-joined")
			}
			refs.PackStart(group, false, false, 0)
		}
		for _, tag := range details.tags[:min(2, len(details.tags))] {
			addBadge(refs, tag, "tag", "", "")
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
		parentLabel := must(gtk.LabelNew("Parents:"))
		parentLabel.SetSelectable(true)
		parents.PackStart(parentLabel, false, false, 0)
		for _, parent := range details.parents {
			parent := parent
			button := must(gtk.ButtonNewWithLabel("↖ " + parent[:7]))
			button.SetRelief(gtk.RELIEF_NONE)
			button.SetTooltipText("Open parent " + parent)
			button.Connect("clicked", func() {
				app.historySearch.SetText("")
				app.revealHistoryRevision(parent)
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
	if app.diffFindBox.GetVisible() {
		app.closeDiffFind()
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
	buffer := must(gtk.TextBufferNew(nil))
	buffer.CreateTag("title", map[string]any{"weight": int(pango.WEIGHT_BOLD), "scale": 1.2})
	buffer.CreateTag("section", map[string]any{"weight": int(pango.WEIGHT_BOLD)})
	insert := func(text, tag string) {
		iter := buffer.GetEndIter()
		if tag == "" {
			buffer.Insert(iter, text)
		} else {
			buffer.InsertWithTagByName(iter, text, tag)
		}
	}
	insert("Branches and tags", "title")
	createdTags := make(map[string]bool, 4)
	for _, section := range []struct {
		name, kind string
		values     []string
	}{{"Branches", "branch", branches}, {"Tags", "tag", tags}} {
		if len(section.values) == 0 {
			continue
		}
		insert("\n\n"+section.name, "section")
		for _, value := range section.values {
			display, tag, background, foreground := referenceAppearance(value, section.kind)
			if !createdTags[tag] {
				buffer.CreateTag(tag, map[string]any{"background": background, "foreground": foreground, "weight": int(pango.WEIGHT_BOLD), "pixels-above-lines": 3, "pixels-below-lines": 3})
				createdTags[tag] = true
			}
			insert("\n "+display+" ", tag)
		}
	}
	if len(branches) == 0 && len(tags) == 0 {
		insert("\n\nNo branches or tags.", "")
	}
	app.referencesView = must(gtk.TextViewNewWithBuffer(buffer))
	app.referencesView.SetEditable(false)
	app.referencesView.SetCursorVisible(false)
	app.referencesView.SetWrapMode(gtk.WRAP_CHAR)
	app.referencesView.SetAcceptsTab(false)
	context, _ := app.referencesView.GetStyleContext()
	context.AddClass("giti-references")
	app.referencesPage.PackStart(app.referencesView, true, true, 0)
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
	row := app.historyRows[index]
	merge := row.kind == "commit" && len(row.parents) > 1
	sameSelection := app.currentRow != nil && app.currentRow.kind == "commit" && app.currentRow.revision == row.revision
	app.fullMergeToggle.HandlerBlock(app.fullMergeHandler)
	if !merge || !sameSelection {
		app.fullMergeToggle.SetActive(false)
	}
	app.fullMergeToggle.SetVisible(merge)
	app.fullMergeToggle.HandlerUnblock(app.fullMergeHandler)
	mergeResolution := merge && !app.fullMergeToggle.GetActive()
	description := "Selected " + row.subject
	if row.kind == "commit" {
		topology := "root commit"
		if len(row.parents) == 1 {
			topology = "one parent"
		} else if len(row.parents) > 1 {
			topology = fmt.Sprintf("merge commit with %d parents", len(row.parents))
		}
		description = fmt.Sprintf("Selected %s, %s, by %s", row.subject, topology, row.author)
	}
	setAccessibility(&app.historyView.Widget, "Commit history", description)
	// Cancel both layers of the previous selection. The generation numbers also
	// reject callbacks that reached GLib just before their contexts were canceled.
	app.selectionGeneration++
	generation := app.selectionGeneration
	if app.selectionCancel != nil {
		app.selectionCancel()
	}
	if app.diffCancel != nil {
		app.diffCancel()
	}
	app.diffGeneration++
	app.resetDiffOverview()
	app.rememberDiffScroll()
	app.clearDiff()
	app.setCommitHeader(commitDetails{subject: "Loading commit details…"})
	app.fileSummary.SetText("Loading changed files…")
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
		// Commit metadata and changed-file statistics are independent Git queries,
		// but run serially to avoid competing Git work for a short-lived selection.
		details, detailsErr := commitDetails{}, error(nil)
		if row.kind == "commit" {
			details, detailsErr = repo.commitDetailsContext(ctx, row.revision)
		}
		files, loadErr := repo.changedFilesForViewContext(ctx, row, ignoreWhitespace, mergeResolution)
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
			// Untracked files are a distinct state: they count toward the file summary
			// but cannot contribute reliable line totals until Git tracks them.
			added, deleted, updated, untracked, additions, deletions := 0, 0, 0, 0, 0, 0
			for _, file := range files {
				switch {
				case file.status == "??":
					untracked++
				case strings.HasPrefix(file.status, "A"):
					added++
				case strings.HasPrefix(file.status, "D"):
					deleted++
				default:
					updated++
				}
				if file.status != "??" && !file.binary {
					additions, deletions = additions+file.additions, deletions+file.deletions
				}
			}
			details.additions, details.deletions, details.untracked = additions, deletions, untracked
			details.statistics = row.kind != "conflict" && row.kind != "resolved" && !mergeResolution
			if row.kind != "commit" {
				details.subject = row.subject
			}
			app.setCommitHeader(details)
			summary := fmt.Sprintf("%d files · %d added · %d deleted · %d updated", len(files), added, deleted, updated)
			markup := fmt.Sprintf("<b>%d files</b> · <span foreground=\"#2e8c47\">%d added</span> · <span foreground=\"#c7404d\">%d deleted</span> · %d updated", len(files), added, deleted, updated)
			switch {
			case row.kind == "conflict":
				summary = fmt.Sprintf("%d conflicts · Needs resolution", len(files))
				markup = fmt.Sprintf("<b>%d conflicts</b> · <span foreground=\"#c7404d\">Needs resolution</span>", len(files))
			case row.kind == "resolved":
				summary = fmt.Sprintf("%d conflicts · Resolution applied", len(files))
				markup = fmt.Sprintf("<b>%d conflicts</b> · <span foreground=\"#2e8c47\">Resolution applied</span>", len(files))
			case mergeResolution:
				summary = fmt.Sprintf("%d merge-resolution files", len(files))
				markup = fmt.Sprintf("<b>%d files</b> · Merge resolution", len(files))
			case untracked > 0:
				summary += fmt.Sprintf(" · %d untracked", untracked)
				markup += fmt.Sprintf(" · <span foreground=\"#4b5563\">%d untracked</span>", untracked)
			}
			app.fileSummary.SetMarkup(markup)
			app.fileSummary.SetTooltipText(summary)
			// Replace the model only after its summary and header agree with the same
			// result, then restore the previous path when it is still present.
			app.files = files
			target := 0
			for fileIndex, file := range files {
				iter := app.fileStore.Append()
				stat := fmt.Sprintf("<span foreground=\"#2e8c47\">+%d</span>  <span foreground=\"#c7404d\">−%d</span>", file.additions, file.deletions)
				if file.status == "??" {
					stat = "<span foreground=\"#6b7280\">Untracked</span>"
				} else if file.conflict != "" {
					color := "#c7404d"
					if file.status == "✓" {
						color = "#2e8c47"
					}
					stat = fmt.Sprintf("<span foreground=\"%s\">%s</span>", color, html.EscapeString(file.conflict))
				} else if mergeResolution {
					stat = "<span foreground=\"#6b7280\">Combined</span>"
				} else if file.binary {
					stat = "<span foreground=\"#6b7280\">Binary</span>"
				}
				app.fileStore.Set(iter, []int{0, 1}, []any{file.label(), stat})
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
	// File loads are independently cancelable from history loads; both generation
	// values must match before a patch can update the shared diff widgets.
	app.diffGeneration++
	generation, selectionGeneration := app.diffGeneration, app.selectionGeneration
	if app.diffCancel != nil {
		app.diffCancel()
	}
	file := &app.files[index]
	app.rememberDiffScroll()
	app.currentFile = file
	app.diffLoaded = false
	app.fullFileToggle.SetSensitive(false)
	app.clearDiff()
	app.resetDiffOverview()
	app.diffScroller.GetHAdjustment().SetValue(0)
	app.diffScroller.GetVAdjustment().SetValue(0)
	ctx, cancel := context.WithCancel(context.Background())
	app.diffCancel = cancel
	repo, row, selectedFile := app.repository, *app.currentRow, *file
	position := app.diffScroll[diffKey(row, selectedFile)]
	ignoreWhitespace, preferFullFile := !app.whitespaceToggle.GetActive(), app.fullFilePreferred
	mergeResolution := row.kind == "commit" && len(row.parents) > 1 && !app.fullMergeToggle.GetActive()
	go func() {
		size := repo.fileSizeContext(ctx, row, selectedFile)
		fullFile := preferFullFile && size <= fullFileLimit
		patch, loadErr := repo.diffForViewContext(ctx, row, selectedFile, ignoreWhitespace, fullFile, mergeResolution)
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
			app.fullFileToggle.SetActive(fullFile)
			if !allowed {
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
	lines := displayLines(patch)
	app.overviewMarkers, app.overviewLines = app.overviewMarkers[:0], len(lines)
	for index, line := range lines {
		if line.tag != "" {
			app.overviewMarkers = append(app.overviewMarkers, overviewMarker{line: index, added: line.tag == "added"})
		}
	}
	for start := 0; start < len(lines); {
		end, text := start+1, strings.Builder{}
		text.WriteString(lines[start].text)
		for end < len(lines) && lines[end].tag == lines[start].tag {
			text.WriteString(lines[end].text)
			end++
		}
		iter := app.diffBuffer.GetEndIter()
		if lines[start].tag == "" {
			app.diffBuffer.Insert(iter, text.String())
		} else {
			app.diffBuffer.InsertWithTagByName(iter, text.String(), lines[start].tag)
		}
		start = end
	}
	app.diffOverviewReveal.SetRevealChild(app.fullFileToggle.GetActive() && len(app.overviewMarkers) > 0)
	app.diffOverview.QueueDraw()
	app.updateDiffFind()
}

func (app *giti) clearDiff() {
	app.diffBuffer.SetText("")
	app.updateDiffFind()
}

type overviewMarker struct {
	line  int
	added bool
}

type displayLine struct {
	text, tag string
}

func displayLines(patch string) []displayLine {
	lines := make([]displayLine, 0)
	inHeader, prefixWidth := true, 1
	for _, line := range splitAfterLines(patch) {
		if strings.HasPrefix(line, "@@") {
			inHeader = false
			// A normal @@ hunk has one prefix column; a combined @@@ hunk has
			// two, one per parent. A line is colored only when those columns agree.
			prefixWidth = max(1, len(line)-len(strings.TrimLeft(line, "@"))-1)
		} else if inHeader && (strings.HasPrefix(line, "diff --") || strings.HasPrefix(line, "index ") ||
			strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ")) {
			continue
		}
		tag := ""
		if !inHeader && len(line) >= prefixWidth {
			prefix := line[:prefixWidth]
			switch {
			case strings.Contains(prefix, "+") && !strings.Contains(prefix, "-"):
				tag = "added"
			case strings.Contains(prefix, "-") && !strings.Contains(prefix, "+"):
				tag = "removed"
			}
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
		app.loadHistory(false)
	}
}

func (app *giti) resetDiffOverview() {
	app.overviewMarkers, app.overviewLines = app.overviewMarkers[:0], 0
	if app.diffOverviewReveal != nil {
		app.diffOverviewReveal.SetRevealChild(false)
		app.diffOverview.QueueDraw()
	}
}

func (app *giti) scrollDiffOverview(y float64) {
	height := app.diffOverview.GetAllocatedHeight()
	if height < 1 || app.overviewLines < 1 {
		return
	}
	adjustment := app.diffScroller.GetVAdjustment()
	ratio := max(0, min(y/float64(height), 1))
	adjustment.SetValue(adjustment.GetLower() + ratio*max(0, adjustment.GetUpper()-adjustment.GetLower()-adjustment.GetPageSize()))
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
	if app.historyCancel != nil {
		app.historyCancel()
	}
	app.searchGeneration++
	if app.searchCancel != nil {
		app.searchCancel()
		app.searchCancel = nil
	}
	app.clearSearchResults()
	app.historyRows, app.files, app.searchMatches = nil, nil, nil
	app.historyHasMore, app.searchLimit = false, initialHistoryLimit
	app.searchViewingResult = false
	app.diffScroll = make(map[string]scrollPosition)
	app.currentRow, app.currentFile, app.diffLoaded = nil, nil, false
	app.historySearch.SetText("")
	app.searchBack.Hide()
	app.searchLoadButton.Hide()
	app.historyStore.Clear()
	app.fileStore.Clear()
	app.fileSummary.SetText("Select a history entry to see changed files")
	app.fileSummary.SetTooltipText("")
	app.setCommitHeader(commitDetails{})
	app.closeDiffFind()
	app.diffFind.SetText("")
	app.clearDiff()
	app.fullFilePreferred = false
	app.fullFileToggle.HandlerBlock(app.fullFileHandler)
	app.fullFileToggle.SetActive(false)
	app.fullFileToggle.HandlerUnblock(app.fullFileHandler)
	app.fullMergeToggle.HandlerBlock(app.fullMergeHandler)
	app.fullMergeToggle.SetActive(false)
	app.fullMergeToggle.SetVisible(false)
	app.fullMergeToggle.HandlerUnblock(app.fullMergeHandler)
	app.resetDiffOverview()
}

func (app *giti) hideResident() {
	app.persistUIState()
	app.clearRepositoryView()
	app.window.Hide()
	app.stateMu.Lock()
	app.busy, app.idleDeadline = false, time.Now().Add(idleDuration)
	app.stateMu.Unlock()
}

func (app *giti) openRepository(path string, history historySpec) bool {
	repo, err := newRepository(path, history)
	if err != nil {
		app.showError(err)
		app.stateMu.Lock()
		app.busy, app.idleDeadline = false, time.Now().Add(idleDuration)
		app.stateMu.Unlock()
		return false
	}
	app.repository, app.historyLimit = repo, initialHistoryLimit
	app.clearRepositoryView()
	app.searchTextMode.SetActive(repo.searchPath == "")
	app.searchFileMode.SetActive(repo.searchPath != "")
	app.searchFollow.SetActive(repo.follow)
	app.historySearch.SetText(repo.searchPath)
	app.whitespaceToggle.SetActive(false)
	app.window.SetTitle(repo.windowTitle())
	app.window.ShowAll()
	app.notificationGeneration++
	app.notification.Hide()
	app.window.Maximize()
	app.window.Present()
	app.loadHistory(false)
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

func (app *giti) showNotification(message string, duration time.Duration) {
	app.notificationGeneration++
	generation := app.notificationGeneration
	app.notificationLabel.SetText(message)
	app.notification.Show()
	addMainSource(duration, func() bool {
		if generation == app.notificationGeneration {
			app.notification.Hide()
		}
		return false
	})
}

func (app *giti) copyToClipboard(text, message string) {
	must(gtk.ClipboardGet(gdk.SELECTION_CLIPBOARD)).SetText(text)
	app.showNotification(message, 2*time.Second)
}

type clipboardAction struct{ label, text, message string }

func fileCopyActions(repositoryPath, relativePath string) []clipboardAction {
	return []clipboardAction{
		{"Copy path", relativePath, "Copied path to clipboard."},
		{"Copy full path", filepath.Join(repositoryPath, relativePath), "Copied full path to clipboard."},
	}
}
