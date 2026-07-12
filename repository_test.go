package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func testRepository(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", path}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	run("init", "-b", "main")
	run("config", "user.name", "Test User")
	run("config", "user.email", "test@example.com")
	for number := range 12 {
		if err := os.WriteFile(filepath.Join(path, "history.txt"), []byte(fmt.Sprintf("commit %d\n", number)), 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", "history.txt")
		run("commit", "-m", fmt.Sprintf("commit %d", number))
	}
	run("tag", "v1")
	run("branch", "older", "HEAD~5")
	return path
}

func TestRevisionFormsAndHistoryLimit(t *testing.T) {
	path := testRepository(t)
	sha, _ := exec.Command("git", "-C", path, "rev-parse", "HEAD").Output()
	for _, revision := range []string{"HEAD", "main", "v1", strings.TrimSpace(string(sha))} {
		repo, err := newRepository(path, revision)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := repo.history(10, true)
		if err != nil {
			t.Fatal(err)
		}
		commits := 0
		for _, row := range rows {
			if row.kind == "commit" {
				commits++
				if commits == 1 && row.subject != "commit 11" {
					t.Fatalf("unexpected top commit %q", row.subject)
				}
			}
		}
		if commits != 10 {
			t.Fatalf("got %d commits", commits)
		}
	}
	older, _ := newRepository(path, "older")
	rows, _ := older.history(1, true)
	if rows[0].subject != "commit 6" {
		t.Fatalf("unexpected older top commit %q", rows[0].subject)
	}
}

func TestCommitDetailsIncludesRefsAndMergeParents(t *testing.T) {
	path := testRepository(t)
	run := func(args ...string) {
		output, err := exec.Command("git", append([]string{"-C", path}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	run("checkout", "-b", "side")
	os.WriteFile(filepath.Join(path, "side.txt"), []byte("side\n"), 0o644)
	run("add", "side.txt")
	run("commit", "-m", "side")
	run("checkout", "main")
	os.WriteFile(filepath.Join(path, "main.txt"), []byte("main\n"), 0o644)
	run("add", "main.txt")
	run("commit", "-m", "main")
	run("merge", "--no-ff", "side", "-m", "merge side")
	for _, tag := range []string{"one", "two", "three", "four"} {
		run("tag", tag)
	}
	repo, err := newRepository(path, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	details, err := repo.commitDetailsContext(context.Background(), repo.revision)
	if err != nil {
		t.Fatal(err)
	}
	if details.sha != repo.revision || details.author != "Test User" || len(details.parents) != 2 || len(details.tags) != 4 || !strings.Contains(strings.Join(details.branches, " "), "main") {
		t.Fatalf("unexpected commit details: %#v", details)
	}
}

func TestStagedUnstagedAndSingleFileDiffs(t *testing.T) {
	path := testRepository(t)
	if err := os.WriteFile(filepath.Join(path, "staged.txt"), []byte("staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", path, "add", "staged.txt").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(path, "unstaged.txt"), []byte("unstaged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo, _ := newRepository(path, "HEAD")
	rows, err := repo.history(10, true)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].kind != "unstaged" || rows[1].kind != "staged" {
		t.Fatalf("unexpected synthetic order: %v", rows[:2])
	}
	for index, expected := range []string{"unstaged.txt", "staged.txt"} {
		files, fileErr := repo.changedFiles(rows[index], true)
		if fileErr != nil || len(files) != 1 || files[0].path != expected {
			t.Fatalf("unexpected files %v: %v", files, fileErr)
		}
		patch, patchErr := repo.diff(rows[index], files[0], true, false)
		if patchErr != nil || !strings.Contains(patch, "+"+strings.TrimSuffix(expected, ".txt")) {
			t.Fatalf("unexpected patch: %v: %s", patchErr, patch)
		}
	}
}

func TestWhitespaceFullFileAndLimits(t *testing.T) {
	path := testRepository(t)
	if err := os.WriteFile(filepath.Join(path, "history.txt"), []byte("commit        11\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo, _ := newRepository(path, "HEAD")
	rows, _ := repo.history(10, true)
	if rows[0].kind != "commit" {
		t.Fatalf("whitespace-only change was not hidden: %v", rows[0])
	}
	rows, _ = repo.history(10, false)
	if rows[0].kind != "unstaged" {
		t.Fatalf("whitespace change missing in regular mode")
	}
	output, truncated, err := repo.runLimited(5, true, "show", "HEAD:history.txt")
	if err != nil || output != "commi" || !truncated {
		t.Fatalf("limit failed: %q %v %v", output, truncated, err)
	}

	context := make([]string, 20)
	for index := range context {
		context[index] = fmt.Sprintf("line %d", index)
	}
	if err = os.WriteFile(filepath.Join(path, "context.txt"), []byte(strings.Join(context, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exec.Command("git", "-C", path, "add", "context.txt").Run()
	exec.Command("git", "-C", path, "commit", "-m", "add context").Run()
	context[0] = "changed"
	os.WriteFile(filepath.Join(path, "context.txt"), []byte(strings.Join(context, "\n")+"\n"), 0o644)
	repo, _ = newRepository(path, "HEAD")
	rows, _ = repo.history(1, true)
	files, _ := repo.changedFiles(rows[0], true)
	compact, _ := repo.diff(rows[0], files[0], true, false)
	full, _ := repo.diff(rows[0], files[0], true, true)
	if strings.Contains(compact, "line 19") || !strings.Contains(full, "line 19") {
		t.Fatalf("full context mismatch")
	}
	if repo.fileSize(rows[0], files[0]) >= fullFileLimit {
		t.Fatalf("fixture unexpectedly large")
	}
}

func TestRenameAndCopyDetection(t *testing.T) {
	path := testRepository(t)
	if output, err := exec.Command("git", "-C", path, "mv", "history.txt", "renamed.txt").CombinedOutput(); err != nil {
		t.Fatalf("git mv: %v: %s", err, output)
	}
	repo, _ := newRepository(path, "HEAD")
	rows, _ := repo.history(1, true)
	files, err := repo.changedFiles(rows[0], true)
	if err != nil || len(files) != 1 || !strings.HasPrefix(files[0].status, "R") || files[0].oldPath != "history.txt" || files[0].path != "renamed.txt" {
		t.Fatalf("rename not detected: %#v: %v", files, err)
	}

	exec.Command("git", "-C", path, "reset", "--hard", "HEAD").Run()
	original, _ := os.ReadFile(filepath.Join(path, "history.txt"))
	os.WriteFile(filepath.Join(path, "copied.txt"), original, 0o644)
	os.WriteFile(filepath.Join(path, "history.txt"), append(original, []byte("modified\n")...), 0o644)
	exec.Command("git", "-C", path, "add", "history.txt", "copied.txt").Run()
	rows, _ = repo.history(1, true)
	files, err = repo.changedFiles(rows[0], true)
	foundCopy := false
	for _, file := range files {
		foundCopy = foundCopy || strings.HasPrefix(file.status, "C") && file.path == "copied.txt"
	}
	if err != nil || !foundCopy {
		t.Fatalf("copy not detected: %#v: %v", files, err)
	}
}

func TestCanceledGitWorkStops(t *testing.T) {
	repo, err := newRepository(testRepository(t), "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = repo.changedFilesContext(ctx, historyRow{kind: "commit", revision: repo.revision}, true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Git work returned %v", err)
	}
}
