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
)

func TestLauncherArguments(t *testing.T) {
	tests := []struct {
		args                         []string
		revision                     string
		foreground, force, wantError bool
	}{
		{args: []string{"giti"}, revision: "HEAD"},
		{args: []string{"giti", "main"}, revision: "main"},
		{args: []string{"giti", "-f"}, revision: "HEAD", foreground: true},
		{args: []string{"giti", "--foreground", "v1"}, revision: "v1", foreground: true},
		{args: []string{"giti", "-1"}, revision: "HEAD", force: true},
		{args: []string{"giti", "-f", "-1"}, revision: "HEAD", wantError: true},
		{args: []string{"giti", "--unknown"}, revision: "HEAD", wantError: true},
		{args: []string{"giti", "one", "two"}, revision: "HEAD", wantError: true},
	}
	for _, test := range tests {
		revision, foreground, force, err := launcherArguments(test.args)
		if revision != test.revision || foreground != test.foreground || force != test.force || (err != nil) != test.wantError {
			t.Errorf("launcherArguments(%q) = %q, %v, %v, %v", test.args, revision, foreground, force, err)
		}
	}
}

func TestContactResident(t *testing.T) {
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
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
		if json.NewDecoder(connection).Decode(&request) == nil && request.Path == "/repo" && request.Revision == "main" {
			connection.Write([]byte("OK\n"))
		}
	}()
	if response := contactResident(openRequest{Path: "/repo", Revision: "main"}); response != "OK" {
		t.Fatalf("unexpected response %q", response)
	}
}

func TestContactResidentMissing(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	if response := contactResident(openRequest{Path: "/repo", Revision: "HEAD"}); response != "" {
		t.Fatalf("unexpected response without resident %q", response)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "giti.sock")); !os.IsNotExist(err) {
		t.Fatalf("launcher created a socket: %v", err)
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
