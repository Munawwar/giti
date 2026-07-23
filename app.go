package main

import (
	"context"
	_ "embed"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
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
	textSearchBatch     = 500
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
	searchDepths               map[string]int
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
	headerTitleRow             *gtk.Box
	headerCommit               *gtk.Box
	headerDetails              *gtk.Box
	diffBuffer                 *gtk.TextBuffer
	diffView                   *gtk.TextView
	diffScroller               *gtk.ScrolledWindow
	diffGutter                 *gtk.DrawingArea
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
	diffLineNumbers            []diffLineNumber
	diffFindMatches            []diffFindMatch
	overviewLines              int
	diffGutterDigits           int
	diffGutterWidth            int
	diffFindIndex              int
	diffFindLimited            bool
	fullFileHandler            glib.SignalHandle
	fullMergeHandler           glib.SignalHandle
	application                *gtk.Application
}

type scrollPosition struct{ horizontal, vertical float64 }

type diffFindMatch struct{ start, end int }

type diffLineKind int8

const (
	diffLineContext diffLineKind = iota
	diffLineAdded
	diffLineRemoved
)

type diffLineNumber struct {
	old, new uint32
	kind     diffLineKind
}

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
	// Text and file searches query history independently, leaving the regular
	// graph and its topology intact until the user opens a result.
	app.historySearch = must(gtk.SearchEntryNew())
	app.historySearch.SetTooltipText("Case-insensitive: exact phrases rank above separate word matches")
	app.historySearch.Connect("changed", func() {
		app.searchViewingResult = false
		app.searchBack.Hide()
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
		app.updateGraphSearch()
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
	app.diffBuffer.CreateTag("hunk", map[string]any{"paragraph-background": "#eef0f2", "foreground": "#4b5563"})
	app.diffBuffer.CreateTag(diffFindTag, map[string]any{"background": "#fff2a8"})
	app.diffBuffer.CreateTag(diffFindCurrentTag, map[string]any{"background": "#f6bd4f", "foreground": "#2d2100"})
	app.diffView = must(gtk.TextViewNewWithBuffer(app.diffBuffer))
	setAccessibility(&app.diffView.Widget, "Commit diff", "Patch for the selected file; additions begin with plus and removals with minus")
	app.diffView.SetEditable(false)
	app.diffView.SetCursorVisible(false)
	app.diffView.SetMonospace(true)
	app.diffView.SetWrapMode(gtk.WRAP_NONE)
	app.buildDiffOverview()
	app.buildDiffGutter()

	app.whitespaceToggle = must(gtk.CheckButtonNewWithLabel("Whitespace changes"))
	app.whitespaceToggle.SetTooltipText("Show whitespace-only changes; off by default, diffs use git --ignore-all-space")
	app.whitespaceToggle.Connect("toggled", app.onWhitespaceToggled)
	app.fullFileToggle = must(gtk.CheckButtonNewWithLabel("Show full file"))
	app.fullFileToggle.SetTooltipText("Show unchanged lines from the complete file")
	app.fullFileHandler = app.fullFileToggle.Connect("toggled", func() {
		app.fullFilePreferred = app.fullFileToggle.GetActive()
		if app.currentFile != nil {
			app.onFileSelected()
		}
	})
	app.fullMergeToggle = must(gtk.CheckButtonNewWithLabel("Full merge"))
	app.fullMergeToggle.SetTooltipText("Off: show the compact combined merge-resolution diff; on: compare the merge with its first parent")
	app.fullMergeToggle.SetNoShowAll(true)
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
	app.headerTitleRow = must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 8))
	headerActionRow := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 0))
	app.headerCommit = must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 8))
	app.headerDetails = must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 2))
	headerActionRow.PackStart(app.headerCommit, false, false, 0)
	headerActionRow.PackEnd(app.whitespaceToggle, false, false, 8)
	headerActionRow.PackEnd(app.fullFileToggle, false, false, 0)
	headerActionRow.PackEnd(app.fullMergeToggle, false, false, 8)
	app.commitHeader.PackStart(app.headerTitleRow, false, false, 0)
	app.commitHeader.PackStart(headerActionRow, false, false, 0)
	app.commitHeader.PackStart(app.headerDetails, false, false, 0)
	diffBox := must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 4))
	diffBox.PackStart(app.commitHeader, false, false, 0)
	app.diffScroller = scroller(app.diffView)
	app.diffScroller.GetVAdjustment().Connect("value-changed", app.diffGutter.QueueDraw)
	app.diffOverviewReveal = must(gtk.RevealerNew())
	app.diffOverviewReveal.SetTransitionDuration(0)
	app.diffOverviewReveal.Add(app.diffOverview)
	diffPage := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 0))
	diffPage.PackStart(app.diffGutter, false, true, 0)
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
	app.diffGutter.Hide()
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

