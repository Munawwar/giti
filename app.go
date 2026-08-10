package main

/*
#cgo pkg-config: gdk-x11-3.0
#include <stdint.h>
typedef struct _GdkWindow GdkWindow;
uint32_t gdk_x11_get_server_time(GdkWindow *window);
*/
import "C"

import (
	"context"
	_ "embed"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/gotk3/gotk3/cairo"
	"github.com/gotk3/gotk3/gdk"
	"github.com/gotk3/gotk3/glib"
	"github.com/gotk3/gotk3/gtk"
	"github.com/gotk3/gotk3/pango"
)

const (
	idleDuration        = 12 * time.Hour
	initialHistoryLimit = 50
	branchResultLimit   = 100
	maxAutoHistory      = 5000
	textSearchBatch     = 500
	maxDiffFindMatches  = 5000
	diffFindTag         = "find-match"
	diffFindCurrentTag  = "find-match-current"
)

const (
	fileLabelColumn = iota
	fileStatColumn
	fileIndexColumn
	fileTooltipColumn
)

//go:embed logo/giti-logo.png
var appIconPNG []byte

//go:embed logo/file-tree-symbolic.svg
var fileTreeIconSVG []byte

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
row.giti-selection-row:selected,
row.giti-selection-row:selected:focus {
  background-color: #fff0e8;
  background-image: none;
  color: #2d1b12;
}
row.giti-selection-row:selected:backdrop {
  background-color: #fff7f2;
  background-image: none;
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
.giti-flat-button {
  background-color: transparent;
  background-image: none;
  border-color: transparent;
  border-radius: 6px;
  box-shadow: none;
  min-height: 18px;
  min-width: 18px;
  padding: 5px;
}
.giti-flat-button:hover {
  background-color: alpha(#6b7280, 0.12);
}
.giti-flat-button:checked {
  background-color: #eef0f2;
}
.giti-flat-button:focus {
  box-shadow: 0 0 0 1px alpha(@theme_selected_bg_color, 0.55);
}
box.giti-load-footer {
  padding: 4px 6px;
}
label.giti-secondary-label {
  color: #6b7280;
  font-size: smaller;
}
.giti-view-switcher {
  background-color: #e5e7eb;
  border: 1px solid #d1d5db;
  border-radius: 6px;
}
.giti-view-option {
  background-color: transparent;
  background-image: none;
  border-width: 0;
  border-radius: 0;
  box-shadow: none;
  min-height: 20px;
  min-width: 24px;
  padding: 4px 8px;
}
.giti-view-option:hover {
  background-color: alpha(#ffffff, 0.45);
}
.giti-view-option:checked {
  background-color: #ffffff;
}
.giti-view-list {
  border-radius: 5px 0 0 5px;
}
.giti-view-tree {
  border-left-color: #d1d5db;
  border-left-style: solid;
  border-left-width: 1px;
  border-radius: 0 5px 5px 0;
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
	windowHeader               *gtk.HeaderBar
	branchButton               *gtk.MenuButton
	branchLabel                *gtk.Label
	branchSearch               *gtk.SearchEntry
	branchList                 *gtk.ListBox
	branchLimitLabel           *gtk.Label
	branchPopover              *gtk.Popover
	branchRevisions            []string
	branchLabels               []string
	branchMarkups              []string
	branchVisible              []int
	branchRepositoryPath       string
	mainMenu                   *gtk.MenuButton
	mainMenuShortcuts          []*gtk.Label
	historyStore, fileStore    *gtk.ListStore
	fileTreeStore              *gtk.TreeStore
	fileModel                  *gtk.TreeModel
	historyView, fileView      *gtk.TreeView
	historyScroller            *gtk.ScrolledWindow
	fileSummary                *gtk.Label
	fileSearch                 *gtk.SearchEntry
	fileSearchReveal           *gtk.Revealer
	fileSearchToggle           *gtk.ToggleButton
	fileListToggle             *gtk.RadioButton
	fileTreeToggle             *gtk.RadioButton
	graphColumn                *gtk.TreeViewColumn
	mainPane, repositoryPane   *gtk.Paned
	historySearch              *gtk.SearchEntry
	searchSpinner              *gtk.Spinner
	searchIconSpacer           *gdk.Pixbuf
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
	searchLoadFooter           *loadFooter
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
	whitespacePreferred        bool
	loadButton                 *gtk.Button
	loadFooter                 *loadFooter
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
	whitespaceHandler          glib.SignalHandle
	historySelectionHandler    glib.SignalHandle
	application                *gtk.Application
}

type loadFooter struct {
	box                    *gtk.Box
	count                  *gtk.Label
	button                 *gtk.Button
	actionLabel, busyLabel string
	busy, restoreFocus     bool
}

func newLoadFooter(actionLabel, busyLabel, accessibleName, description string) *loadFooter {
	footer := &loadFooter{actionLabel: actionLabel, busyLabel: busyLabel}
	footer.box = must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 8))
	footer.box.SetNoShowAll(true)
	footer.count = must(gtk.LabelNew(""))
	footer.count.SetXAlign(0)
	footer.count.SetEllipsize(pango.ELLIPSIZE_END)
	setAccessibilityRoleAlert(&footer.count.Widget)
	countContext, _ := footer.count.GetStyleContext()
	countContext.AddClass("giti-secondary-label")
	footer.button = must(gtk.ButtonNewWithLabel(actionLabel))
	footer.button.SetRelief(gtk.RELIEF_NONE)
	setAccessibility(&footer.button.Widget, accessibleName, description)
	buttonContext, _ := footer.button.GetStyleContext()
	buttonContext.AddClass("giti-flat-button")
	boxContext, _ := footer.box.GetStyleContext()
	boxContext.AddClass("giti-load-footer")
	footer.box.PackStart(footer.count, true, true, 0)
	footer.box.PackEnd(footer.button, false, false, 0)
	return footer
}

func (footer *loadFooter) update(count int, noun string, visible bool) {
	if count != 1 {
		noun += "s"
	}
	exact, compact := strconv.Itoa(count), strconv.Itoa(count)
	if count >= 10000 {
		value, suffix := float64(count)/1000, "k"
		if count >= 1000000000 {
			value, suffix = float64(count)/1000000000, "B"
		} else if count >= 1000000 {
			value, suffix = float64(count)/1000000, "M"
		}
		precision := 1
		if value >= 100 {
			precision = 0
		}
		compact = strings.TrimSuffix(strconv.FormatFloat(value, 'f', precision, 64), ".0") + suffix
	}
	fullCount := fmt.Sprintf("%s %s loaded", exact, noun)
	footer.count.SetText(fmt.Sprintf("%s %s loaded", compact, noun))
	footer.count.SetTooltipText(fullCount)
	setAccessibility(&footer.count.Widget, fullCount, "Pagination status")
	footer.setVisible(visible)
}

func (footer *loadFooter) setVisible(visible bool) {
	footer.count.SetVisible(visible)
	footer.button.SetVisible(visible)
	footer.box.SetVisible(visible)
}

func (footer *loadFooter) setBusy(busy bool) {
	if busy && !footer.busy {
		footer.restoreFocus = footer.button.IsFocus()
	}
	footer.busy = busy
	footer.button.SetSensitive(!busy)
	if busy {
		footer.button.SetLabel(footer.busyLabel)
	} else {
		footer.button.SetLabel(footer.actionLabel)
	}
}

func (footer *loadFooter) takeFocus() bool {
	restore := footer.restoreFocus
	footer.restoreFocus = false
	return restore
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

func (app *giti) toggleFileDirectory(path *gtk.TreePath) {
	if app.fileView.RowExpanded(path) {
		app.fileView.CollapseRow(path)
	} else {
		app.fileView.ExpandRow(path, false)
	}
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
	app.fileStore = must(gtk.ListStoreNew(glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_INT, glib.TYPE_STRING))
	app.fileTreeStore = must(gtk.TreeStoreNew(glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_INT, glib.TYPE_STRING))
	app.fileModel = &app.fileStore.TreeModel
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
		window.SetShowMenubar(false)
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
	setAccessibility(&app.historyView.Widget, "Commit history", "Git commits ordered from newest to oldest; each row shows its author and commit time")
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
	app.historySelectionHandler = historySelection.Connect("changed", app.onHistorySelected)
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
	app.searchSpinner = must(gtk.SpinnerNew())
	app.searchIconSpacer = must(gdk.PixbufNew(gdk.COLORSPACE_RGB, true, 8, 16, 16))
	app.searchIconSpacer.Fill(0)
	app.searchSpinner.SetHAlign(gtk.ALIGN_START)
	app.searchSpinner.SetVAlign(gtk.ALIGN_CENTER)
	app.searchSpinner.SetMarginStart(8)
	app.searchSpinner.SetNoShowAll(true)
	app.searchSpinner.Hide()
	setAccessibility(&app.searchSpinner.Widget, "Searching history", "Search is still running")
	searchOverlay := must(gtk.OverlayNew())
	searchOverlay.Add(app.historySearch)
	searchOverlay.AddOverlay(app.searchSpinner)
	searchOverlay.SetOverlayPassThrough(app.searchSpinner, true)
	app.historySearch.SetTooltipText("Case-insensitive: exact phrases rank above separate word matches")
	app.historySearch.Connect("changed", func() {
		app.searchViewingResult = false
		app.searchBack.Hide()
		app.searchLimit = initialHistoryLimit
		app.searchLoadFooter.setBusy(false)
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
	searchSettingsContext, _ := app.searchSettings.GetStyleContext()
	searchSettingsContext.AddClass("giti-flat-button")
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
	app.searchLoadFooter = newLoadFooter("Load 100 more results", "Loading more results…", "Load more search results", "Load the next 100 file history search results")
	app.searchLoadButton = app.searchLoadFooter.button
	app.searchLoadButton.Connect("clicked", func() {
		app.searchLoadFooter.setBusy(true)
		app.searchLimit += 100
		app.updateGraphSearch()
	})
	searchPage.PackStart(app.searchLoadFooter.box, false, false, 0)
	app.historyStack.AddNamed(searchPage, "search")
	app.searchBack = must(gtk.ButtonNewFromIconName("go-previous-symbolic", gtk.ICON_SIZE_BUTTON))
	setAccessibility(&app.searchBack.Widget, "Back to search results", "Return from the selected commit to the current search results")
	app.searchBack.SetTooltipText("Back to search results")
	app.searchBack.SetRelief(gtk.RELIEF_NONE)
	searchBackContext, _ := app.searchBack.GetStyleContext()
	searchBackContext.AddClass("giti-flat-button")
	app.searchBack.Connect("clicked", func() {
		app.searchViewingResult = false
		app.historyStack.SetVisibleChildName("search")
		app.searchBack.Hide()
		app.loadFooter.setVisible(false)
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
		if button.Type() != gdk.EVENT_BUTTON_PRESS || button.Button() != gdk.BUTTON_PRIMARY && button.Button() != gdk.BUTTON_SECONDARY {
			return false
		}
		path, _, _, _, ok := app.fileView.GetPathAtPos(int(button.X()), int(button.Y()))
		if !ok && button.Button() == gdk.BUTTON_PRIMARY {
			return false
		}
		index := -1
		if ok {
			iter, err := app.fileModel.GetIter(path)
			if err != nil {
				return false
			}
			value, err := app.fileModel.GetValue(iter, fileIndexColumn)
			if err != nil {
				return false
			}
			rawIndex, err := value.GoValue()
			var valid bool
			index, valid = rawIndex.(int)
			if err != nil || !valid {
				return false
			}
		}
		if button.Button() == gdk.BUTTON_PRIMARY {
			if index >= 0 {
				return false
			}
			app.fileView.GrabFocus()
			app.toggleFileDirectory(path)
			return true
		}
		treeMode := app.fileTreeToggle.GetActive()
		fileActions := app.repository != nil && index >= 0 && index < len(app.files)
		if !treeMode && !fileActions {
			return false
		}
		menu := must(gtk.MenuNew())
		if fileActions {
			selection, _ := app.fileView.GetSelection()
			selection.SelectPath(path)
			for _, action := range fileCopyActions(app.repository.path, app.files[index].path) {
				action := action
				item := must(gtk.MenuItemNewWithLabel(action.label))
				item.Connect("activate", func() { app.copyToClipboard(action.text, action.message) })
				menu.Append(item)
			}
		}
		if treeMode {
			if fileActions {
				menu.Append(must(gtk.SeparatorMenuItemNew()))
			}
			collapseAll := must(gtk.MenuItemNewWithLabel("Collapse All Nodes"))
			collapseAll.Connect("activate", app.fileView.CollapseAll)
			menu.Append(collapseAll)
			expandAll := must(gtk.MenuItemNewWithLabel("Expand All Nodes"))
			expandAll.Connect("activate", app.fileView.ExpandAll)
			menu.Append(expandAll)
		}
		menu.Connect("selection-done", menu.Destroy)
		menu.ShowAll()
		menu.PopupAtPointer(event)
		return true
	})
	app.fileView.SetTooltipColumn(fileTooltipColumn)
	fileContext, _ := app.fileView.GetStyleContext()
	fileContext.AddClass("giti-list")
	fileRenderer := must(gtk.CellRendererTextNew())
	fileRenderer.SetProperty("family", "monospace")
	fileRenderer.SetProperty("ellipsize", pango.ELLIPSIZE_MIDDLE)
	fileColumn := must(gtk.TreeViewColumnNewWithAttribute("Files", fileRenderer, "markup", fileLabelColumn))
	fileColumn.SetExpand(true)
	app.fileView.AppendColumn(fileColumn)
	statRenderer := must(gtk.CellRendererTextNew())
	statRenderer.SetProperty("xalign", 1.0)
	statColumn := must(gtk.TreeViewColumnNewWithAttribute("Changes", statRenderer, "markup", 1))
	app.fileView.AppendColumn(statColumn)
	fileSelection, _ := app.fileView.GetSelection()
	fileSelection.SetSelectFunction(func(_ *gtk.TreeSelection, model *gtk.TreeModel, path *gtk.TreePath, _ bool) bool {
		iter, err := model.GetIter(path)
		if err != nil {
			return false
		}
		value, err := model.GetValue(iter, fileIndexColumn)
		if err != nil {
			return false
		}
		index, err := value.GoValue()
		fileIndex, valid := index.(int)
		return err == nil && valid && fileIndex >= 0
	})
	fileSelection.Connect("changed", app.onFileSelected)
	app.fileSearch = must(gtk.SearchEntryNew())
	app.fileSearch.SetPlaceholderText("Search changed files")
	app.fileSearch.SetTooltipText("Filter changed files by path, status, or conflict")
	setAccessibility(&app.fileSearch.Widget, "Search changed files", "Filter the files changed by the selected history entry")
	app.fileSearch.Connect("changed", func() { app.refreshFileView("") })
	app.fileSearch.Connect("stop-search", func() { app.fileSearchToggle.SetActive(false) })
	app.fileSearchToggle = must(gtk.ToggleButtonNew())
	app.fileSearchToggle.SetImage(must(gtk.ImageNewFromIconName("edit-find-symbolic", gtk.ICON_SIZE_BUTTON)))
	app.fileSearchToggle.SetRelief(gtk.RELIEF_NONE)
	app.fileSearchToggle.SetTooltipText("Filter changed files")
	fileSearchToggleContext, _ := app.fileSearchToggle.GetStyleContext()
	fileSearchToggleContext.AddClass("giti-flat-button")
	setAccessibility(&app.fileSearchToggle.Widget, "Filter changed files", "Show or hide the changed-file search field")
	app.fileSearchToggle.Connect("toggled", func() {
		visible := app.fileSearchToggle.GetActive()
		app.fileSearchReveal.SetRevealChild(visible)
		if visible {
			app.fileSearch.GrabFocus()
		} else {
			app.fileSearch.SetText("")
		}
	})
	app.fileListToggle = must(gtk.RadioButtonNew(nil))
	app.fileTreeToggle = must(gtk.RadioButtonNewFromWidget(app.fileListToggle))
	app.fileListToggle.SetMode(false)
	app.fileTreeToggle.SetMode(false)
	fileListToggleContext, _ := app.fileListToggle.GetStyleContext()
	fileListToggleContext.AddClass("giti-view-option")
	fileListToggleContext.AddClass("giti-view-list")
	fileTreeToggleContext, _ := app.fileTreeToggle.GetStyleContext()
	fileTreeToggleContext.AddClass("giti-view-option")
	fileTreeToggleContext.AddClass("giti-view-tree")
	app.fileListToggle.SetImage(must(gtk.ImageNewFromIconName("view-list-symbolic", gtk.ICON_SIZE_BUTTON)))
	fileTreeIconLoader := must(gdk.PixbufLoaderNewWithType("svg"))
	app.fileTreeToggle.SetImage(must(gtk.ImageNewFromPixbuf(must(fileTreeIconLoader.WriteAndReturnPixbuf(fileTreeIconSVG)))))
	app.fileListToggle.SetTooltipText("List view")
	app.fileTreeToggle.SetTooltipText("Group changed files into collapsible folders")
	setAccessibility(&app.fileListToggle.Widget, "List view", "Show changed files in the original flat list")
	setAccessibility(&app.fileTreeToggle.Widget, "Tree view", "Toggle between the unchanged file list and a folder tree")
	app.fileTreeToggle.Connect("toggled", func() { app.refreshFileView("") })

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
	app.whitespaceToggle.SetTooltipText("Include whitespace-only changes in file lists and diffs")
	app.whitespaceHandler = app.whitespaceToggle.Connect("toggled", app.onWhitespaceToggled)
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
	app.loadFooter = newLoadFooter("Load 100 more commits", "Loading older commits…", "Load more commits", "Load the next 100 older commits")
	app.loadButton = app.loadFooter.button
	app.loadButton.Connect("clicked", func() {
		app.loadFooter.setBusy(true)
		app.historyLimit += 100
		app.loadHistoryTo("", false, true)
	})

	// Compose the three principal regions only after their controls and signal
	// handlers are ready, avoiding visible partially initialized state.
	graphBox := must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 4))
	searchBox := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 0))
	searchBox.PackStart(app.searchBack, false, false, 0)
	searchBox.PackStart(searchOverlay, true, true, 0)
	searchBox.PackStart(app.searchSettings, false, false, 0)
	graphBox.PackStart(searchBox, false, false, 0)
	graphBox.PackStart(app.historyStack, true, true, 0)
	graphBox.PackStart(app.loadFooter.box, false, false, 0)
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
	fileHeader := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 4))
	fileHeader.SetMarginTop(2)
	fileHeader.SetMarginBottom(2)
	fileHeader.PackStart(app.fileSummary, true, true, 0)
	fileViewButtons := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 0))
	fileViewButtonsContext, _ := fileViewButtons.GetStyleContext()
	fileViewButtonsContext.AddClass("giti-view-switcher")
	fileViewButtons.PackStart(app.fileListToggle, false, false, 0)
	fileViewButtons.PackStart(app.fileTreeToggle, false, false, 0)
	fileHeader.PackEnd(fileViewButtons, false, false, 8)
	fileHeader.PackEnd(app.fileSearchToggle, false, false, 0)
	fileBox.PackStart(fileHeader, false, false, 0)
	app.fileSearchReveal = must(gtk.RevealerNew())
	app.fileSearchReveal.SetTransitionType(gtk.REVEALER_TRANSITION_TYPE_SLIDE_DOWN)
	app.fileSearchReveal.SetTransitionDuration(120)
	fileSearchBox := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 0))
	fileSearchBox.SetMarginStart(8)
	fileSearchBox.SetMarginEnd(8)
	fileSearchBox.SetMarginTop(6)
	fileSearchBox.SetMarginBottom(6)
	fileSearchBox.PackStart(app.fileSearch, true, true, 0)
	app.fileSearchReveal.Add(fileSearchBox)
	fileBox.PackStart(app.fileSearchReveal, false, false, 0)
	fileBox.PackStart(scroller(app.fileView), true, true, 0)
	app.repositoryPane.Pack2(fileBox, true, true)

	if application != nil {
		refresh := glib.SimpleActionNew("refresh", nil)
		refresh.Connect("activate", func() {
			app.branchRepositoryPath = ""
			app.populateBranchSelector(false)
			app.loadHistory(true)
		})
		application.AddAction(refresh)
		application.SetAccelsForAction("app.refresh", []string{"<Primary>r"})
		findDiff := glib.SimpleActionNew("find-diff", nil)
		findDiff.Connect("activate", func() { app.handleDiffFindKey(gdk.KEY_f, gdk.CONTROL_MASK) })
		application.AddAction(findDiff)
		application.SetAccelsForAction("app.find-diff", []string{"<Primary>f"})
		app.mainMenu = must(gtk.MenuButtonNew())
		app.mainMenu.SetImage(must(gtk.ImageNewFromIconName("open-menu-symbolic", gtk.ICON_SIZE_BUTTON)))
		app.mainMenu.SetRelief(gtk.RELIEF_NONE)
		app.mainMenu.SetTooltipText("Main Menu")
		setAccessibility(&app.mainMenu.Widget, "Main Menu", "Open application commands")
		mainMenuContext, _ := app.mainMenu.GetStyleContext()
		mainMenuContext.AddClass("giti-flat-button")
		popover := must(gtk.PopoverNew(app.mainMenu))
		menuBox := must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 0))
		for _, command := range []struct {
			label, shortcut string
			action          *glib.SimpleAction
		}{
			{"Find in Diff", "Ctrl+F", findDiff},
			{"Refresh", "Ctrl+R", refresh},
		} {
			row := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 24))
			label, shortcut := must(gtk.LabelNew(command.label)), must(gtk.LabelNew(command.shortcut))
			label.SetXAlign(0)
			shortcut.SetXAlign(1)
			shortcutContext, _ := shortcut.GetStyleContext()
			shortcutContext.AddClass("giti-secondary-label")
			row.PackStart(label, true, true, 0)
			row.PackEnd(shortcut, false, false, 0)
			button := must(gtk.ButtonNew())
			button.SetRelief(gtk.RELIEF_NONE)
			button.Add(row)
			buttonContext, _ := button.GetStyleContext()
			buttonContext.AddClass("giti-flat-button")
			setAccessibility(&button.Widget, command.label, command.label+" ("+command.shortcut+")")
			action := command.action
			button.Connect("clicked", func() { popover.Popdown(); action.Activate(nil) })
			menuBox.PackStart(button, false, true, 0)
			app.mainMenuShortcuts = append(app.mainMenuShortcuts, shortcut)
		}
		menuBox.SetMarginStart(6)
		menuBox.SetMarginEnd(6)
		menuBox.SetMarginTop(6)
		menuBox.SetMarginBottom(6)
		popover.Add(menuBox)
		menuBox.ShowAll()
		app.mainMenu.SetPopover(popover)
		app.windowHeader = must(gtk.HeaderBarNew())
		app.windowHeader.SetTitle(filepath.Base(app.repository.path))
		app.windowHeader.SetHasSubtitle(false)
		app.windowHeader.SetShowCloseButton(true)
		app.branchButton = must(gtk.MenuButtonNew())
		app.branchButton.SetRelief(gtk.RELIEF_NONE)
		branchButtonContext, _ := app.branchButton.GetStyleContext()
		branchButtonContext.AddClass("giti-flat-button")
		branchButtonBox := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 6))
		app.branchLabel = must(gtk.LabelNew(""))
		app.branchLabel.SetEllipsize(pango.ELLIPSIZE_END)
		app.branchLabel.SetMaxWidthChars(28)
		branchButtonBox.PackStart(app.branchLabel, true, true, 0)
		branchButtonBox.PackEnd(must(gtk.ImageNewFromIconName("pan-down-symbolic", gtk.ICON_SIZE_MENU)), false, false, 0)
		app.branchButton.Add(branchButtonBox)
		app.branchPopover = must(gtk.PopoverNew(app.branchButton))
		branchBox := must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 6))
		branchBox.SetMarginStart(8)
		branchBox.SetMarginEnd(8)
		branchBox.SetMarginTop(8)
		branchBox.SetMarginBottom(8)
		app.branchSearch = must(gtk.SearchEntryNew())
		app.branchSearch.SetPlaceholderText("Search branches")
		setAccessibility(&app.branchSearch.Widget, "Search branches", "Filter the branches available for history browsing")
		app.branchList = must(gtk.ListBoxNew())
		app.branchList.SetSelectionMode(gtk.SELECTION_SINGLE)
		app.branchList.SetActivateOnSingleClick(true)
		app.branchSearch.Connect("search-changed", app.renderBranchSelector)
		app.branchList.Connect("row-activated", func(_ *gtk.ListBox, row *gtk.ListBoxRow) {
			index := row.GetIndex()
			if index < 0 || index >= len(app.branchVisible) || app.branchRevisions[app.branchVisible[index]] == app.repository.revisionArg {
				app.branchPopover.Popdown()
				return
			}
			branch := app.branchRevisions[app.branchVisible[index]]
			app.branchPopover.Popdown()
			app.openRepository(app.repository.path, historySpec{Revision: branch, Path: app.repository.searchPath, Follow: app.repository.follow})
		})
		branchScroll := scroller(app.branchList)
		branchScroll.SetPolicy(gtk.POLICY_NEVER, gtk.POLICY_AUTOMATIC)
		branchScroll.SetSizeRequest(280, 240)
		app.branchLimitLabel = must(gtk.LabelNew(""))
		app.branchLimitLabel.SetNoShowAll(true)
		app.branchLimitLabel.SetXAlign(0)
		app.branchLimitLabel.SetLineWrap(true)
		limitContext, _ := app.branchLimitLabel.GetStyleContext()
		limitContext.AddClass("giti-secondary-label")
		setAccessibility(&app.branchLimitLabel.Widget, "Additional branch results", "Search to reveal branches outside the displayed results")
		branchBox.PackStart(app.branchSearch, false, false, 0)
		branchBox.PackStart(branchScroll, true, true, 0)
		branchBox.PackStart(app.branchLimitLabel, false, false, 0)
		app.branchPopover.Add(branchBox)
		app.branchPopover.Connect("show", func() {
			if app.branchRepositoryPath != app.repository.path {
				app.populateBranchSelector(true)
			}
			app.branchSearch.SetText("")
			app.branchSearch.GrabFocus()
		})
		app.branchButton.SetPopover(app.branchPopover)
		app.populateBranchSelector(false)
		branchBox.ShowAll()
		app.windowHeader.PackStart(app.branchButton)
		app.windowHeader.PackEnd(app.mainMenu)
		app.window.SetTitlebar(app.windowHeader)
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
	case modifiers&gdk.CONTROL_MASK != 0 && modifiers&gdk.SHIFT_MASK == 0 && (key == gdk.KEY_f || key == gdk.KEY_F):
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
	button.SetRelief(gtk.RELIEF_NONE)
	context, _ := button.GetStyleContext()
	context.AddClass("giti-flat-button")
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
	app.loadHistoryTo("", refreshSearch, false)
}

