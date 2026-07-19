package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBashCompletionCombinesRevisionsAndPaths(t *testing.T) {
	repository := fileHistoryInit(t, map[string]string{"README.md": "read me\n", "Road Map.md": "road map\n", "Resources/icon.svg": "icon\n"})
	fileHistoryGit(t, repository, "branch", "Release")
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "completions", "giti.bash")
	complete := func(words ...string) []string {
		arguments := append([]string{"-c", `source "$1"
shift
compopt() { :; }
COMP_WORDS=("$@")
COMP_CWORD=$((${#COMP_WORDS[@]} - 1))
_giti
printf '%s\n' "${COMPREPLY[@]}"`, "bash", script}, words...)
		command := exec.Command("bash", arguments...)
		command.Dir = repository
		output, commandErr := command.CombinedOutput()
		if commandErr != nil {
			t.Fatalf("complete %q: %v: %s", strings.Join(words, " "), commandErr, output)
		}
		return strings.Split(strings.TrimSpace(string(output)), "\n")
	}

	joined := strings.Join(complete("giti", "R"), "\n")
	for _, candidate := range []string{"README.md", "Road Map.md", "Resources", "Release"} {
		if !strings.Contains(joined, candidate) {
			t.Fatalf("combined completion missed %q: %q", candidate, joined)
		}
	}
	afterSeparator := strings.Join(complete("giti", "--", "R"), "\n")
	if strings.Contains(afterSeparator, "Release") || !strings.Contains(afterSeparator, "README.md") {
		t.Fatalf("path-only completion after -- was %q", afterSeparator)
	}
}
