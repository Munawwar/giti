package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
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

func TestSortedReferencesDoesNotMutateSource(t *testing.T) {
	branches, tags := []string{"zeta", remoteRefPrefix + "origin/HEAD", "main" + headRefSuffix, "alpha"}, []string{"v2", "v1"}
	sortedBranches, sortedTags := sortedReferences(branches, tags)
	if strings.Join(sortedBranches, ",") != "main <- HEAD,alpha,zeta,refs/remotes/origin/HEAD" || strings.Join(sortedTags, ",") != "v1,v2" || branches[0] != "zeta" || tags[0] != "v2" {
		t.Fatalf("references not independently sorted: %v %v from %v %v", sortedBranches, sortedTags, branches, tags)
	}
}

func TestSortedReferencesKeepsLocalBranchesBeforeRemotes(t *testing.T) {
	branches := []string{"tsk-2407-products", remoteRefPrefix + "origin/tsk-2407-products", "alpha", remoteRefPrefix + "origin/alpha"}
	sorted, _ := sortedReferences(branches, nil)
	want := "alpha,tsk-2407-products,refs/remotes/origin/alpha,refs/remotes/origin/tsk-2407-products"
	if strings.Join(sorted, ",") != want {
		t.Fatalf("local and remote branches were not grouped consistently: %v", sorted)
	}
}

func TestReferenceMetadataIncludesHeadAndUpstream(t *testing.T) {
	output := "refs/heads/main\x00refs/remotes/origin/main\x00*\nrefs/remotes/origin/main\x00\x00 \nrefs/tags/v1\x00\x00 \n"
	branches, tags, upstreams := referenceMetadata(output)
	if strings.Join(branches, ",") != "main <- HEAD,refs/remotes/origin/main" || strings.Join(tags, ",") != "v1" || upstreams["main"] != "refs/remotes/origin/main" {
		t.Fatalf("reference metadata was not parsed: %v %v %v", branches, tags, upstreams)
	}
}

