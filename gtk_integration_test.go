package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gotk3/gotk3/gdk"
	"github.com/gotk3/gotk3/gtk"
)

func TestGTKSelectionAndMemoryLifecycle(t *testing.T) {
	if os.Getenv("GITSKIM_GTK_TEST") == "" {
		t.Skip("set GITSKIM_GTK_TEST=1 to run the display integration test")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	path := testRepository(t)
	os.WriteFile(filepath.Join(path, "first.txt"), []byte("one\ntwo\nthree\n"), 0o644)
	os.WriteFile(filepath.Join(path, "second.txt"), []byte("second\n"), 0o644)
	repo, err := newRepository(path, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	gtk.Init(nil)
	app := newGitSkim(repo, false)
	defer app.window.Destroy()
	deadline := time.Now().Add(2 * time.Second)
	for !app.window.IsMaximized() && time.Now().Before(deadline) {
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
	app.fileView.SetCursor(must(gtk.TreePathNewFromIndicesv([]int{1})), nil, false)
	start, end = app.diffBuffer.GetBounds()
	second, _ := app.diffBuffer.GetText(start, end, true)
	if app.fullFileToggle.GetActive() || app.currentFile.path != "second.txt" || !strings.Contains(second, "+second") || strings.Contains(second, "+one") {
		t.Fatalf("file switch retained state or content: full=%v file=%#v diff=%q", app.fullFileToggle.GetActive(), app.currentFile, second)
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
