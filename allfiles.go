package main

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gotk3/gotk3/gdk"
	"github.com/gotk3/gotk3/glib"
	"github.com/gotk3/gotk3/gtk"
	"github.com/gotk3/gotk3/pango"
)

// GTK rows are the expensive part of a large repository, so one folder or search
// shows at most this many; Git lists even 200k paths in milliseconds.
const allFilesRowLimit = 5000

const (
	allFileLabelColumn = iota
	allFileStatColumn
	allFilePathColumn
	allFileFolderColumn
)

type fileNode struct {
	name, path string
	folder     bool
	changed    int
	children   []*fileNode
}

// buildFileTree indexes paths by folder off the GTK thread, folders first.
func buildFileTree(paths []string, changed map[string]*changedFile) *fileNode {
	root := &fileNode{folder: true}
	folders := map[string]*fileNode{"": root}
	split := func(path string) (parent, name string) {
		index := strings.LastIndexByte(path, '/')
		return path[:max(0, index)], path[index+1:]
	}
	var folder func(string) *fileNode
	folder = func(path string) *fileNode {
		if node := folders[path]; node != nil {
			return node
		}
		parent, name := split(path)
		node := &fileNode{name: name, path: path, folder: true}
		folders[path] = node
		owner := folder(parent)
		owner.children = append(owner.children, node)
		return node
	}
	for _, path := range paths {
		parent, name := split(path)
		owner := folder(parent)
		owner.children = append(owner.children, &fileNode{name: name, path: path})
		if changed[path] != nil {
			for directory := parent; ; directory, _ = split(directory) {
				folders[directory].changed++
				if directory == "" {
					break
				}
			}
		}
	}
	for _, node := range folders {
		sort.Slice(node.children, func(i, j int) bool {
			a, b := node.children[i], node.children[j]
			if a.folder != b.folder {
				return a.folder
			}
			return a.name < b.name
		})
	}
	return root
}

// allFilesContext lists what the row's snapshot contains: a commit's tree, the
// index, or the working tree including untracked files.
func (repo *repository) allFilesContext(ctx context.Context, row historyRow) ([]string, error) {
	args := []string{"ls-files", "-z", "--cached", "--others", "--exclude-standard"}
	switch row.kind {
	case "commit":
		args = []string{"ls-tree", "-r", "-z", "--name-only", row.revision}
	case "staged":
		args = []string{"ls-files", "-z", "--cached"}
	}
	output, err := repo.runContext(ctx, args...)
	if err != nil {
		return nil, err
	}
	seen, paths := make(map[string]bool), make([]string, 0)
	for _, path := range strings.Split(output, "\x00") {
		if path != "" && !seen[path] {
			seen[path], paths = true, append(paths, path)
		}
	}
	return paths, nil
}

func (repo *repository) fileContentContext(ctx context.Context, row historyRow, path string) (string, bool, error) {
	if row.kind == "commit" || row.kind == "staged" {
		revision := ":" + path
		if row.kind == "commit" {
			revision = row.revision + ":" + path
		}
		return repo.runLimitedContext(ctx, diffOutputLimit, true, "cat-file", "blob", revision)
	}
	fullPath := filepath.Join(repo.path, filepath.FromSlash(path))
	info, err := os.Lstat(fullPath)
	if err != nil {
		return "", false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(fullPath)
		return target, false, err
	}
	if !info.Mode().IsRegular() {
		return "", false, errors.New("not a regular file")
	}
	file, err := os.Open(fullPath)
	if err != nil {
		return "", false, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, diffOutputLimit+1))
	return string(content), len(content) > diffOutputLimit, err
}

func plainFileLines(content string, truncated bool, err error) []displayLine {
	switch {
	case err != nil:
		return []displayLine{{text: "Cannot show this file: " + strings.ToValidUTF8(err.Error(), "�") + "\n"}}
	case content == "":
		return []displayLine{{text: "Empty file\n"}}
	case strings.IndexByte(content[:min(len(content), 8000)], 0) >= 0:
		return []displayLine{{text: "Binary file — contents not shown\n"}}
	}
	lines := splitAfterLines(strings.ToValidUTF8(content, "�"))
	result := make([]displayLine, 0, len(lines)+1)
	for index, line := range lines {
		if !strings.HasSuffix(line, "\n") {
			line += "\n"
		}
		result = append(result, displayLine{text: line, new: index + 1})
	}
	if truncated {
		result = append(result, displayLine{text: fmt.Sprintf("\n[File truncated at %d MiB]\n", diffOutputLimit/1024/1024)})
	}
	return result
}

