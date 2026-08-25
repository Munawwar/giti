package main

import (
	"context"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gotk3/gotk3/cairo"
	"github.com/gotk3/gotk3/gdk"
	"github.com/gotk3/gotk3/gtk"
	"github.com/gotk3/gotk3/pango"
)

func (app *giti) refreshFileView(preferredPath string) {
	if preferredPath == "" && app.currentFile != nil {
		preferredPath = app.currentFile.path
	}
	query, _ := app.fileSearch.GetText()
	terms := strings.Fields(strings.ToLower(query))
	treeMode := app.fileTreeToggle.GetActive()
	kind := ""
	if app.currentRow != nil {
		kind = app.currentRow.kind
	}
	app.fileStageColumn.SetVisible(kind == "unstaged" || kind == "staged" || kind == "conflict" || kind == "resolved")
	app.fileUndoColumn.SetVisible(kind == "unstaged")
	commonPrefix := ""
	if !treeMode && len(app.files) > 1 {
		parts := strings.Split(app.files[0].path, "/")
		commonParts := len(parts) - 1
		for _, file := range app.files[1:] {
			candidate := strings.Split(file.path, "/")
			commonParts = min(commonParts, len(candidate)-1)
			for index := 0; index < commonParts; index++ {
				if parts[index] != candidate[index] {
					commonParts = index
					break
				}
			}
		}
		if commonParts > 0 {
			commonPrefix = strings.Join(parts[:commonParts], "/") + "/"
		}
	}
	app.fileStore.Clear()
	app.fileTreeStore.Clear()
	if treeMode {
		app.fileModel = &app.fileTreeStore.TreeModel
		app.fileView.SetModel(app.fileTreeStore)
	} else {
		app.fileModel = &app.fileStore.TreeModel
		app.fileView.SetModel(app.fileStore)
	}
	app.fileView.SetEnableTreeLines(treeMode)

	var targetPath, firstPath *gtk.TreePath
	visibleFiles := make([]int, 0, len(app.files))
	directories, directoryFiles := make(map[string]*gtk.TreeIter), make(map[string]int)
	directoryChildren := make(map[string]map[string]bool)
	for fileIndex, file := range app.files {
		haystack := strings.ToLower(strings.Join([]string{file.status, file.path, file.oldPath, file.conflict}, " "))
		matches := true
		for _, term := range terms {
			if !strings.Contains(haystack, term) {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		visibleFiles = append(visibleFiles, fileIndex)
		if treeMode {
			parts, parent := strings.Split(file.path, "/"), ""
			for _, directory := range parts[:len(parts)-1] {
				current := directory
				if parent != "" {
					current = parent + "/" + directory
				}
				if directoryChildren[parent] == nil {
					directoryChildren[parent] = make(map[string]bool)
				}
				directoryChildren[parent][current] = true
				parent = current
			}
			directoryFiles[parent]++
		}
	}
	app.fileVisible = append(app.fileVisible[:0], visibleFiles...)

	mergeResolution := app.currentRow != nil && app.currentRow.kind == "commit" && len(app.currentRow.parents) > 1 && !app.fullMergeToggle.GetActive()
	for _, fileIndex := range visibleFiles {
		file := app.files[fileIndex]
		stageIcon, undoIcon, _ := app.workingFileAction(kind, file)
		stat := fmt.Sprintf("<span foreground=\"#2e8c47\">+%d</span>  <span foreground=\"#c7404d\">−%d</span>", file.additions, file.deletions)
		switch {
		case file.status == "??":
			stat = "<span foreground=\"#6b7280\">Untracked</span>"
		case file.conflict != "":
			color := "#c7404d"
			if file.status == "✓" {
				color = "#2e8c47"
			}
			stat = fmt.Sprintf("<span foreground=\"%s\">%s</span>", color, html.EscapeString(file.conflict))
		case mergeResolution:
			stat = "<span foreground=\"#6b7280\">Combined</span>"
		case file.binary:
			stat = "<span foreground=\"#6b7280\">Binary</span>"
		}
		sourcePath, tooltip := "", file.status+" "+file.path
		switch {
		case file.status == "??":
			tooltip = "Untracked " + file.path
		case file.status == "✓":
			tooltip = "Resolution applied to " + file.path
		case file.conflict != "":
			tooltip = file.conflict + ": " + file.path
		case strings.HasPrefix(file.status, "A"):
			tooltip = "Added " + file.path
		case strings.HasPrefix(file.status, "D"):
			tooltip = "Deleted " + file.path
		case strings.HasPrefix(file.status, "R"):
			sourcePath, tooltip = file.oldPath, "Renamed "+file.oldPath+" to "+file.path
			if similarity, err := strconv.Atoi(strings.TrimPrefix(file.status, "R")); err == nil {
				tooltip += fmt.Sprintf(" (%d%% similarity)", similarity)
			}
		case strings.HasPrefix(file.status, "C"):
			sourcePath, tooltip = file.oldPath, "Copied "+file.oldPath+" to "+file.path
		case strings.Contains(file.status, "M"):
			tooltip = "Modified " + file.path
		case strings.Contains(file.status, "U"):
			tooltip = "Unmerged " + file.path
		case strings.Contains(file.status, "T"):
			tooltip = "Type changed " + file.path
		}
		statusText, statusColor := file.status, "#6b7280"
		if strings.Contains(file.status, "D") {
			statusColor = "#b42318"
		} else if strings.Contains(file.status, "A") {
			statusColor = "#2e8c47"
		} else if strings.HasPrefix(file.status, "R") {
			statusText, statusColor = "R", "#24527a"
		}
		status := fmt.Sprintf("<span size=\"small\" foreground=\"%s\">%s</span>", statusColor, html.EscapeString(statusText))
		displayPath := html.EscapeString(file.path)
		if commonPrefix != "" && strings.HasPrefix(file.path, commonPrefix) {
			displayPath = `<span foreground="#9ca3af">…/</span>` + html.EscapeString(strings.TrimPrefix(file.path, commonPrefix))
		}
		if sourcePath != "" {
			displaySource := html.EscapeString(sourcePath)
			if commonPrefix != "" && strings.HasPrefix(sourcePath, commonPrefix) {
				displaySource = `<span foreground="#9ca3af">…/</span>` + html.EscapeString(strings.TrimPrefix(sourcePath, commonPrefix))
			}
			displayPath = displaySource + " → " + displayPath
		}
		label := status + " " + displayPath

		flatIter := app.fileStore.Append()
		app.fileStore.Set(flatIter, []int{fileLabelColumn, fileStatColumn, fileIndexColumn, fileTooltipColumn, fileStageIconColumn, fileUndoIconColumn}, []any{label, stat, fileIndex, tooltip, stageIcon, undoIcon})
		path, _ := app.fileStore.GetPath(flatIter)
		if treeMode {
			parts, parent, directoryPath := strings.Split(file.path, "/"), (*gtk.TreeIter)(nil), ""
			for directoryIndex := 0; directoryIndex < len(parts)-1; directoryIndex++ {
				if directoryPath != "" {
					directoryPath += "/"
				}
				directoryPath += parts[directoryIndex]
				labelParts := []string{parts[directoryIndex]}
				for directoryFiles[directoryPath] == 0 && len(directoryChildren[directoryPath]) == 1 {
					nextPath := ""
					for child := range directoryChildren[directoryPath] {
						nextPath = child
					}
					labelParts = append(labelParts, strings.TrimPrefix(nextPath, directoryPath+"/"))
					directoryPath = nextPath
					directoryIndex++
				}
				iter := directories[directoryPath]
				if iter == nil {
					iter = app.fileTreeStore.Append(parent)
					app.fileTreeStore.SetValue(iter, fileLabelColumn, html.EscapeString(strings.Join(labelParts, "/")))
					app.fileTreeStore.SetValue(iter, fileStatColumn, "")
					app.fileTreeStore.SetValue(iter, fileIndexColumn, -1)
					app.fileTreeStore.SetValue(iter, fileTooltipColumn, "Directory "+directoryPath)
					directories[directoryPath] = iter
				}
				parent = iter
			}
			name := parts[len(parts)-1]
			if file.oldPath != "" {
				oldParts := strings.Split(file.oldPath, "/")
				name = oldParts[len(oldParts)-1] + " → " + name
			}
			iter := app.fileTreeStore.Append(parent)
			app.fileTreeStore.SetValue(iter, fileLabelColumn, status+" "+html.EscapeString(name))
			app.fileTreeStore.SetValue(iter, fileStatColumn, stat)
			app.fileTreeStore.SetValue(iter, fileIndexColumn, fileIndex)
			app.fileTreeStore.SetValue(iter, fileTooltipColumn, tooltip)
			app.fileTreeStore.SetValue(iter, fileStageIconColumn, stageIcon)
			app.fileTreeStore.SetValue(iter, fileUndoIconColumn, undoIcon)
			path, _ = app.fileTreeStore.GetPath(iter)
		}
		if firstPath == nil {
			firstPath = path
		}
		if file.path == preferredPath {
			targetPath = path
		}
	}
	if targetPath == nil {
		targetPath = firstPath
	}
	if treeMode {
		app.fileView.ExpandAll()
	}
	if targetPath != nil {
		selection, _ := app.fileView.GetSelection()
		selection.SelectPath(targetPath)
		app.fileView.ScrollToCell(targetPath, nil, false, 0, 0)
	}
}

func (app *giti) workingFileAction(kind string, file changedFile) (stageIcon, undoIcon, action string) {
	switch kind {
	case "unstaged":
		return "list-add-symbolic", "edit-undo-symbolic", "stage"
	case "staged":
		return "list-remove-symbolic", "", "unstage"
	case "conflict":
		if app.repository.hasConflictMarkers(file.path) {
			return "dialog-warning-symbolic", "", "stage-warning"
		}
		return "list-add-symbolic", "", "stage"
	case "resolved":
		if file.staged {
			return "list-remove-symbolic", "", "unstage"
		}
		return "list-add-symbolic", "", "stage"
	}
	return "", "", ""
}

func (app *giti) changeWorkingFile(index int, undo bool) {
	if app.repository == nil || app.currentRow == nil || index < 0 || index >= len(app.files) || app.currentRow.kind == "commit" {
		return
	}
	file, kind, action := app.files[index], app.currentRow.kind, ""
	targetPath := ""
	for position, visibleIndex := range app.fileVisible {
		if visibleIndex == index && len(app.fileVisible) > 1 {
			target := min(position+1, len(app.fileVisible)-1)
			if target == position {
				target--
			}
			targetPath = app.files[app.fileVisible[target]].path
			break
		}
	}
	if undo {
		if kind != "unstaged" {
			return
		}
		action = "undo"
	} else {
		_, _, action = app.workingFileAction(kind, file)
	}
	if action == "" {
		return
	}
	if action == "undo" || action == "stage-warning" {
		primary, secondary, confirm := "Discard changes to this file?", file.path+" cannot be restored after this operation.", "Discard Changes"
		if file.status == "??" {
			primary, secondary, confirm = "Delete this untracked file?", file.path+" is not tracked by Git and cannot be restored.", "Delete File"
		} else if action == "stage-warning" {
			primary, secondary, confirm = "Stage this unresolved file anyway?", file.path+" still appears to contain conflict markers.", "Stage Anyway"
		}
		dialog := gtk.MessageDialogNew(app.window, gtk.DIALOG_MODAL|gtk.DIALOG_DESTROY_WITH_PARENT, gtk.MESSAGE_WARNING, gtk.BUTTONS_CANCEL, "%s", primary)
		dialog.FormatSecondaryText("%s", secondary)
		dialog.AddButton(confirm, gtk.RESPONSE_OK)
		app.workingDialog = dialog
		response := dialog.Run()
		app.workingDialog = nil
		dialog.Destroy()
		if response != gtk.RESPONSE_OK {
			return
		}
		if action == "stage-warning" {
			action = "stage"
		}
	}
	repo, path := app.repository, file.path
	paths := []string{"--", path}
	if file.oldPath != "" {
		paths = []string{"--", file.oldPath, path}
	}
	app.fileView.SetSensitive(false)
	go func() {
		var err error
		switch action {
		case "stage":
			_, err = repo.run(append([]string{"add"}, paths...)...)
		case "unstage":
			_, err = repo.run(append([]string{"reset", "-q"}, paths...)...)
		case "undo":
			if file.status == "??" {
				err = os.Remove(filepath.Join(repo.path, filepath.FromSlash(path)))
			} else {
				_, err = repo.run(append([]string{"restore", "--worktree"}, paths...)...)
			}
		}
		addMainSource(0, func() bool {
			app.fileView.SetSensitive(true)
			if repo != app.repository {
				return false
			}
			if err != nil {
				app.showError(err)
				return false
			}
			app.historyTargetKind, app.fileTargetPath = kind, targetPath
			app.loadHistory(false)
			return false
		})
	}()
}

func diffKey(row historyRow, file changedFile) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s", row.kind, row.revision, file.status, file.oldPath, file.path)
}

func (app *giti) toggleFileDirectory(path *gtk.TreePath) {
	if app.fileView.RowExpanded(path) {
		expanded := make([]string, 0)
		app.fileModel.ForEach(func(_ *gtk.TreeModel, candidate *gtk.TreePath, _ *gtk.TreeIter) bool {
			if path.IsAncestor(candidate) && app.fileView.RowExpanded(candidate) {
				expanded = append(expanded, candidate.String())
			}
			return false
		})
		app.fileExpandedSubtrees[path.String()] = expanded
		app.fileView.CollapseRow(path)
	} else {
		app.fileView.ExpandRow(path, false)
		for _, child := range app.fileExpandedSubtrees[path.String()] {
			app.fileView.ExpandRow(must(gtk.TreePathNewFromString(child)), false)
		}
	}
}

func (app *giti) normalizeFileCursor() {
	if app.normalizingFileCursor {
		return
	}
	app.fileView.QueueDraw()
	path, column := app.fileView.GetCursor()
	if path == nil || column == nil || column.GetTitle() == "Files" {
		return
	}
	iter, err := app.fileModel.GetIter(path)
	if err != nil {
		return
	}
	modelColumn := fileIndexColumn
	if column.GetTitle() == "Stage or Unstage" {
		modelColumn = fileStageIconColumn
	} else if column.GetTitle() == "Undo" {
		modelColumn = fileUndoIconColumn
	}
	if modelColumn == fileIndexColumn {
		app.normalizingFileCursor = true
		app.fileView.SetCursor(path, app.fileColumn, false)
		app.normalizingFileCursor = false
		return
	}
	value, err := app.fileModel.GetValue(iter, modelColumn)
	if err != nil {
		return
	}
	if text, textErr := value.GetString(); textErr != nil || text == "" {
		app.normalizingFileCursor = true
		app.fileView.SetCursor(path, app.fileColumn, false)
		app.normalizingFileCursor = false
	}
}

func (app *giti) onFileSelected() {
	app.showDiffPage()
	selection, _ := app.fileView.GetSelection()
	_, iter, ok := selection.GetSelected()
	if !ok || app.currentRow == nil {
		return
	}
	value, err := app.fileModel.GetValue(iter, fileIndexColumn)
	if err != nil {
		return
	}
	rawIndex, err := value.GoValue()
	index, valid := rawIndex.(int)
	if err != nil || !valid || index < 0 || index >= len(app.files) {
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
		automaticWhitespace := false
		if loadErr == nil && patch == "" && ignoreWhitespace && mergeResolution {
			patch, loadErr = repo.diffForViewContext(ctx, row, selectedFile, false, fullFile, true)
			automaticWhitespace = loadErr == nil && patch != ""
		}
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
				app.fullFileToggle.SetTooltipText("Show unchanged lines from the complete file")
			}
			app.fullFileToggle.HandlerUnblock(app.fullFileHandler)
			if automaticWhitespace {
				patch = "Whitespace-only merge resolution — shown automatically.\n\n" + patch
			} else if patch == "" && ignoreWhitespace {
				patch = "No non-whitespace changes. Turn on “Whitespace changes” to view this diff.\n"
			}
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

func (app *giti) buildFilePane() *gtk.Box {
	// Changed files keep paths and compact statistics in separate renderers so
	// long paths ellipsize without displacing the right-aligned counts.
	app.fileView = must(gtk.TreeViewNewWithModel(app.fileStore))
	setAccessibility(&app.fileView.Widget, "Changed files", "Files changed by the selected history entry; working-tree rows include Stage, Unstage, and Undo actions")
	app.fileView.SetHeadersVisible(false)
	app.fileView.AddEvents(int(gdk.BUTTON_PRESS_MASK))
	app.fileView.Connect("button-press-event", func(_ *gtk.TreeView, event *gdk.Event) bool {
		button := gdk.EventButtonNewFromEvent(event)
		if button.Type() != gdk.EVENT_BUTTON_PRESS || button.Button() != gdk.BUTTON_PRIMARY && button.Button() != gdk.BUTTON_SECONDARY {
			return false
		}
		path, column, _, _, ok := app.fileView.GetPathAtPos(int(button.X()), int(button.Y()))
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
			if index >= 0 && column != nil && (column.GetTitle() == "Stage or Unstage" || column.GetTitle() == "Undo") {
				selection, _ := app.fileView.GetSelection()
				selection.SelectPath(path)
				app.changeWorkingFile(index, column.GetTitle() == "Undo")
				return true
			}
			if index >= 0 {
				if column != nil && column.GetTitle() == "Changes" {
					app.fileView.SetCursor(path, app.fileColumn, false)
					return true
				}
				return false
			}
			app.fileView.GrabFocus()
			app.fileView.SetCursor(path, app.fileColumn, false)
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
	app.fileView.Connect("key-press-event", func(_ *gtk.TreeView, event *gdk.Event) bool {
		key := gdk.EventKeyNewFromEvent(event).KeyVal()
		if key == gdk.KEY_Left || key == gdk.KEY_Right {
			path, column := app.fileView.GetCursor()
			if path == nil || column == nil {
				return false
			}
			iter, err := app.fileModel.GetIter(path)
			if err != nil {
				return false
			}
			value, err := app.fileModel.GetValue(iter, fileIndexColumn)
			if err != nil {
				return false
			}
			index, valueErr := value.GoValue()
			fileIndex, valid := index.(int)
			if valueErr != nil || !valid || fileIndex < 0 {
				return false
			}
			if key == gdk.KEY_Right && column.GetTitle() != "Stage or Unstage" && column.GetTitle() != "Undo" && app.fileStageColumn.GetVisible() {
				app.fileView.SetCursorOnCell(path, app.fileStageColumn, &app.fileStageRenderer.CellRenderer, false)
			} else if key == gdk.KEY_Right && column.GetTitle() == "Stage or Unstage" && app.fileUndoColumn.GetVisible() {
				app.fileView.SetCursorOnCell(path, app.fileUndoColumn, &app.fileUndoRenderer.CellRenderer, false)
			} else if key == gdk.KEY_Left && column.GetTitle() == "Undo" {
				app.fileView.SetCursorOnCell(path, app.fileStageColumn, &app.fileStageRenderer.CellRenderer, false)
			} else if key == gdk.KEY_Left && column.GetTitle() != "Files" {
				app.fileView.SetCursor(path, app.fileColumn, false)
			}
			return true
		}
		if key != gdk.KEY_Return && key != gdk.KEY_KP_Enter && key != gdk.KEY_space {
			return false
		}
		path, column := app.fileView.GetCursor()
		if path == nil || column == nil {
			return false
		}
		iter, err := app.fileModel.GetIter(path)
		if err != nil {
			return false
		}
		value, err := app.fileModel.GetValue(iter, fileIndexColumn)
		if err != nil {
			return false
		}
		index, valueErr := value.GoValue()
		if valueErr != nil {
			return false
		}
		if index, ok := index.(int); ok && index < 0 && column.GetTitle() == "Files" {
			app.toggleFileDirectory(path)
			return true
		} else if ok && index >= 0 && (column.GetTitle() == "Stage or Unstage" || column.GetTitle() == "Undo") {
			app.changeWorkingFile(index, column.GetTitle() == "Undo")
			return true
		}
		return false
	})
	app.fileView.Connect("cursor-changed", app.normalizeFileCursor)
	app.fileView.Connect("key-release-event", func() bool { app.normalizeFileCursor(); return false })
	app.fileView.Connect("focus-in-event", func() bool { app.fileView.QueueDraw(); return false })
	app.fileView.Connect("focus-out-event", func() bool { app.fileView.QueueDraw(); return false })
	app.fileView.ConnectAfter("draw", func(_ *gtk.TreeView, context *cairo.Context) bool {
		path, column := app.fileView.GetCursor()
		if !app.fileView.HasFocus() || path == nil || column == nil {
			return false
		}
		iter, err := app.fileModel.GetIter(path)
		if err != nil {
			return false
		}
		value, err := app.fileModel.GetValue(iter, fileIndexColumn)
		if err != nil {
			return false
		}
		index, valueErr := value.GoValue()
		fileIndex, valid := index.(int)
		if valueErr != nil || !valid {
			return false
		}
		x, y, width, height := app.fileView.GetCellArea(path, column).GetRectangleInt()
		if fileIndex < 0 {
			x, width = 0, app.fileView.GetAllocatedWidth()
		} else if column.GetTitle() != "Stage or Unstage" && column.GetTitle() != "Undo" {
			return false
		}
		context.SetSourceRGB(0.914, 0.329, 0.125)
		context.SetLineWidth(2)
		context.Rectangle(float64(x+1), float64(y+1), float64(width-2), float64(height-2))
		context.Stroke()
		return false
	})
	app.fileView.SetProperty("has-tooltip", true)
	app.fileView.Connect("query-tooltip", func(_ *gtk.TreeView, x, y int, keyboard bool, tooltip *gtk.Tooltip) bool {
		path, column := app.fileView.GetCursor()
		if !keyboard {
			var ok bool
			path, column, _, _, ok = app.fileView.GetPathAtPos(x, y)
			if !ok {
				return false
			}
		}
		if path == nil || column == nil {
			return false
		}
		iter, err := app.fileModel.GetIter(path)
		if err != nil {
			return false
		}
		text := ""
		if column.GetTitle() == "Files" || column.GetTitle() == "Changes" {
			if value, valueErr := app.fileModel.GetValue(iter, fileTooltipColumn); valueErr == nil {
				text, _ = value.GetString()
			}
		} else if app.currentRow != nil {
			value, valueErr := app.fileModel.GetValue(iter, fileIndexColumn)
			if valueErr != nil {
				return false
			}
			if raw, rawErr := value.GoValue(); rawErr == nil {
				if index, ok := raw.(int); ok && index >= 0 && index < len(app.files) {
					stageIcon, undoIcon, action := app.workingFileAction(app.currentRow.kind, app.files[index])
					if column.GetTitle() == "Stage or Unstage" && stageIcon != "" {
						text = map[string]string{"stage": "Stage " + app.files[index].path, "unstage": "Unstage " + app.files[index].path, "stage-warning": "Conflict markers remain; stage " + app.files[index].path + " anyway"}[action]
					} else if column.GetTitle() == "Undo" && undoIcon != "" {
						text = "Undo changes to " + app.files[index].path
					}
				}
			}
		}
		if text == "" {
			return false
		}
		tooltip.SetText(text)
		app.fileView.SetTooltipCell(tooltip, path, column, nil)
		return true
	})
	fileContext, _ := app.fileView.GetStyleContext()
	fileContext.AddClass("giti-list")
	fileRenderer := must(gtk.CellRendererTextNew())
	fileRenderer.SetProperty("family", "monospace")
	fileRenderer.SetProperty("ellipsize", pango.ELLIPSIZE_MIDDLE)
	fileColumn := must(gtk.TreeViewColumnNewWithAttribute("Files", fileRenderer, "markup", fileLabelColumn))
	fileColumn.SetExpand(true)
	app.fileView.AppendColumn(fileColumn)
	app.fileColumn = fileColumn
	statRenderer := must(gtk.CellRendererTextNew())
	statRenderer.SetProperty("xalign", 1.0)
	statColumn := must(gtk.TreeViewColumnNewWithAttribute("Changes", statRenderer, "markup", 1))
	app.fileView.AppendColumn(statColumn)
	for _, action := range []struct {
		title string
		icon  int
	}{{"Stage or Unstage", fileStageIconColumn}, {"Undo", fileUndoIconColumn}} {
		renderer := must(gtk.CellRendererPixbufNew())
		renderer.SetProperty("xpad", 6)
		renderer.SetProperty("mode", gtk.CELL_RENDERER_MODE_ACTIVATABLE)
		column := must(gtk.TreeViewColumnNewWithAttribute(action.title, renderer, "icon-name", action.icon))
		column.SetMinWidth(32)
		app.fileView.AppendColumn(column)
		if action.icon == fileStageIconColumn {
			app.fileStageColumn, app.fileStageRenderer = column, renderer
		} else {
			app.fileUndoColumn, app.fileUndoRenderer = column, renderer
		}
	}
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
		return err == nil && valid && (fileIndex >= 0 || app.fileTreeToggle.GetActive())
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
	app.fileTreeToggle.SetActive(true)
	app.fileTreeToggle.Connect("toggled", func() { app.refreshFileView("") })

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
	return fileBox
}