func (app *giti) loadHistoryTo(reveal string, refreshSearch, preserveView bool) {
	// Generation checks protect GTK from callbacks already queued on the main loop.
	// Pagination retains downstream work because existing rows remain unchanged.
	app.historyGeneration++
	if app.historyCancel != nil {
		app.historyCancel()
	}
	historyGeneration := app.historyGeneration
	preserveWork := preserveView && app.currentRow != nil && reveal == ""
	if !preserveWork {
		app.selectionGeneration++
		app.diffGeneration++
		if app.selectionCancel != nil {
			app.selectionCancel()
		}
		if app.diffCancel != nil {
			app.diffCancel()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	app.historyCancel = cancel
	repo, limit := app.repository, app.historyLimit
	ignoreWhitespace := !app.whitespaceToggle.GetActive()
	app.loadFooter.setBusy(true)
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
			restoreFocus := app.loadFooter.takeFocus()
			app.loadFooter.setBusy(false)
			if err != nil {
				app.historyCancel = nil
				app.showError(err)
				if restoreFocus {
					app.loadButton.GrabFocus()
				}
				return false
			}
			// Commit the result to GTK only after the stale-work guard above; all
			// model selection and follow-up measurement stays on the main thread.
			app.historyLimit, app.graphWidth, app.historyRows, app.historyHasMore = limit, graphWidth, rows, hasMore
			commitCount := 0
			for _, row := range rows {
				if row.kind == "commit" {
					commitCount++
				}
			}
			app.loadFooter.update(commitCount, "commit", hasMore && (app.historyStack.GetVisibleChildName() == "graph" || app.searchViewingResult))
			app.graphColumn.SetFixedWidth(graphWidth)
			selection, _ := app.historyView.GetSelection()
			historyScroll := app.historyScroller.GetVAdjustment().GetValue()
			preferredKind, preferredRevision := "", ""
			if app.currentRow != nil {
				preferredKind, preferredRevision = app.currentRow.kind, app.currentRow.revision
			}
			preserveSelection := preserveView && preferredKind != "" && reveal == ""
			if preserveSelection {
				selection.HandlerBlock(app.historySelectionHandler)
			}
			app.historyStore.Clear()
			target, preferredFound := -1, false
			for index, row := range rows {
				iter := app.historyStore.Append()
				app.historyStore.Set(iter, []int{0, 1, 2}, []any{graphs[index], historyLabel(row), row.kind})
				if target < 0 {
					target = index
				}
				if row.kind == preferredKind && (preferredRevision == "" || preferredRevision == row.revision) {
					target, preferredFound = index, true
				}
				if reveal != "" && row.revision == reveal {
					target = index
				}
			}
			if target >= 0 {
				path := must(gtk.TreePathNewFromIndicesv([]int{target}))
				selection.SelectPath(path)
				if reveal != "" {
					app.historyView.ScrollToCell(path, nil, true, 0, .5)
					app.historyView.GrabFocus()
				}
			}
			if preserveSelection {
				selection.HandlerUnblock(app.historySelectionHandler)
				if preferredFound {
					app.currentRow = &app.historyRows[target]
				} else if target >= 0 {
					app.onHistorySelected()
				}
			}
			if restoreFocus {
				if hasMore && app.loadFooter.box.GetVisible() {
					app.loadButton.GrabFocus()
				} else {
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
					} else if preserveSelection {
						adjustment := app.historyScroller.GetVAdjustment()
						adjustment.SetValue(min(historyScroll, max(adjustment.GetLower(), adjustment.GetUpper()-adjustment.GetPageSize())))
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
	app.setSearchBusy(false)
	app.historySearch.SetText("")
	app.searchBack.Hide()
	app.searchLoadFooter.setVisible(false)
	app.loadFooter.setVisible(false)
	app.historyStore.Clear()
	app.fileStore.Clear()
	app.fileTreeStore.Clear()
	app.fileSearch.SetText("")
	app.fileSearchToggle.SetActive(false)
	app.fileListToggle.SetActive(true)
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
	app.whitespaceToggle.HandlerBlock(app.whitespaceHandler)
	app.whitespaceToggle.SetActive(app.whitespacePreferred)
	app.whitespaceToggle.HandlerUnblock(app.whitespaceHandler)
	app.window.SetTitle(repo.windowTitle())
	if app.windowHeader != nil {
		app.windowHeader.SetTitle(filepath.Base(repo.path))
		if app.branchRepositoryPath != repo.path {
			app.branchRepositoryPath = ""
		}
		app.populateBranchSelector(false)
	}
	app.window.ShowAll()
	app.notificationGeneration++
	app.notification.Hide()
	app.window.Maximize()
	window, windowErr := app.window.GetWindow()
	if windowErr == nil && window.IsA(glib.TypeFromName("GdkX11Window")) {
		app.window.PresentWithTime(uint32(C.gdk_x11_get_server_time((*C.GdkWindow)(unsafe.Pointer(window.Native())))))
	} else {
		app.window.Present()
	}
	app.loadHistory(false)
	return false
}

func (app *giti) populateBranchSelector(reload bool) {
	if app.branchList == nil {
		return
	}
	current, currentMarkup := "detached at "+app.repository.revision[:min(7, len(app.repository.revision))], ""
	if branch, err := app.repository.run("symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		current = strings.TrimSpace(branch)
		currentMarkup = referenceBadge(current, "branch")
	} else {
		currentMarkup = referenceBadge("HEAD"+headRefSuffix, "branch") + ` <span foreground="#6b7280">` + current + `</span>`
	}
	if reload {
		app.branchRevisions = []string{"HEAD"}
		app.branchLabels = []string{"Current — " + current}
		app.branchMarkups = []string{"Current — " + currentMarkup}
		output, err := app.repository.run("for-each-ref", "--format=%(refname)%00%(symref)", "refs/heads/", "refs/remotes/")
		if err == nil {
			type branchOption struct {
				revision, label, markup string
				remote                  bool
			}
			var branches []branchOption
			for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
				fields := strings.SplitN(line, "\x00", 2)
				if fields[0] == "" || len(fields) > 1 && fields[1] != "" {
					continue
				}
				revision, label := fields[0], strings.TrimPrefix(fields[0], "refs/heads/")
				remote := strings.HasPrefix(revision, remoteRefPrefix)
				if remote {
					label = strings.TrimPrefix(revision, remoteRefPrefix)
				} else {
					revision = label
				}
				branches = append(branches, branchOption{revision, label, referenceBadge(revision, "branch"), remote})
			}
			sort.Slice(branches, func(left, right int) bool {
				if branches[left].remote != branches[right].remote {
					return !branches[left].remote
				}
				leftName, rightName := strings.ToLower(branches[left].label), strings.ToLower(branches[right].label)
				return leftName < rightName || leftName == rightName && branches[left].label < branches[right].label
			})
			for _, branch := range branches {
				app.branchRevisions = append(app.branchRevisions, branch.revision)
				app.branchLabels = append(app.branchLabels, branch.label)
				app.branchMarkups = append(app.branchMarkups, branch.markup)
			}
		}
		app.branchRepositoryPath = app.repository.path
		app.renderBranchSelector()
	} else {
		for row, index := range app.branchVisible {
			if app.branchRevisions[index] == app.repository.revisionArg {
				app.branchList.SelectRow(app.branchList.GetRowAtIndex(row))
				break
			}
		}
	}
	viewed := app.repository.revisionArg
	viewedMarkup := referenceBadge(viewed, "branch")
	if viewed == "HEAD" {
		viewed = "Current — " + current
		viewedMarkup = "Current — " + currentMarkup
	}
	app.branchLabel.SetMarkup(viewedMarkup)
	app.branchButton.SetTooltipText("Browse history from " + viewed)
	setAccessibility(&app.branchButton.Widget, "History branch: "+viewed, "Choose a branch to browse without changing the working tree")
}

func (app *giti) renderBranchSelector() {
	app.branchList.UnselectAll()
	if children := app.branchList.GetChildren(); children != nil {
		children.Foreach(func(child any) { app.branchList.Remove(child.(gtk.IWidget)) })
		children.Free()
	}
	text, _ := app.branchSearch.GetText()
	query, matches := strings.ToLower(strings.TrimSpace(text)), 0
	app.branchVisible = nil
	for index, name := range app.branchLabels {
		if query != "" && !strings.Contains(strings.ToLower(name), query) {
			continue
		}
		matches++
		if len(app.branchVisible) >= branchResultLimit {
			continue
		}
		app.branchVisible = append(app.branchVisible, index)
		label := must(gtk.LabelNew(""))
		label.SetMarkup(app.branchMarkups[index])
		label.SetXAlign(0)
		label.SetMarginStart(8)
		label.SetMarginEnd(8)
		label.SetMarginTop(6)
		label.SetMarginBottom(6)
		row := must(gtk.ListBoxRowNew())
		rowContext, _ := row.GetStyleContext()
		rowContext.AddClass("giti-selection-row")
		row.Add(label)
		app.branchList.Add(row)
		if app.branchRevisions[index] == app.repository.revisionArg {
			app.branchList.SelectRow(row)
		}
	}
	app.branchList.ShowAll()
	remaining := matches - len(app.branchVisible)
	if remaining == 0 {
		app.branchLimitLabel.Hide()
		return
	}
	message := fmt.Sprintf("… %d more branches. Search to show them.", remaining)
	if query != "" {
		message = fmt.Sprintf("… %d more matches. Refine your search to show them.", remaining)
	}
	app.branchLimitLabel.SetText(message)
	app.branchLimitLabel.Show()
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