func (app *giti) allFilesMode() bool {
	return app.allFilesToggle != nil && app.allFilesToggle.GetActive()
}

func (app *giti) openFilePath() string {
	if app.currentFile != nil {
		return app.currentFile.path
	}
	return app.plainFile
}

// loadAllFiles replaces the tree with the current row's files, then opens target
// if it exists there, otherwise the first changed file.
func (app *giti) loadAllFiles(target string) {
	if app.allFilesCancel != nil {
		app.allFilesCancel()
		app.allFilesCancel = nil
	}
	app.allFilesRoot, app.allFilesPaths = nil, nil
	app.allFilesStore.Clear()
	if app.currentRow == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	app.allFilesCancel = cancel
	repo, row, generation := app.repository, *app.currentRow, app.selectionGeneration
	app.allFilesChanged = make(map[string]*changedFile, len(app.files))
	for index := range app.files {
		app.allFilesChanged[app.files[index].path] = &app.files[index]
	}
	changed := app.allFilesChanged
	go func() {
		paths, err := repo.allFilesContext(ctx, row)
		// Deleted files are in the commit's changes but no longer in its tree.
		listed := make(map[string]bool, len(paths))
		for _, path := range paths {
			listed[path] = true
		}
		for path := range changed {
			if !listed[path] {
				paths = append(paths, path)
			}
		}
		root := buildFileTree(paths, changed)
		addMainSource(0, func() bool {
			if ctx.Err() != nil || generation != app.selectionGeneration || repo != app.repository || !app.allFilesMode() {
				return false
			}
			app.allFilesCancel = nil
			if err != nil {
				app.showError(fmt.Errorf("list files: %w", err))
				return false
			}
			app.allFilesRoot, app.allFilesPaths = root, paths
			app.renderAllFiles()
			if !listed[target] && changed[target] == nil {
				target = ""
				for _, index := range app.fileVisible {
					target = app.files[index].path
					break
				}
			}
			if target != "" {
				app.openAllFile(target)
				app.revealAllFile(target)
			}
			return false
		})
	}()
}

// renderAllFiles shows top-level rows, filling folders as they expand; a search
// instead shows every match expanded.
func (app *giti) renderAllFiles() {
	if !app.allFilesMode() || app.allFilesRoot == nil {
		return
	}
	query, _ := app.fileSearch.GetText()
	terms := strings.Fields(strings.ToLower(query))
	app.allFilesStore.Clear()
	app.fileSummary.SetMarkup(fmt.Sprintf("<b>%d files</b> · %d changed", len(app.allFilesPaths), len(app.files)))
	if len(terms) == 0 {
		app.addFileRows(nil, app.allFilesRoot, false)
		return
	}
	matches, total := make([]string, 0), 0
	for _, path := range app.allFilesPaths {
		lower, matched := strings.ToLower(path), true
		for _, term := range terms {
			matched = matched && strings.Contains(lower, term)
		}
		if matched {
			if total++; total <= allFilesRowLimit {
				matches = append(matches, path)
			}
		}
	}
	app.addFileRows(nil, buildFileTree(matches, app.allFilesChanged), true)
	if total > allFilesRowLimit {
		app.addNoteRow(nil, fmt.Sprintf("… and %d more matches — refine your search", total-allFilesRowLimit))
	}
	app.allFilesView.ExpandAll()
}

func (app *giti) addNoteRow(parent *gtk.TreeIter, note string) {
	app.allFilesStore.SetValue(app.allFilesStore.Append(parent), allFileLabelColumn, `<span foreground="#6b7280">`+html.EscapeString(note)+`</span>`)
}

