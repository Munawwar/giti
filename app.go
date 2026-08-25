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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

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
	fileStageIconColumn
	fileUndoIconColumn
)

//go:embed logo/giti-logo.png
var appIconPNG []byte

//go:embed logo/file-tree-symbolic.svg
var fileTreeIconSVG []byte

const appCSS = `
treeview.giti-list.view {
  -GtkTreeView-vertical-separator: 0;
  border-left-color: #c7cbd1;
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
button.giti-parent-button {
  background-color: transparent;
  background-image: none;
  border-color: transparent;
  box-shadow: none;
  min-height: 0;
  min-width: 0;
  padding: 1px 3px;
}
button.giti-parent-button > label {
  color: #4b5563;
  font-family: monospace;
  text-decoration-line: underline;
}
button.giti-parent-button:hover {
  background-color: alpha(#6b7280, 0.10);
}
button.giti-parent-button:hover > label {
  color: #303846;
}
button.giti-parent-button:focus {
  box-shadow: 0 0 0 2px #e95420;
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
	fileVisible                []int
	currentRow                 *historyRow
	currentFile                *changedFile
	historyTargetKind          string
	fileTargetPath             string
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
	workingDialog              *gtk.MessageDialog
	graphColumn                *gtk.TreeViewColumn
	fileColumn                 *gtk.TreeViewColumn
	fileStageColumn            *gtk.TreeViewColumn
	fileUndoColumn             *gtk.TreeViewColumn
	fileStageRenderer          *gtk.CellRendererPixbuf
	fileUndoRenderer           *gtk.CellRendererPixbuf
	normalizingFileCursor      bool
	fileExpandedSubtrees       map[string][]string
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
	headerParentButtons        []*gtk.Button
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
	diffContextLine            int
	compactLineNumbers         bool
	fullLineNumbers            bool
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
	app := &giti{repository: repo, resident: resident, application: application, busy: true, historyLimit: initialHistoryLimit, searchLimit: initialHistoryLimit, diffScroll: make(map[string]scrollPosition), fileExpandedSubtrees: make(map[string][]string), statePath: uiStatePath(), fullLineNumbers: true}
	if resident {
		app.server = newResidentServer(app)
		if err := app.server.start(); err != nil {
			return nil, err
		}
	}
	app.historyStore = must(gtk.ListStoreNew(gdk.PixbufGetType(), glib.TYPE_STRING, glib.TYPE_STRING))
	fileColumns := []glib.Type{glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_INT, glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING}
	app.fileStore = must(gtk.ListStoreNew(fileColumns...))
	app.fileTreeStore = must(gtk.TreeStoreNew(fileColumns...))
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
	app.compactLineNumbers = state.CompactLineNumbers
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

	historyPane := app.buildHistoryPane(state)
	filePane := app.buildFilePane()
	contentPane := app.buildContentPane()
	app.repositoryPane = must(gtk.PanedNew(gtk.ORIENTATION_VERTICAL))
	app.repositoryPane.SetWideHandle(true)
	app.repositoryPane.Pack1(historyPane, false, true)
	app.repositoryPane.Pack2(filePane, true, true)

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
	app.mainPane = must(gtk.PanedNew(gtk.ORIENTATION_HORIZONTAL))
	app.mainPane.SetWideHandle(true)
	app.mainPane.Pack1(app.repositoryPane, false, true)
	app.mainPane.Pack2(contentPane, true, true)
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
				app.persistPanePositions()
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

func (app *giti) persistPanePositions() {
	if !app.panesReady || app.mainPane.GetAllocatedWidth() <= 1 || app.repositoryPane.GetAllocatedHeight() <= 1 {
		return
	}
	main, repository := app.mainPane.GetPosition(), app.repositoryPane.GetPosition()
	_ = patchUIState(app.statePath, func(state *uiState) {
		state.MainPanePosition, state.RepositoryPanePosition = main, repository
	})
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
	app.historyRows, app.files, app.fileVisible, app.searchMatches, app.searchDepths = nil, nil, nil, nil, nil
	app.historyHasMore, app.searchLimit = false, initialHistoryLimit
	app.searchViewingResult = false
	app.diffScroll = make(map[string]scrollPosition)
	app.currentRow, app.currentFile, app.historyTargetKind, app.fileTargetPath, app.diffLoaded = nil, nil, "", "", false
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
	app.fileTreeToggle.SetActive(true)
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
