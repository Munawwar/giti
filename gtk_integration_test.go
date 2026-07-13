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
	"github.com/gotk3/gotk3/gtk"
)

func TestGTKSelectionAndMemoryLifecycle(t *testing.T) {
	if os.Getenv("GITI_GTK_TEST") == "" {
		t.Skip("set GITI_GTK_TEST=1 to run the display integration test")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	path := testRepository(t)
	os.WriteFile(filepath.Join(path, "first.txt"), []byte("one\n"+strings.Repeat("more\n", 200)), 0o644)
	os.WriteFile(filepath.Join(path, "second.txt"), []byte("second\n"), 0o644)
	repo, err := newRepository(path, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	gtk.Init(nil)
	app, err := newGiti(repo, false)
	if err != nil {
		t.Fatal(err)
	}
	defer app.window.Destroy()
	theme, themeErr := must(gtk.SettingsGetDefault()).GetProperty("gtk-theme-name")
	themeName, isThemeName := theme.(string)
	if themeErr != nil || !isThemeName || strings.HasSuffix(themeName, "-dark") {
		t.Fatalf("Giti did not force a light GTK theme: theme=%q err=%v", theme, themeErr)
	}
	deadline := time.Now().Add(2 * time.Second)
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
	scroll := app.diffScroller.GetVAdjustment()
	scroll.SetValue(180)
	wanted := scroll.GetValue()
	if wanted == 0 {
		t.Fatal("fixture did not create a scrollable diff")
	}
	app.fileView.SetCursor(must(gtk.TreePathNewFromIndicesv([]int{1})), nil, false)
	app.fileView.SetCursor(must(gtk.TreePathNewFromIndicesv([]int{0})), nil, false)
	deadline = time.Now().Add(2 * time.Second)
	for (app.currentFile == nil || app.currentFile.path != "first.txt" || scroll.GetValue() < wanted-1) && time.Now().Before(deadline) {
		for gtk.EventsPending() {
			gtk.MainIteration()
		}
		time.Sleep(10 * time.Millisecond)
	}
	if restored := scroll.GetValue(); app.currentFile == nil || app.currentFile.path != "first.txt" || restored < wanted-1 || restored > wanted+1 {
		t.Fatalf("file scroll was not restored: got=%v want=%v", restored, wanted)
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
