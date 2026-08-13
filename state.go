package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
)

type uiState struct {
	MainPanePosition       int  `json:"main_pane_position"`
	RepositoryPanePosition int  `json:"repository_pane_position"`
	SearchCommitMessages   bool `json:"search_commit_messages"`
	SearchReferences       bool `json:"search_references"`
	CompactLineNumbers     bool `json:"compact_line_numbers"`
}

func uiStatePath() string {
	config, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(config, "giti", "state.json")
}

func loadUIState(path string) uiState {
	var state uiState
	data, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(data, &state)
	}
	if state.MainPanePosition < 0 {
		state.MainPanePosition = 0
	}
	if state.RepositoryPanePosition < 0 {
		state.RepositoryPanePosition = 0
	}
	return state
}

func saveUIState(path string, state uiState) error {
	return patchUIState(path, func(current *uiState) { *current = state })
}

func patchUIState(path string, patch func(*uiState)) error {
	if path == "" {
		return nil
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	state := loadUIState(path)
	patch(&state)
	temporary, err := os.CreateTemp(directory, "state-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0o600); err == nil {
		encoder := json.NewEncoder(temporary)
		encoder.SetIndent("", "  ")
		err = encoder.Encode(state)
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temporaryPath, path)
	}
	return err
}