func (app *giti) addFileRows(parent *gtk.TreeIter, folder *fileNode, recursive bool) {
	mergeResolution := app.currentRow != nil && app.currentRow.kind == "commit" && len(app.currentRow.parents) > 1 && !app.fullMergeToggle.GetActive()
	for index, node := range folder.children {
		if index == allFilesRowLimit {
			app.addNoteRow(parent, fmt.Sprintf("… and %d more — use search to find them", len(folder.children)-index))
			break
		}
		name, stat := node.name, ""
		// Folders holding a single folder share a row, as in the changed-file tree.
		for node.folder && len(node.children) == 1 && node.children[0].folder {
			node = node.children[0]
			name += "/" + node.name
		}
		label := html.EscapeString(name)
		if node.folder {
			if node.changed > 0 {
				stat = fmt.Sprintf(`<span foreground="#6b7280">%d changed</span>`, node.changed)
			}
		} else if file := app.allFilesChanged[node.path]; file != nil {
			var status string
			status, stat = fileRowMarkup(*file, mergeResolution)
			label = status + " " + label
		} else {
			label = `<span size="small"> </span> ` + label // Same width as a status letter.
		}
		iter := app.allFilesStore.Append(parent)
		for column, value := range []any{label, stat, node.path, node.folder} {
			app.allFilesStore.SetValue(iter, column, value)
		}
		if node.folder {
			if recursive {
				app.addFileRows(iter, node, true)
			} else {
				app.allFilesStore.Append(iter) // Placeholder for the expander; filled on expansion.
			}
		}
	}
}

func (app *giti) allFileRow(iter *gtk.TreeIter) (path string, folder bool) {
	model := &app.allFilesStore.TreeModel
	path, _ = must(model.GetValue(iter, allFilePathColumn)).GetString()
	raw, _ := must(model.GetValue(iter, allFileFolderColumn)).GoValue()
	folder, _ = raw.(bool)
	return
}

func (app *giti) findFileNode(path string) *fileNode {
	node := app.allFilesRoot
	for _, name := range strings.Split(path, "/") {
		var next *fileNode
		for _, child := range node.children {
			if child.name == name {
				next = child
				break
			}
		}
		if next == nil {
			return nil
		}
		node = next
	}
	return node
}

// revealAllFile expands down to path, filling folders on the way, and selects it.
func (app *giti) revealAllFile(path string) {
	model, selection := &app.allFilesStore.TreeModel, must(app.allFilesView.GetSelection())
	var parent *gtk.TreeIter
	for {
		child, found := &gtk.TreeIter{}, false
		for ok := model.IterChildren(parent, child); ok; ok = model.IterNext(child) {
			candidate, folder := app.allFileRow(child)
			if !folder && candidate == path {
				selection.SelectIter(child)
				app.allFilesView.ScrollToCell(must(model.GetPath(child)), nil, false, 0, 0)
				return
			}
			if folder && strings.HasPrefix(path, candidate+"/") {
				app.allFilesView.ExpandRow(must(model.GetPath(child)), false)
				parent, found = child, true
				break
			}
		}
		if !found {
			selection.UnselectAll()
			return
		}
	}
}

// openAllFile sends a changed file through the regular diff flow by selecting it
// in the hidden changed-file view, which also keeps review and › navigation there.
func (app *giti) openAllFile(path string) {
	if path == "" || path == app.openFilePath() {
		return
	}
	target := ""
	app.fileModel.ForEach(func(model *gtk.TreeModel, row *gtk.TreePath, iter *gtk.TreeIter) bool {
		raw, _ := must(model.GetValue(iter, fileIndexColumn)).GoValue()
		if index, ok := raw.(int); ok && index >= 0 && index < len(app.files) && app.files[index].path == path {
			target = row.String() // ForEach frees its paths when the callback returns.
			return true
		}
		return false
	})
	selection := must(app.fileView.GetSelection())
	if target != "" {
		selection.SelectPath(must(gtk.TreePathNewFromString(target)))
		return
	}
	selection.UnselectAll()
	app.showPlainFile(path)
}

// showPlainFile opens a file the row did not change. It has no currentFile, so
// review marks, comments, and progress never apply to it.
func (app *giti) showPlainFile(path string) {
	app.diffGeneration++
	if app.diffCancel != nil {
		app.diffCancel()
	}
	generation, selectionGeneration := app.diffGeneration, app.selectionGeneration
	app.rememberDiffScroll()
	app.currentFile, app.plainFile, app.diffLoaded = nil, path, false
	app.showDiffPage()
	app.clearDiff()
	app.resetDiffOverview()
	ctx, cancel := context.WithCancel(context.Background())
	app.diffCancel = cancel
	repo, row := app.repository, *app.currentRow
	go func() {
		content, truncated, err := repo.fileContentContext(ctx, row, path)
		lines := plainFileLines(content, truncated, err)
		addMainSource(0, func() bool {
			if ctx.Err() != nil || generation != app.diffGeneration || selectionGeneration != app.selectionGeneration || repo != app.repository {
				return false
			}
			app.diffCancel = nil
			app.diffLoaded = true
			app.diffScroller.GetHAdjustment().SetValue(0)
			app.diffScroller.GetVAdjustment().SetValue(0)
			app.setDiffLines(lines)
			return false
		})
	}()
}