func (app *giti) buildDiffGutter() {
	app.diffGutter = must(gtk.DrawingAreaNew())
	app.diffGutterDigits, app.diffGutterWidth = 2, 80
	app.diffGutter.SetSizeRequest(app.diffGutterWidth, -1)
	app.diffGutter.SetVAlign(gtk.ALIGN_FILL)
	app.diffGutter.SetVExpand(true)
	app.diffGutter.SetTooltipText("Old and new file line numbers")
	setAccessibility(&app.diffGutter.Widget, "Diff line numbers", "Old or first-parent line numbers followed by resulting-file line numbers")
	app.diffGutter.Connect("draw", func(_ *gtk.DrawingArea, context *cairo.Context) bool {
		width, height := app.diffGutter.GetAllocatedWidth(), app.diffGutter.GetAllocatedHeight()
		context.SetSourceRGB(.965, .97, .975)
		context.Paint()
		if len(app.diffLineNumbers) == 0 || height < 1 {
			return false
		}
		// TextView reports line positions in buffer coordinates; subtract its
		// visible origin so the independent gutter tracks vertical scrolling.
		// Wrap is disabled, so every logical line has the same measured height.
		visible := app.diffView.GetVisibleRect()
		first := app.diffBuffer.GetStartIter()
		firstY, lineHeight := app.diffView.GetLineYrange(first)
		if lineHeight < 1 {
			return false
		}
		line := max(0, (visible.GetY()-firstY)/lineHeight)
		context.SelectFontFace("monospace", cairo.FONT_SLANT_NORMAL, cairo.FONT_WEIGHT_NORMAL)
		context.SetFontSize(max(8, float64(lineHeight)*.68))
		digitWidth := context.TextExtents("0").XAdvance
		// Resize only when the file's digit count or scaled font changes, keeping
		// the two right-aligned columns compact without clipping large files.
		columnWidth := int(math.Ceil(digitWidth*float64(app.diffGutterDigits) + 12))
		requiredWidth := columnWidth*2 + 1
		if requiredWidth != app.diffGutterWidth {
			app.diffGutterWidth = requiredWidth
			app.diffGutter.SetSizeRequest(requiredWidth, -1)
			return false
		}
		font := context.FontExtents()
		for line < len(app.diffLineNumbers) {
			iter := app.diffBuffer.GetIterAtLine(line)
			bufferY, rowHeight := app.diffView.GetLineYrange(iter)
			y := bufferY - visible.GetY()
			if y >= height {
				break
			}
			numbers := app.diffLineNumbers[line]
			switch numbers.kind {
			case diffLineAdded:
				context.SetSourceRGB(.843, .961, .867)
			case diffLineRemoved:
				context.SetSourceRGB(.976, .843, .851)
			default:
				context.SetSourceRGB(.965, .97, .975)
			}
			context.Rectangle(0, float64(y), float64(width), float64(rowHeight))
			context.Fill()
			switch numbers.kind {
			case diffLineAdded:
				context.SetSourceRGB(.09, .30, .13)
			case diffLineRemoved:
				context.SetSourceRGB(.41, .13, .15)
			default:
				context.SetSourceRGB(.42, .45, .50)
			}
			baseline := float64(y) + (float64(rowHeight)-font.Ascent-font.Descent)/2 + font.Ascent
			for column, number := range []uint32{numbers.old, numbers.new} {
				if number == 0 {
					continue
				}
				text := strconv.FormatUint(uint64(number), 10)
				context.MoveTo(float64((column+1)*columnWidth-6)-context.TextExtents(text).XAdvance, baseline)
				context.ShowText(text)
			}
			line++
		}
		context.SetSourceRGB(.83, .85, .87)
		context.SetLineWidth(1)
		for _, x := range []float64{float64(columnWidth) + .5, float64(width) - .5} {
			context.MoveTo(x, 0)
			context.LineTo(x, float64(height))
		}
		context.Stroke()
		return false
	})
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

// Search results are independent of graph pagination and selection-driven loads;
// only an explicit repository refresh needs to query them for new commits.
func (app *giti) loadHistory(refreshSearch bool) {
	app.loadHistoryTo("", refreshSearch)
}

func (app *giti) loadHistoryTo(reveal string, refreshSearch bool) {
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
	ignoreWhitespace := !app.whitespaceToggle.GetActive()
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
			rows, hasMore, err = repo.historyContext(ctx, limit, ignoreWhitespace, false)
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
			if refreshSearch {
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
	app.historyRows, app.files, app.searchMatches, app.searchDepths = nil, nil, nil, nil
	app.historyHasMore, app.searchLimit = false, initialHistoryLimit
	app.searchViewingResult = false
	app.diffScroll = make(map[string]scrollPosition)
	app.currentRow, app.currentFile, app.diffLoaded = nil, nil, false
	app.historySearch.SetProgressFraction(0)
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
	app.diffLineNumbers = nil
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
