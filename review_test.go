package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestCommitReviewPersistsIndependentUpdatesAndAmend(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	path := fileHistoryInit(t, map[string]string{"sample.txt": "before\n", "other.txt": "before\n"})
	fileHistoryWrite(t, path, "sample.txt", "after\n")
	fileHistoryWrite(t, path, "other.txt", "after\n")
	fileHistoryGit(t, path, "commit", "-am", "change")
	repo, err := newRepository(path, historySpec{Revision: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	row := historyRow{kind: "commit", revision: repo.revision}
	review, savePath, err := loadCommitReview(repo, row, false)
	if err != nil {
		t.Fatal(err)
	}
	var writes sync.WaitGroup
	for _, name := range []string{"sample.txt", "other.txt"} {
		writes.Add(1)
		go func() {
			defer writes.Done()
			_, saveErr := saveCommitReview(savePath, review, func(current *commitReview) {
				current.file(name).Comments = append(current.file(name).Comments, reviewComment{ID: name, Text: "Remember " + name})
			})
			if saveErr != nil {
				t.Error(saveErr)
			}
		}()
	}
	writes.Wait()
	loaded, _, err := loadCommitReview(repo, row, false)
	if err != nil || len(loaded.Files) != 2 || loaded.Files["sample.txt"].Comments[0].Text != "Remember sample.txt" {
		t.Fatalf("concurrent notes lost: %#v, %v", loaded, err)
	}
	info, err := os.Stat(savePath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("notes permissions: %v, %v", info, err)
	}
	fileHistoryGit(t, path, "commit", "--amend", "-m", "amended change")
	row.revision = fileHistoryGit(t, path, "rev-parse", "HEAD")
	amended, _, err := loadCommitReview(repo, row, false)
	if err != nil || len(amended.Files) != 0 {
		t.Fatalf("amended commit inherited review: %#v, %v", amended, err)
	}
	if err := os.WriteFile(savePath, []byte("broken JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := saveCommitReview(savePath, review, func(current *commitReview) { current.file("sample.txt").Reviewed = true }); err == nil {
		t.Fatal("corrupt notes were silently overwritten")
	}
	if data, _ := os.ReadFile(savePath); string(data) != "broken JSON" {
		t.Fatalf("corrupt notes were lost: %s", data)
	}
}

func TestReviewMarkdownCapturesBothSidesAndFileComments(t *testing.T) {
	review := &commitReview{Repository: "/repo", Commit: strings.Repeat("a", 40), Comparison: "first-parent", Parents: []string{strings.Repeat("b", 40)}, Files: make(map[string]*reviewFile)}
	file := review.file("sample.txt")
	file.Comments = []reviewComment{
		{ID: "file", Text: "File-wide question"},
		{ID: "lines", Text: "Why change this?", Lines: []reviewLine{{Old: 2, Kind: "removed", Text: "-before"}, {New: 2, Kind: "added", Text: "+after"}, {Old: 3, New: 3, Kind: "context", Text: " ```"}}},
	}
	line := reviewLine{New: 2, Kind: "added", Text: "+after", Reviewed: true}
	file.Lines[reviewLineKey(line)] = line
	output := review.markdown([]changedFile{{path: "sample.txt", additions: 1, deletions: 1}, {path: "untouched.txt", additions: 2}})
	for _, expected := range []string{"**Repository:** `/repo`", "**Commit:** `" + review.Commit + "`", "**Parent commits:** " + review.Parents[0], "## sample.txt", "Partially reviewed", "File-wide question", "Old line 2, new line 0 (removed)", "Old line 0, new line 2 (added)", "````diff\n-before\n+after\n ```\n````", "Why change this?", "`untouched.txt` | Unreviewed | 0", "1 of 4 lines reviewed"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("export missing %q:\n%s", expected, output)
		}
	}
}

func TestReviewCleanupRetentionThrottleAndConcurrentWriters(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "giti", "reviews")
	now := time.Now().UTC()
	makeReview := func(repository, name string, age time.Duration) string {
		t.Helper()
		path := filepath.Join(directory, repository, name+"-first-parent.json")
		seed := &commitReview{Repository: repository, Commit: name, Comparison: "first-parent"}
		if _, err := saveCommitReview(path, seed, func(review *commitReview) {
			review.file("sample.txt").Comments = []reviewComment{{ID: "note", Text: "Review question"}}
		}); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
		return path
	}
	old := makeReview("repo-a", "old", 31*24*time.Hour)
	other := makeReview("repo-b", "old", 31*24*time.Hour)
	fresh := makeReview("repo-a", "fresh", time.Hour)
	boundary := makeReview("repo-a", "boundary", 30*24*time.Hour)
	busy := makeReview("repo-a", "busy", 31*24*time.Hour)
	// Hold the writer's real lock while cleanup visits an otherwise expired review.
	writer := must(lockReviewData(busy+".lock", syscall.LOCK_EX))
	defer writer.Close()
	defer syscall.Flock(int(writer.Fd()), syscall.LOCK_UN)
	if err := cleanupCommitReviews(directory, now); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{old, other} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expired review remains: %s, %v", path, err)
		}
	}
	for _, path := range []string{fresh, boundary, busy} {
		if data, err := os.ReadFile(path); err != nil || !strings.Contains(string(data), "Review question") {
			t.Fatalf("retained review lost notes: %s, %v", path, err)
		}
	}
	if err := syscall.Flock(int(writer.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	pending := makeReview("repo-b", "pending", 31*24*time.Hour)
	if err := cleanupCommitReviews(directory, now.Add(23*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{pending, busy} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("cleanup ran again within 24 hours: %s, %v", path, err)
		}
	}
	// Another process owning the cleanup lock must prevent a competing scan.
	cleaner := must(lockReviewData(filepath.Join(directory, ".cleanup.lock"), syscall.LOCK_EX))
	defer cleaner.Close()
	defer syscall.Flock(int(cleaner.Fd()), syscall.LOCK_UN)
	if err := cleanupCommitReviews(directory, now.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pending); err != nil {
		t.Fatalf("competing cleaner ignored the process lock: %v", err)
	}
	if err := syscall.Flock(int(cleaner.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := cleanupCommitReviews(directory, now.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{pending, busy, boundary} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("next daily cleanup missed expired review: %s, %v", path, err)
		}
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("next cleanup removed a fresh review: %v", err)
	}
}
