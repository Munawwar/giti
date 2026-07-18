package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func mergeTestGit(t *testing.T, path string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", path}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func mergeTestWrite(t *testing.T, path, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(path, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mergeTestInit(t *testing.T, files map[string]string) string {
	t.Helper()
	path := t.TempDir()
	mergeTestGit(t, path, "init", "-b", "main")
	mergeTestGit(t, path, "config", "user.name", "Test User")
	mergeTestGit(t, path, "config", "user.email", "test@example.com")
	for name, contents := range files {
		mergeTestWrite(t, path, name, contents)
	}
	mergeTestGit(t, path, "add", ".")
	mergeTestGit(t, path, "commit", "-m", "base")
	return path
}

func mergeTestRow(t *testing.T, rows []historyRow, kind string) historyRow {
	t.Helper()
	for _, row := range rows {
		if row.kind == kind {
			return row
		}
	}
	t.Fatalf("no %q row in %#v", kind, rows)
	return historyRow{}
}

func TestMergeResolutionIsDefaultAndFullMergeRemainsAvailable(t *testing.T) {
	path := mergeTestInit(t, map[string]string{"conflict.txt": "base\n", "clean.txt": "base\n"})
	mergeTestGit(t, path, "checkout", "-b", "side")
	mergeTestWrite(t, path, "conflict.txt", "side\n")
	mergeTestWrite(t, path, "clean.txt", "side\n")
	mergeTestGit(t, path, "commit", "-am", "side")
	mergeTestGit(t, path, "checkout", "main")
	mergeTestWrite(t, path, "conflict.txt", "main\n")
	mergeTestGit(t, path, "commit", "-am", "main")
	command := exec.Command("git", "-C", path, "merge", "--no-ff", "side")
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("merge unexpectedly succeeded: %s", output)
	}
	mergeTestWrite(t, path, "conflict.txt", "resolution\n")
	mergeTestGit(t, path, "add", "conflict.txt")
	mergeTestGit(t, path, "commit", "-m", "merge side")

	repo, err := newRepository(path, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := repo.history(3, true, false)
	if err != nil {
		t.Fatal(err)
	}
	merge := mergeTestRow(t, rows, "commit")
	if len(merge.parents) != 2 {
		t.Fatalf("top commit is not a merge: %#v", merge)
	}
	resolutionFiles, err := repo.changedFiles(merge, true)
	if err != nil || len(resolutionFiles) != 1 || resolutionFiles[0].path != "conflict.txt" {
		t.Fatalf("default merge-resolution files = %#v, %v", resolutionFiles, err)
	}
	fullFiles, err := repo.changedFilesForViewContext(context.Background(), merge, true, false)
	if err != nil || len(fullFiles) != 2 {
		t.Fatalf("full first-parent files = %#v, %v", fullFiles, err)
	}
	resolutionPatch, err := repo.diff(merge, resolutionFiles[0], true, false)
	if err != nil || !strings.Contains(resolutionPatch, "diff --cc conflict.txt") || !strings.Contains(resolutionPatch, "++resolution") || strings.Contains(resolutionPatch, "clean.txt") {
		t.Fatalf("default resolution patch = %q, %v", resolutionPatch, err)
	}
	fullPatch, err := repo.diffForViewContext(context.Background(), merge, fullFiles[0], true, false, false)
	if err != nil || strings.Contains(fullPatch, "diff --cc") {
		t.Fatalf("full first-parent patch = %q, %v", fullPatch, err)
	}
}

