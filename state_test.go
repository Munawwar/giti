package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUIStateUsesXDGConfigAndRoundTrips(t *testing.T) {
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	path := uiStatePath()
	if path != filepath.Join(config, "giti", "state.json") {
		t.Fatalf("unexpected state path %q", path)
	}
	want := uiState{MainPanePosition: 437, RepositoryPanePosition: 362}
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