func TestRevisionFormsAndHistoryLimit(t *testing.T) {
	path := testRepository(t)
	sha, _ := exec.Command("git", "-C", path, "rev-parse", "HEAD").Output()
	for _, revision := range []string{"HEAD", "main", "v1", strings.TrimSpace(string(sha))} {
		repo, err := newRepository(path, revision)
		if err != nil {
			t.Fatal(err)
		}
		rows, hasMore, err := repo.history(10, true, false)
		if err != nil {
			t.Fatal(err)
		}
		commits := 0
		for _, row := range rows {
			if row.kind == "commit" {
				commits++
				if commits == 1 && (row.subject != "commit 11" || row.timestamp == 0) {
					t.Fatalf("unexpected top commit %q", row.subject)
				}
			}
		}
		if commits != 10 || !hasMore {
			t.Fatalf("got %d commits", commits)
		}
		last := rows[len(rows)-1]
		if len(last.graph.next) != 1 || len(last.graph.next[0].from) != 1 {
			t.Fatalf("limited history does not continue into its lookahead: %#v", last.graph)
		}
	}
	older, _ := newRepository(path, "older")
	rows, _, _ := older.history(1, true, false)
	if rows[0].subject != "commit 6" {
		t.Fatalf("unexpected older top commit %q", rows[0].subject)
	}
	rows, hasMore, err := older.history(7, true, false)
	if err != nil || len(rows) != 7 || hasMore {
		t.Fatalf("exhausted history was not detected: rows=%d more=%v err=%v", len(rows), hasMore, err)
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
	run("merge", "--no-ff", "side", "-m", "merge side", "-m", "Explain the merge.\n\n1. Keep both changes\n2. Preserve history")
	run("remote", "add", "origin", path)
	run("update-ref", "refs/remotes/origin/main", "HEAD")
	run("branch", "--set-upstream-to=origin/main", "main")
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
	if details.sha != repo.revision || details.author != "Test User" || !strings.Contains(details.body, "1. Keep both changes") || len(details.parents) != 2 || strings.Join(details.tags, ",") != "four,one,three,two" || !strings.Contains(strings.Join(details.branches, " "), remoteRefPrefix+"origin/main") || details.upstreams["main"] != remoteRefPrefix+"origin/main" {
		t.Fatalf("unexpected commit details: %#v", details)
	}
	rows, _, err := repo.history(5, true, true)
	if err != nil || len(rows) != 5 || len(rows[0].parents) != 2 || rows[0].graph.position != 0 || !strings.Contains(rows[0].body, "Preserve history") || rows[0].upstreams["main"] != remoteRefPrefix+"origin/main" {
		t.Fatalf("merge topology was not loaded: rows=%#v err=%v", rows, err)
	}
	for _, row := range rows {
		if row.kind != "commit" || row.revision == "" || len(row.graph.lanes) == 0 {
			t.Fatalf("history contains a terminal connector or incomplete commit: %#v", row)
		}
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
	rows, _, err := repo.history(10, true, false)
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
	rows, _, _ := repo.history(10, true, false)
	if rows[0].kind != "commit" {
		t.Fatalf("whitespace-only change was not hidden: %v", rows[0])
	}
	rows, _, _ = repo.history(10, false, false)
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
	rows, _, _ = repo.history(1, true, false)
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
	rows, _, _ := repo.history(1, true, false)
	files, err := repo.changedFiles(rows[0], true)
	if err != nil || len(files) != 1 || !strings.HasPrefix(files[0].status, "R") || files[0].oldPath != "history.txt" || files[0].path != "renamed.txt" {
		t.Fatalf("rename not detected: %#v: %v", files, err)
	}

	exec.Command("git", "-C", path, "reset", "--hard", "HEAD").Run()
	original, _ := os.ReadFile(filepath.Join(path, "history.txt"))
	os.WriteFile(filepath.Join(path, "copied.txt"), original, 0o644)
	os.WriteFile(filepath.Join(path, "history.txt"), append(original, []byte("modified\n")...), 0o644)
	exec.Command("git", "-C", path, "add", "history.txt", "copied.txt").Run()
	rows, _, _ = repo.history(1, true, false)
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
	bin, pidFile := t.TempDir(), filepath.Join(t.TempDir(), "git.pid")
	git := filepath.Join(bin, "git")
	if err := os.WriteFile(git, []byte("#!/bin/sh\nprintf '%s' \"$$\" >\"$GITI_TEST_PID\"\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("GITI_TEST_PID", pidFile)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := (&repository{path: t.TempDir()}).runContext(ctx, "status")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("canceled Git process returned %v after %v", err, time.Since(started))
	}
	data, readErr := os.ReadFile(pidFile)
	pid, parseErr := strconv.Atoi(string(data))
	if readErr != nil || parseErr != nil {
		t.Fatalf("Git process did not record its PID: %q, %v, %v", data, readErr, parseErr)
	}
	if killErr := syscall.Kill(pid, 0); !errors.Is(killErr, syscall.ESRCH) {
		t.Fatalf("canceled Git process %d is still alive: %v", pid, killErr)
	}
}

func TestGitParsingPreservesUnusualNamesAndMarkerText(t *testing.T) {
	path := testRepository(t)
	tracked, untracked := " tracked\tline\nend .txt", " untracked\tline\nend .txt"
	if err := os.WriteFile(filepath.Join(path, tracked), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "--", tracked}, {"commit", "-m", "subject __GITI_FIELD__", "-m", "body __GITI_COMMIT__"}} {
		if output, err := exec.Command("git", append([]string{"-C", path}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(path, tracked), []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, untracked), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo, err := newRepository(path, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := repo.history(2, true, true)
	if err != nil || rows[0].kind != "unstaged" || rows[1].subject != "subject __GITI_FIELD__" || rows[1].body != "body __GITI_COMMIT__" {
		t.Fatalf("history parsing lost marker text: rows=%#v err=%v", rows, err)
	}
	files, err := repo.changedFiles(rows[0], true)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{tracked: true, untracked: true}
	for _, file := range files {
		delete(want, file.path)
	}
	if len(want) != 0 {
		t.Fatalf("unusual file names were not preserved: missing=%v files=%#v", want, files)
	}
	details, err := repo.commitDetailsContext(context.Background(), repo.revision)
	if err != nil || details.subject != "subject __GITI_FIELD__" || details.body != "body __GITI_COMMIT__" {
		t.Fatalf("commit details lost marker text: %#v err=%v", details, err)
	}
}
