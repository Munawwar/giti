package main

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gotk3/gotk3/gdk"
	"github.com/gotk3/gotk3/glib"
	"github.com/gotk3/gotk3/gtk"
)

func TestGTKApplicationMenu(t *testing.T) {
	if os.Getenv("GITI_GTK_TEST") == "" {
		t.Skip("set GITI_GTK_TEST=1 to run the display integration test")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := testRepository(t)
	repo, err := newRepository(path, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	gtk.Init(nil)
	application, err := gtk.ApplicationNew(applicationID+".menu-test", glib.APPLICATION_NON_UNIQUE)
	if err != nil {
		t.Fatal(err)
	}
	var app *giti
	var appErr error
	application.Connect("activate", func() {
		app, appErr = newGiti(repo, false, application)
		addMainSource(0, func() bool {
			application.Quit()
			return false
		})
	})
	application.Run([]string{"giti-menu-test"})
	if appErr != nil {
		t.Fatal(appErr)
	}
	defer func() {
		app.clearRepositoryView()
		app.window.Destroy()
	}()
	if application.GetAppMenu() == nil || application.GetMenubar() == nil || application.GetMenubar().GetNItems() != 1 {
		t.Fatal("refresh menu was not installed")
	}
}

func TestGTKParentNavigationLoadsAndRevealsOlderCommit(t *testing.T) {
	if os.Getenv("GITI_GTK_TEST") == "" {
		t.Skip("set GITI_GTK_TEST=1 to run the display integration test")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := saveUIState(uiStatePath(), uiState{MainPanePosition: 410, RepositoryPanePosition: 300, SearchCommitMessages: true, SearchReferences: true}); err != nil {
		t.Fatal(err)
	}
	repo, err := newRepository(testRepository(t), "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	gtk.Init(nil)
	app, err := newGiti(repo, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		app.clearRepositoryView()
		app.window.Destroy()
	}()
	app.historyLimit = 1
	if err = app.loadHistory(); err != nil {
		t.Fatal(err)
	}
	target, err := repo.run("rev-parse", "HEAD~11")
	if err != nil {
		t.Fatal(err)
	}
	target = strings.TrimSpace(target)
	found, err := app.revealHistoryRevision(target)
	deadline := time.Now().Add(3 * time.Second)
	for (app.currentRow == nil || app.currentRow.revision != target || app.historyScroller.GetVAdjustment().GetValue() == 0) && time.Now().Before(deadline) {
		for gtk.EventsPending() {
			gtk.MainIteration()
		}
		time.Sleep(10 * time.Millisecond)
	}
	missing, missingErr := app.revealHistoryRevision(strings.Repeat("f", 40))
	mainPosition, repositoryPosition := app.mainPane.GetPosition(), app.repositoryPane.GetPosition()
	if err != nil || !found || app.historyLimit != 101 || app.currentRow == nil || app.currentRow.revision != target || app.historyScroller.GetVAdjustment().GetValue() == 0 || missing || missingErr != nil || mainPosition != 410 || repositoryPosition != 300 || !app.searchMessages.GetActive() || !app.searchReferences.GetActive() {
		t.Fatalf("older commit was not loaded and revealed: found=%v err=%v limit=%d row=%#v scroll=%v missing=%v/%v panes=%d/%d", found, err, app.historyLimit, app.currentRow, app.historyScroller.GetVAdjustment().GetValue(), missing, missingErr, mainPosition, repositoryPosition)
	}
}

func TestGTKSelectionAndMemoryLifecycle(t *testing.T) {
	if os.Getenv("GITI_GTK_TEST") == "" {
		t.Skip("set GITI_GTK_TEST=1 to run the display integration test")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := testRepository(t)
	os.WriteFile(filepath.Join(path, "first.txt"), []byte("one"+strings.Repeat("x", 400)+"\n"+strings.Repeat("more\n", 200)), 0o644)
	os.WriteFile(filepath.Join(path, "second.txt"), []byte("second\n"), 0o644)
	if output, amendErr := exec.Command("git", "-C", path, "commit", "--amend", "-m", "commit 11", "-m", "Explain the orbital cache architecture.").CombinedOutput(); amendErr != nil {
		t.Fatalf("amend commit message: %v: %s", amendErr, output)
	}
	if output, tagErr := exec.Command("git", "-C", path, "tag", strings.Repeat("long-release-name-", 12)).CombinedOutput(); tagErr != nil {
		t.Fatalf("create long tag: %v: %s", tagErr, output)
	}
	repo, err := newRepository(path, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	gtk.Init(nil)
	nestedRuns := 0
	var nestedSource func() bool
	nestedSource = func() bool {
		nestedRuns++
		if nestedRuns < 1000 {
			addMainSource(0, nestedSource)
		}
		return false
	}
	addMainSource(0, nestedSource)
	deadline := time.Now().Add(2 * time.Second)
	for nestedRuns < 1000 && time.Now().Before(deadline) {
		for gtk.EventsPending() {
			gtk.MainIteration()
		}
	}
	if nestedRuns != 1000 {
		t.Fatalf("nested main-context scheduling stopped after %d runs", nestedRuns)
	}
	app, err := newGiti(repo, false)
	if err != nil {
		t.Fatal(err)
	}
	icon, iconErr := app.window.GetIcon()
	if iconErr != nil || icon == nil || icon.GetWidth() != 256 || icon.GetHeight() != 256 {
		t.Fatalf("window icon is not the embedded 256px logo: icon=%v err=%v", icon, iconErr)
	}
	splitDeadline := time.Now().Add(2 * time.Second)
	for app.repositoryPane.GetAllocatedHeight() <= 1 && time.Now().Before(splitDeadline) {
		for gtk.EventsPending() {
			gtk.MainIteration()
		}
		time.Sleep(10 * time.Millisecond)
	}
	splitHeight, splitPosition := app.repositoryPane.GetAllocatedHeight(), app.repositoryPane.GetPosition()
	messageOption, _ := app.searchMessages.GetLabel()
	referenceOption, _ := app.searchReferences.GetLabel()
	if app.historyLimit != initialHistoryLimit || !app.mainPane.GetWideHandle() || !app.repositoryPane.GetWideHandle() || app.loadButton.GetVisible() || app.fileView.GetTooltipColumn() != 0 || splitHeight <= 1 || splitPosition*2 < splitHeight-2 || splitPosition*2 > splitHeight+2 || app.searchSettings.GetPopover() == nil || !app.searchMessages.GetVisible() || !app.searchReferences.GetVisible() || app.searchMessages.GetActive() || app.searchReferences.GetActive() {
		t.Fatalf("bad initial graph layout: limit=%d dividers=%v/%v load-more=%v tooltip=%d split=%d/%d", app.historyLimit, app.mainPane.GetWideHandle(), app.repositoryPane.GetWideHandle(), app.loadButton.GetVisible(), app.fileView.GetTooltipColumn(), splitPosition, splitHeight)
	}
	if messageOption != "Also match commit description" || referenceOption != "Also match branches and tags" {
		t.Fatalf("unexpected search options %q / %q", messageOption, referenceOption)
	}
	app.mainPane.SetPosition(app.mainPane.GetAllocatedWidth() / 3)
	app.repositoryPane.SetPosition(splitHeight * 2 / 3)
	app.persistUIState()
	savedState := loadUIState(app.statePath)
	if savedState.MainPanePosition != app.mainPane.GetPosition() || savedState.RepositoryPanePosition != app.repositoryPane.GetPosition() {
		t.Fatalf("pane positions were not persisted: %#v", savedState)
	}
	details := commitDetails{sha: strings.Repeat("a", 40), subject: "subject"}
	app.setCommitHeader(details)
	children := app.commitHeader.GetChildren()
	compactHeader := children.Length()
	children.Free()
	details.body = "A longer description\n\nwith multiple lines."
	app.setCommitHeader(details)
	children = app.commitHeader.GetChildren()
	expandedHeader := children.Length()
	children.Free()
	if expandedHeader != compactHeader+1 {
		t.Fatalf("commit description expander missing: compact=%d body=%d", compactHeader, expandedHeader)
	}
	defer func() {
		app.clearRepositoryView()
		app.window.Destroy()
	}()
	loaded := len(app.historyRows)
	for range 3 {
		app.loadHistory()
	}
	if len(app.historyRows) != loaded || app.historyStore.IterNChildren(nil) != loaded {
		t.Fatalf("reloading duplicated history: rows=%d model=%d want=%d", len(app.historyRows), app.historyStore.IterNChildren(nil), loaded)
	}
	iter, ok := app.historyStore.GetIterFirst()
	if !ok {
		t.Fatal("rendered history is empty")
	}
	value, err := app.historyStore.GetValue(iter, 0)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := value.GoValue()
	pixbuf, pixbufOK := rendered.(*gdk.Pixbuf)
	if err != nil || !pixbufOK || pixbuf.GetWidth() < 48 || pixbuf.GetHeight() != graphRowHeight {
		t.Fatalf("history graph is not a rendered pixbuf: value=%T err=%v", rendered, err)
	}
	graphScroll := app.historyScroller.GetHAdjustment()
	scrollDeadline := time.Now().Add(2 * time.Second)
	for graphScroll.GetUpper() <= graphScroll.GetPageSize() && time.Now().Before(scrollDeadline) {
		for gtk.EventsPending() {
			gtk.MainIteration()
		}
		time.Sleep(10 * time.Millisecond)
	}
	graphScroll.SetValue(graphScroll.GetUpper() - graphScroll.GetPageSize())
	if app.graphColumn.GetFixedWidth() != app.graphWidth || graphScroll.GetUpper() <= graphScroll.GetPageSize() || graphScroll.GetValue() == 0 {
		t.Fatalf("graph is not horizontally scrollable: column=%d/%d range=%v/%v value=%v", app.graphColumn.GetFixedWidth(), app.graphWidth, graphScroll.GetUpper(), graphScroll.GetPageSize(), graphScroll.GetValue())
	}
	graphScroll.SetValue(0)
	app.showReferences([]string{"main", "origin/main", "feature"}, []string{"v1", "v2", "v3"})
	if app.diffStack.GetVisibleChildName() != "references" || app.referencesPage.GetChildren() == nil {
		t.Fatalf("references page did not replace diff view")
	}
	app.showDiffPage()
	if app.diffStack.GetVisibleChildName() != "diff" {
		t.Fatalf("diff page did not restore after references page")
	}
	for index, row := range app.historyRows {
		if row.refs == "" {
			continue
		}
		iter, err = app.historyStore.GetIter(must(gtk.TreePathNewFromIndicesv([]int{index})))
		if err == nil {
			value, err = app.historyStore.GetValue(iter, 0)
		}
		if err == nil {
			rendered, err = value.GoValue()
			pixbuf, pixbufOK = rendered.(*gdk.Pixbuf)
		}
		cellHeight := app.historyView.GetCellArea(must(gtk.TreePathNewFromIndicesv([]int{index})), app.graphColumn).GetHeight()
		if err != nil || !pixbufOK || pixbuf.GetHeight() != cellHeight {
			height := 0
			if pixbufOK {
				height = pixbuf.GetHeight()
			}
			t.Fatalf("ref row graph does not fill its row: value=%T height=%d cell=%d err=%v", rendered, height, cellHeight, err)
		}
		break
	}
	theme, themeErr := must(gtk.SettingsGetDefault()).GetProperty("gtk-theme-name")
	themeName, isThemeName := theme.(string)
	if themeErr != nil || !isThemeName || strings.HasSuffix(themeName, "-dark") {
		t.Fatalf("Giti did not force a light GTK theme: theme=%q err=%v", theme, themeErr)
	}
	deadline = time.Now().Add(2 * time.Second)
	for (!app.window.IsMaximized() || app.currentFile == nil || app.diffBuffer.GetCharCount() == 0) && time.Now().Before(deadline) {
		for gtk.EventsPending() {
			gtk.MainIteration()
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !app.window.IsMaximized() || app.currentRow == nil || app.currentRow.kind != "unstaged" {
		t.Fatalf("bad initial selection: maximized=%v row=%#v", app.window.IsMaximized(), app.currentRow)
	}
	if app.currentFile == nil || app.currentFile.path != "first.txt" || len(app.files) != 2 {
		t.Fatalf("bad initial file selection: %#v from %#v", app.currentFile, app.files)
	}
	start, end := app.diffBuffer.GetBounds()
	compact, _ := app.diffBuffer.GetText(start, end, true)
	if !strings.Contains(compact, "+one") || strings.Contains(compact, "second") || strings.Contains(compact, "diff --git") {
		t.Fatalf("single-file rendered diff is wrong: %q", compact)
	}
	app.diffBuffer.SelectRange(start, end)
	clipboard := must(gtk.ClipboardGet(gdk.SELECTION_CLIPBOARD))
	app.diffBuffer.CopyClipboard(clipboard)
	copied, err := clipboard.WaitForText()
	if err != nil || copied != compact {
		t.Fatalf("diff selection was not copyable: %q: %v", copied, err)
	}
	app.fullFileToggle.SetActive(true)
	if !app.fullFileToggle.GetActive() {
		t.Fatal("full-file mode did not activate")
	}
	app.fileView.GrabFocus()
	app.fileView.SetCursor(must(gtk.TreePathNewFromIndicesv([]int{1})), nil, false)
	deadline = time.Now().Add(2 * time.Second)
	for (app.currentFile == nil || app.currentFile.path != "second.txt" || app.diffBuffer.GetCharCount() == 0) && time.Now().Before(deadline) {
		for gtk.EventsPending() {
			gtk.MainIteration()
		}
		time.Sleep(10 * time.Millisecond)
	}
	start, end = app.diffBuffer.GetBounds()
	second, _ := app.diffBuffer.GetText(start, end, true)
	if !app.fileView.IsFocus() || app.fullFileToggle.GetActive() || app.currentFile.path != "second.txt" || !strings.Contains(second, "+second") || strings.Contains(second, "+one") {
		t.Fatalf("file switch retained state or content: full=%v file=%#v diff=%q", app.fullFileToggle.GetActive(), app.currentFile, second)
	}
	app.fileView.SetCursor(must(gtk.TreePathNewFromIndicesv([]int{0})), nil, false)
	deadline = time.Now().Add(2 * time.Second)
	for (app.currentFile == nil || app.currentFile.path != "first.txt" || app.diffBuffer.GetCharCount() == 0) && time.Now().Before(deadline) {
		for gtk.EventsPending() {
			gtk.MainIteration()
		}
		time.Sleep(10 * time.Millisecond)
	}
	vertical, horizontal := app.diffScroller.GetVAdjustment(), app.diffScroller.GetHAdjustment()
	vertical.SetValue(180)
	horizontal.SetValue(120)
	wanted := scrollPosition{horizontal.GetValue(), vertical.GetValue()}
	if wanted.horizontal == 0 || wanted.vertical == 0 {
		t.Fatalf("fixture did not create a two-axis scrollable diff: %#v", wanted)
	}
	switchStarted := time.Now()
	for _, index := range []int{1, 0, 1, 0, 1, 0} {
		app.fileView.SetCursor(must(gtk.TreePathNewFromIndicesv([]int{index})), nil, false)
	}
	if elapsed := time.Since(switchStarted); elapsed > 100*time.Millisecond {
		t.Fatalf("file selection blocked GTK for %v", elapsed)
	}
	deadline = time.Now().Add(2 * time.Second)
	for (app.currentFile == nil || app.currentFile.path != "first.txt" || horizontal.GetValue() < wanted.horizontal-1 || vertical.GetValue() < wanted.vertical-1) && time.Now().Before(deadline) {
		for gtk.EventsPending() {
			gtk.MainIteration()
		}
		time.Sleep(10 * time.Millisecond)
	}
	restored := scrollPosition{horizontal.GetValue(), vertical.GetValue()}
	if app.currentFile == nil || app.currentFile.path != "first.txt" || restored.horizontal < wanted.horizontal-1 || restored.horizontal > wanted.horizontal+1 || restored.vertical < wanted.vertical-1 || restored.vertical > wanted.vertical+1 {
		t.Fatalf("file scroll was not restored: got=%#v want=%#v", restored, wanted)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	wrapper := "#!/bin/sh\ncase \" $* \" in *\" diff --name-status \"*) sleep .3;; esac\nexec " + realGit + " \"$@\"\n"
	if err = os.WriteFile(filepath.Join(binDir, "git"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	app.historyView.GrabFocus()
	selectionStarted := time.Now()
	for _, index := range []int{1, 2, 3} {
		app.historyView.SetCursor(must(gtk.TreePathNewFromIndicesv([]int{index})), nil, false)
	}
	if elapsed := time.Since(selectionStarted); elapsed > 100*time.Millisecond {
		t.Fatalf("graph selection blocked GTK for %v", elapsed)
	}
	deadline = time.Now().Add(2 * time.Second)
	for (app.currentFile == nil || app.diffBuffer.GetCharCount() == 0) && time.Now().Before(deadline) {
		for gtk.EventsPending() {
			gtk.MainIteration()
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !app.historyView.IsFocus() || app.currentRow == nil || app.currentRow.subject != "commit 9" || app.currentFile == nil {
		t.Fatalf("rapid graph selection applied stale state: row=%#v file=%#v", app.currentRow, app.currentFile)
	}
	app.historySearch.SetText("orbital cache")
	if len(app.searchMatches) != 0 {
		t.Fatalf("default search included a long commit message: %#v", app.searchMatches)
	}
	app.searchMessages.SetActive(true)
	if len(app.searchMatches) != 1 || app.searchMatches[0].subject != "commit 11" {
		t.Fatalf("enabled long-message search missed its commit: %#v", app.searchMatches)
	}
	app.historySearch.SetText("long-release-name")
	if len(app.searchMatches) != 0 {
		t.Fatalf("default reference search included a tag: %#v", app.searchMatches)
	}
	app.searchReferences.SetActive(true)
	if len(app.searchMatches) != 1 || app.searchMatches[0].subject != "commit 11" {
		t.Fatalf("enabled reference search missed its tag: %#v", app.searchMatches)
	}
	app.searchSettings.SetActive(true)
	for gtk.EventsPending() {
		gtk.MainIteration()
	}
	if !app.searchMessages.GetMapped() || !app.searchReferences.GetMapped() {
		t.Fatal("search options were not mapped when their popover opened")
	}
	app.searchSettings.SetActive(false)
	if state := loadUIState(app.statePath); !state.SearchCommitMessages || !state.SearchReferences {
		t.Fatalf("search settings were not persisted: %#v", state)
	}
	app.historySearch.SetText("CoMmIt 8")
	if app.historyStack.GetVisibleChildName() != "search" || len(app.searchMatches) == 0 || app.searchMatches[0].subject != "commit 8" {
		t.Fatalf("search did not limit loaded graph rows: stack=%q matches=%#v", app.historyStack.GetVisibleChildName(), app.searchMatches)
	}
	app.openSearchResult(0)
	deadline = time.Now().Add(2 * time.Second)
	for (app.currentRow == nil || app.currentRow.subject != "commit 8") && time.Now().Before(deadline) {
		for gtk.EventsPending() {
			gtk.MainIteration()
		}
		time.Sleep(10 * time.Millisecond)
	}
	query, _ := app.historySearch.GetText()
	if query != "" || app.historyStack.GetVisibleChildName() != "graph" || app.currentRow == nil || app.currentRow.subject != "commit 8" || !app.historyView.IsFocus() {
		t.Fatalf("search result did not restore graph selection: query=%q stack=%q row=%#v", query, app.historyStack.GetVisibleChildName(), app.currentRow)
	}
	app.resident = true
	app.window.Close()
	for gtk.EventsPending() {
		gtk.MainIteration()
	}
	start, end = app.diffBuffer.GetBounds()
	cleared, _ := app.diffBuffer.GetText(start, end, true)
	if app.window.GetVisible() || app.currentRow != nil || app.currentFile != nil || app.historyRows != nil || app.files != nil || cleared != "" {
		t.Fatalf("hidden view retained repository data: row=%#v file=%#v rows=%d files=%d diff=%q", app.currentRow, app.currentFile, len(app.historyRows), len(app.files), cleared)
	}
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	app.server = newResidentServer(app)
	if err = app.server.start(); err != nil {
		t.Fatal(err)
	}
	defer app.server.stop()
	duplicate := newResidentServer(app)
	if err = duplicate.start(); err == nil {
		duplicate.stop()
		t.Fatal("second resident acquired the process lock")
	}
	connection, err := net.Dial("unix", app.server.socketPath)
	if err != nil {
		t.Fatal(err)
	}
	json.NewEncoder(connection).Encode(openRequest{Path: path, Revision: "HEAD"})
	response := make([]byte, 3)
	connection.Read(response)
	connection.Close()
	reopenDeadline := time.Now().Add(2 * time.Second)
	for app.currentRow == nil && time.Now().Before(reopenDeadline) {
		for gtk.EventsPending() {
			gtk.MainIteration()
		}
		time.Sleep(10 * time.Millisecond)
	}
	if string(response) != "OK\n" || app.currentRow == nil || !app.busy {
		t.Fatalf("warm reopen failed: response=%q row=%#v busy=%v", response, app.currentRow, app.busy)
	}
}

func TestGTKGraphTextScaling(t *testing.T) {
	if os.Getenv("GITI_GTK_SCALE_TEST") == "" {
		t.Skip("set GITI_GTK_SCALE_TEST=1 and increase GDK_DPI_SCALE")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	gtk.Init(nil)
	repo, err := newRepository(testRepository(t), "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	app, err := newGiti(repo, false)
	if err != nil {
		t.Fatal(err)
	}
	defer app.window.Destroy()
	deadline := time.Now().Add(2 * time.Second)
	path := must(gtk.TreePathNewFromIndicesv([]int{0}))
	for app.historyView.GetCellArea(path, app.historyView.GetColumn(0)).GetHeight() <= graphRowHeight && time.Now().Before(deadline) {
		for gtk.EventsPending() {
			gtk.MainIteration()
		}
		time.Sleep(10 * time.Millisecond)
	}
	height := app.historyView.GetCellArea(path, app.historyView.GetColumn(0)).GetHeight()
	iter, err := app.historyStore.GetIter(path)
	if err != nil {
		t.Fatal(err)
	}
	value, err := app.historyStore.GetValue(iter, 0)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := value.GoValue()
	pixbuf, ok := rendered.(*gdk.Pixbuf)
	pixbufHeight := 0
	if ok {
		pixbufHeight = pixbuf.GetHeight()
	}
	if err != nil || !ok || height <= graphRowHeight || pixbufHeight != height {
		t.Fatalf("scaled graph does not fill its GTK row: cell=%d pixbuf=%T/%d err=%v", height, rendered, pixbufHeight, err)
	}
}