func (app *giti) buildAllFilesView() *gtk.TreeView {
	app.allFilesStore = must(gtk.TreeStoreNew(glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_STRING, glib.TYPE_BOOLEAN))
	app.allFilesView = must(gtk.TreeViewNewWithModel(app.allFilesStore))
	setAccessibility(&app.allFilesView.Widget, "All files", "Every file in the selected history entry; changed files show their status")
	app.allFilesView.SetHeadersVisible(false)
	app.allFilesView.SetEnableTreeLines(true)
	app.allFilesView.SetActivateOnSingleClick(true)
	context, _ := app.allFilesView.GetStyleContext()
	context.AddClass("giti-list")
	labelRenderer := must(gtk.CellRendererTextNew())
	labelRenderer.SetProperty("family", "monospace")
	labelRenderer.SetProperty("ellipsize", pango.ELLIPSIZE_MIDDLE)
	labelColumn := must(gtk.TreeViewColumnNewWithAttribute("Files", labelRenderer, "markup", allFileLabelColumn))
	labelColumn.SetExpand(true)
	app.allFilesView.AppendColumn(labelColumn)
	statRenderer := must(gtk.CellRendererTextNew())
	statRenderer.SetProperty("xalign", 1.0)
	app.allFilesView.AppendColumn(must(gtk.TreeViewColumnNewWithAttribute("Changes", statRenderer, "markup", allFileStatColumn)))
	app.allFilesView.Connect("row-expanded", func(_ *gtk.TreeView, iter *gtk.TreeIter, _ *gtk.TreePath) {
		model, first := &app.allFilesStore.TreeModel, &gtk.TreeIter{}
		path, _ := app.allFileRow(iter)
		if !model.IterChildren(iter, first) {
			return
		}
		if placeholder, _ := app.allFileRow(first); placeholder != "" || app.allFilesRoot == nil {
			return
		}
		if node := app.findFileNode(path); node != nil {
			app.addFileRows(iter, node, false)
		}
		app.allFilesStore.Remove(first)
	})
	app.allFilesView.Connect("row-activated", func(_ *gtk.TreeView, path *gtk.TreePath) {
		iter, err := app.allFilesStore.TreeModel.GetIter(path)
		if err != nil {
			return
		}
		if _, folder := app.allFileRow(iter); !folder {
			return
		}
		if app.allFilesView.RowExpanded(path) {
			app.allFilesView.CollapseRow(path)
		} else {
			app.allFilesView.ExpandRow(path, false)
		}
	})
	must(app.allFilesView.GetSelection()).Connect("changed", func(selection *gtk.TreeSelection) {
		// Clearing the store also lands here; only a loaded tree opens files.
		if _, iter, ok := selection.GetSelected(); ok && app.allFilesRoot != nil {
			if path, folder := app.allFileRow(iter); !folder {
				app.openAllFile(path)
			}
		}
	})
	app.allFilesView.Connect("button-press-event", func(_ *gtk.TreeView, event *gdk.Event) bool {
		button := gdk.EventButtonNewFromEvent(event)
		if button.Type() != gdk.EVENT_BUTTON_PRESS || button.Button() != gdk.BUTTON_SECONDARY {
			return false
		}
		row, _, _, _, ok := app.allFilesView.GetPathAtPos(int(button.X()), int(button.Y()))
		if !ok || app.repository == nil {
			return false
		}
		iter, err := app.allFilesStore.TreeModel.GetIter(row)
		if err != nil {
			return false
		}
		path, folder := app.allFileRow(iter)
		if path == "" || folder {
			return false
		}
		menu := must(gtk.MenuNew())
		app.addFileCopyItems(menu, path)
		menu.Connect("selection-done", menu.Destroy)
		menu.ShowAll()
		menu.PopupAtPointer(event)
		return true
	})
	return app.allFilesView
}