func TestWorkingChangesSeparateConflictsResolutionsAndOrdinaryFiles(t *testing.T) {
	path := mergeTestInit(t, map[string]string{"conflict.txt": "base\n", "unstaged.txt": "base\n", "staged.txt": "base\n"})
	mergeTestGit(t, path, "config", "rerere.enabled", "true")
	mergeTestGit(t, path, "checkout", "-b", "side")
	mergeTestWrite(t, path, "conflict.txt", "side\n")
	mergeTestGit(t, path, "commit", "-am", "side")
	mergeTestGit(t, path, "checkout", "main")
	mergeTestWrite(t, path, "conflict.txt", "main\n")
	mergeTestGit(t, path, "commit", "-am", "main")
	merge := func() {
		t.Helper()
		command := exec.Command("git", "-C", path, "merge", "side")
		if output, err := command.CombinedOutput(); err == nil {
			t.Fatalf("merge unexpectedly succeeded: %s", output)
		}
	}
	merge()
	mergeTestWrite(t, path, "unstaged.txt", "worktree\n")
	mergeTestWrite(t, path, "staged.txt", "index\n")
	mergeTestGit(t, path, "add", "staged.txt")

	repo, err := newRepository(path, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := repo.history(2, true, false)
	if err != nil {
		t.Fatal(err)
	}
	conflict, unstaged, staged := mergeTestRow(t, rows, "conflict"), mergeTestRow(t, rows, "unstaged"), mergeTestRow(t, rows, "staged")
	if len(conflict.files) != 1 || conflict.files[0].path != "conflict.txt" || conflict.files[0].conflict != "Both modified" ||
		len(unstaged.files) != 1 || unstaged.files[0].path != "unstaged.txt" || len(staged.files) != 1 || staged.files[0].path != "staged.txt" {
		t.Fatalf("unresolved merge rows overlap or are incomplete: %#v", rows[:3])
	}

	mergeTestGit(t, path, "restore", "unstaged.txt")
	mergeTestGit(t, path, "restore", "--staged", "staged.txt")
	mergeTestGit(t, path, "restore", "staged.txt")
	mergeTestWrite(t, path, "conflict.txt", "remembered resolution\n")
	mergeTestGit(t, path, "add", "conflict.txt")
	mergeTestGit(t, path, "rerere")
	mergeTestGit(t, path, "merge", "--abort")
	merge()

	repo, _ = newRepository(path, "HEAD")
	rows, _, err = repo.history(2, true, false)
	if err != nil {
		t.Fatal(err)
	}
	resolved := mergeTestRow(t, rows, "resolved")
	if len(resolved.files) != 1 || resolved.files[0].path != "conflict.txt" || resolved.files[0].status != "✓" || resolved.files[0].staged {
		t.Fatalf("rerere worktree resolution was not separated: %#v", resolved.files)
	}
	for _, row := range rows {
		if row.kind == "conflict" || row.kind == "unstaged" || row.kind == "staged" {
			t.Fatalf("applied resolution also appeared as %q: %#v", row.kind, row.files)
		}
	}

	mergeTestGit(t, path, "merge", "--abort")
	mergeTestGit(t, path, "config", "rerere.autoupdate", "true")
	merge()
	repo, _ = newRepository(path, "HEAD")
	rows, _, err = repo.history(2, true, false)
	if err != nil {
		t.Fatal(err)
	}
	resolved = mergeTestRow(t, rows, "resolved")
	if len(resolved.files) != 1 || resolved.files[0].path != "conflict.txt" || !resolved.files[0].staged {
		t.Fatalf("auto-staged resolution was not separated: %#v", resolved.files)
	}
	for _, row := range rows {
		if row.kind == "staged" {
			t.Fatalf("auto-staged resolution was duplicated in staged files: %#v", row.files)
		}
	}
}

func TestDisplayLinesUnderstandsCombinedMergePrefixes(t *testing.T) {
	lines := displayLines("diff --cc conflict.txt\nindex 123,456..789\n--- a/conflict.txt\n+++ b/conflict.txt\n@@@ -1,1 -1,1 +1,1 @@@\n- main\n -side\n++resolution\n  context\n")
	want := []struct{ text, tag string }{{"@@@ -1,1 -1,1 +1,1 @@@\n", ""}, {"- main\n", "removed"}, {" -side\n", "removed"}, {"++resolution\n", "added"}, {"  context\n", ""}}
	if len(lines) != len(want) {
		t.Fatalf("combined display lines = %#v", lines)
	}
	for index := range want {
		if lines[index].text != want[index].text || lines[index].tag != want[index].tag {
			t.Fatalf("combined line %d = %#v, want %#v", index, lines[index], want[index])
		}
	}
}
