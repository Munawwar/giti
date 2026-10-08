package main

import (
	"os"
	"strings"
	"sync"
	"testing"
)

func TestCommitReviewPersistsIndependentUpdatesAndComparisons(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	path := fileHistoryInit(t, map[string]string{"sample.txt": "before\n"})
	fileHistoryWrite(t, path, "sample.txt", "after\n")
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
	combined, combinedPath, err := loadCommitReview(repo, row, true)
	if err != nil || combinedPath == savePath || len(combined.Files) != 0 {
		t.Fatalf("comparison reviews mixed: %#v, %s, %v", combined, combinedPath, err)
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
		{ID: "lines", Text: "Why change this?", Lines: []reviewLine{{Old: 2, Kind: "removed", Text: "-before"}, {New: 2, Kind: "added", Text: "+```after"}}},
	}
	line := reviewLine{New: 2, Kind: "added", Text: "+```after", Reviewed: true}
	file.Lines[reviewLineKey(line)] = line
	output := review.markdown()
	for _, expected := range []string{"Repository: /repo", "Commit: " + review.Commit, "Parent commits: " + review.Parents[0], "## sample.txt", "Partially reviewed", "File-wide question", "Old line 2, new line 0 (removed)", "Old line 0, new line 2 (added)", "````diff\n-before\n+```after\n````", "Why change this?"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("export missing %q:\n%s", expected, output)
		}
	}
}
