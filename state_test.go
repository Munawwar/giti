package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestUIState(t *testing.T) {
	{
		config := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", config)
		path := uiStatePath()
		if path != filepath.Join(config, "giti", "state.json") {
			t.Fatalf("unexpected state path %q", path)
		}
		want := uiState{MainPanePosition: 437, RepositoryPanePosition: 362, SearchCommitMessages: true, SearchReferences: true}
		if err := saveUIState(path, want); err != nil {
			t.Fatal(err)
		}
		got := loadUIState(path)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got != want || info.Mode().Perm() != 0o600 {
			t.Fatalf("state did not round-trip securely: got=%#v mode=%v", got, info.Mode().Perm())
		}
		if err = os.WriteFile(path, []byte(`{"main_pane_position":-2,"repository_pane_position":-1}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if got = loadUIState(path); got != (uiState{}) {
			t.Fatalf("invalid positions were accepted: %#v", got)
		}
	}
	{
		path := filepath.Join(t.TempDir(), "giti", "state.json")
		first := uiState{MainPanePosition: 400, SearchReferences: true}
		if err := saveUIState(path, first); err != nil {
			t.Fatal(err)
		}
		lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			t.Fatal(err)
		}
		defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		if err = saveUIState(path, uiState{MainPanePosition: 900, SearchCommitMessages: true}); err != nil {
			t.Fatal(err)
		}
		if got := loadUIState(path); got != first {
			t.Fatalf("contending writer replaced the first state: got=%#v want=%#v", got, first)
		}
	}
}
