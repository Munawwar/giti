package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
)

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

func TestGitiAppPathRecognizesRebuiltResident(t *testing.T) {
	for path, expected := range map[string]bool{
		"/repo/bin/giti-app":                 true,
		"/repo/bin/giti-app-debug":           true,
		"/repo/bin/giti-app (deleted)":       true,
		"/repo/bin/giti-app-debug (deleted)": true,
		"/usr/bin/unrelated (deleted)":       false,
	} {
		if actual := isGitiAppPath(path); actual != expected {
			t.Errorf("isGitiAppPath(%q) = %v, want %v", path, actual, expected)
		}
	}
}
