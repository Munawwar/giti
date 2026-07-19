package main

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLauncherArguments(t *testing.T) {
	path := testRepository(t)
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("read me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(path, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		args                         []string
		history                      historySpec
		foreground, force, wantError bool
	}{
		{args: []string{"giti"}, history: historySpec{Revision: "HEAD"}},
		{args: []string{"giti", "main"}, history: historySpec{Revision: "main"}},
		{args: []string{"giti", "README.md"}, history: historySpec{Revision: "HEAD", Path: "README.md"}},
		{args: []string{"giti", "main", "README.md"}, history: historySpec{Revision: "main", Path: "README.md"}},
		{args: []string{"giti", "--", "README.md"}, history: historySpec{Revision: "HEAD", Path: "README.md"}},
		{args: []string{"giti", "--follow", "README.md"}, history: historySpec{Revision: "HEAD", Path: "README.md", Follow: true}},
		{args: []string{"giti", "-f"}, history: historySpec{Revision: "HEAD"}, foreground: true},
		{args: []string{"giti", "--foreground", "v1"}, history: historySpec{Revision: "v1"}, foreground: true},
		{args: []string{"giti", "-1"}, history: historySpec{Revision: "HEAD"}, force: true},
		{args: []string{"giti", "-f", "-1"}, history: historySpec{Revision: "HEAD"}, wantError: true},
		{args: []string{"giti", "--unknown"}, history: historySpec{Revision: "HEAD"}, wantError: true},
		{args: []string{"giti", "--follow"}, history: historySpec{Revision: "HEAD"}, wantError: true},
		{args: []string{"giti", "--follow", "docs"}, history: historySpec{Revision: "HEAD", Path: "docs", Follow: true}, wantError: true},
		{args: []string{"giti", "--", "README.md", "history.txt"}, history: historySpec{Revision: "HEAD"}, wantError: true},
		{args: []string{"giti", "--follow", "--", "README.md", "history.txt"}, history: historySpec{Revision: "HEAD", Follow: true}, wantError: true},
		{args: []string{"giti", "main", "older"}, history: historySpec{Revision: "HEAD"}, wantError: true},
		{args: []string{"giti", "main~2..main"}, history: historySpec{Revision: "HEAD"}, wantError: true},
		{args: []string{"giti", "main", "older", "--", "README.md"}, history: historySpec{Revision: "HEAD"}, wantError: true},
	}
	for _, test := range tests {
		history, foreground, force, err := launcherArguments(test.args, path)
		historyMismatch := !test.wantError && (history != test.history || foreground != test.foreground || force != test.force)
		if historyMismatch || (err != nil) != test.wantError {
			t.Errorf("launcherArguments(%q) = %#v, %v, %v, %v", test.args, history, foreground, force, err)
		}
	}
	if err := os.WriteFile(filepath.Join(path, "main"), []byte("ambiguous\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := launcherArguments([]string{"giti", "main"}, path); err == nil || !strings.Contains(err.Error(), "both a revision and a path") {
		t.Fatalf("ambiguous revision/path was accepted: %v", err)
	}
}

func TestLaunchModeUsesAnotherWindowWhenResidentIsBusy(t *testing.T) {
	if launchMode("", false) != "--resident" || launchMode("OK", false) != "--resident" || launchMode("BUSY", false) != "--ephemeral" || launchMode("", true) != "--ephemeral" {
		t.Fatal("launcher did not isolate a second repository in an ephemeral window")
	}
}

func TestContactResident(t *testing.T) {
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	request := openRequest{Path: "/repo", History: historySpec{Revision: "main", Path: "README.md", Follow: true}}
	if response := contactResident(request); response != "" {
		t.Fatalf("unexpected response without resident %q", response)
	}
	if _, err := os.Stat(filepath.Join(runtimeDir, "giti.sock")); !os.IsNotExist(err) {
		t.Fatalf("launcher created a socket: %v", err)
	}
	listener, err := net.Listen("unix", filepath.Join(runtimeDir, "giti.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		var request openRequest
		if json.NewDecoder(connection).Decode(&request) == nil && request.Path == "/repo" && request.History == (historySpec{Revision: "main", Path: "README.md", Follow: true}) {
			connection.Write([]byte("OK\n"))
		}
	}()
	if response := contactResident(request); response != "OK" {
		t.Fatalf("unexpected response %q", response)
	}
}

func TestResidentStalledClientDoesNotBlockLaunches(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	server := newResidentServer(&giti{busy: true})
	if err := server.start(); err != nil {
		t.Fatal(err)
	}
	defer server.stop()
	stalled, err := net.Dial("unix", server.socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stalled.Close()
	started := time.Now()
	if response := contactResident(openRequest{Path: "/repo", History: historySpec{Revision: "HEAD"}}); response != "BUSY" {
		t.Fatalf("stalled client blocked resident response: %q", response)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("resident response took %v behind stalled client", elapsed)
	}
}

func TestStopLegacyResidentAfterExecutableReplacement(t *testing.T) {
	runtimeDir := t.TempDir()
	app := filepath.Join(runtimeDir, "giti-app")
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(sleep)
	if err == nil {
		err = os.WriteFile(app, binary, 0o755)
	}
	if err != nil {
		t.Fatalf("copy helper executable: %v", err)
	}
	process := exec.Command(app, "30")
	if err = process.Start(); err != nil {
		t.Fatal(err)
	}
	defer process.Process.Kill()
	if err = os.WriteFile(filepath.Join(runtimeDir, "giti.pid"), []byte(strconv.Itoa(process.Process.Pid)+"\n"), 0o600); err == nil {
		err = os.Remove(app)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err = stopResident(runtimeDir, filepath.Join(runtimeDir, "giti")); err != nil {
		t.Fatal(err)
	}
	_ = process.Wait()
	if _, err = os.Stat(filepath.Join(runtimeDir, "giti.pid")); !os.IsNotExist(err) {
		t.Fatalf("PID file survived restart: %v", err)
	}
}

func TestStopResidentPreservesCoordinationFilesWhileLocked(t *testing.T) {
	runtimeDir := t.TempDir()
	lock, err := os.OpenFile(filepath.Join(runtimeDir, "giti.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	pidPath, socketPath := filepath.Join(runtimeDir, "giti.pid"), filepath.Join(runtimeDir, "giti.sock")
	if err = os.WriteFile(pidPath, []byte("invalid\n"), 0o600); err == nil {
		err = os.WriteFile(socketPath, nil, 0o600)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err = stopResident(runtimeDir, filepath.Join(runtimeDir, "giti")); err == nil || !strings.Contains(err.Error(), "still owns") {
		t.Fatalf("locked resident returned %v", err)
	}
	for _, path := range []string{pidPath, socketPath} {
		if _, err = os.Stat(path); err != nil {
			t.Fatalf("coordination file %s was removed: %v", path, err)
		}
	}
}
