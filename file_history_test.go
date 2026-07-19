package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func fileHistoryGit(t *testing.T, path string, args ...string) string {
	t.Helper()
	output, err := exec.Command("git", append([]string{"-C", path}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func fileHistoryWrite(t *testing.T, path, name, contents string) {
	t.Helper()
	fullPath := filepath.Join(path, name)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fileHistoryInit(t *testing.T, files map[string]string) string {
	t.Helper()
	path := t.TempDir()
	fileHistoryGit(t, path, "init", "-b", "main")
	fileHistoryGit(t, path, "config", "user.name", "Test User")
	fileHistoryGit(t, path, "config", "user.email", "test@example.com")
	for name, contents := range files {
		fileHistoryWrite(t, path, name, contents)
	}
	fileHistoryGit(t, path, "add", ".")
	fileHistoryGit(t, path, "commit", "-m", "base")
	return path
}

func TestFileSearchKeepsRegularGraphAndCommitViewComplete(t *testing.T) {
	path := fileHistoryInit(t, map[string]string{"README.md": "base\n", "other.txt": "base\n", "docs/guide.md": "base\n", ":(exclude)README.md": "base\n"})
	fileHistoryWrite(t, path, "other.txt", "other commit\n")
	fileHistoryGit(t, path, "commit", "-am", "other change")
	fileHistoryWrite(t, path, "README.md", "readme commit\n")
	fileHistoryWrite(t, path, "other.txt", "combined commit\n")
	fileHistoryGit(t, path, "commit", "-am", "readme and other change")
	fileHistoryWrite(t, path, "docs/guide.md", "guide commit\n")
	fileHistoryGit(t, path, "commit", "-am", "guide change")

	repo, err := newRepository(path, historySpec{Revision: "HEAD", Path: "README.md"})
	if err != nil {
		t.Fatal(err)
	}
	rows, hasMore, err := repo.history(10, true, false)
	if err != nil || hasMore || len(rows) != 4 || rows[0].subject != "guide change" || repo.windowTitle() != "Giti — "+filepath.Base(path) {
		t.Fatalf("regular graph was unexpectedly scoped: rows=%#v more=%v title=%q err=%v", rows, hasMore, repo.windowTitle(), err)
	}
	matches, hasMore, err := repo.fileHistoryContext(context.Background(), "README.md", false, 1)
	if err != nil || !hasMore || len(matches) != 1 || matches[0].subject != "readme and other change" {
		t.Fatalf("bounded README search mismatch: rows=%#v more=%v err=%v", matches, hasMore, err)
	}
	files, err := repo.changedFiles(matches[0], true)
	if err != nil || len(files) != 2 {
		t.Fatalf("selected result did not retain the commit's complete diff: files=%#v err=%v", files, err)
	}
	docs, hasMore, err := repo.fileHistoryContext(context.Background(), "docs", false, 10)
	if err != nil || hasMore || len(docs) != 2 || docs[0].subject != "guide change" || docs[1].subject != "base" {
		t.Fatalf("directory search mismatch: rows=%#v more=%v err=%v", docs, hasMore, err)
	}
	literal, hasMore, err := repo.fileHistoryContext(context.Background(), ":(exclude)README.md", false, 10)
	if err != nil || hasMore || len(literal) != 1 || literal[0].subject != "base" {
		t.Fatalf("file name was interpreted as Git pathspec magic: rows=%#v more=%v err=%v", literal, hasMore, err)
	}
}

func TestFileSearchFollowCrossesRename(t *testing.T) {
	oldName, newName := "old name.txt", "new name.txt"
	path := fileHistoryInit(t, map[string]string{oldName: "base\n"})
	fileHistoryWrite(t, path, oldName, "before rename\n")
	fileHistoryGit(t, path, "commit", "-am", "old-name change")
	fileHistoryGit(t, path, "mv", oldName, newName)
	fileHistoryGit(t, path, "commit", "-m", "rename file")
	fileHistoryWrite(t, path, newName, "after rename\n")
	fileHistoryGit(t, path, "commit", "-am", "new-name change")

	repo, err := newRepository(path, historySpec{Revision: "HEAD", Path: newName, Follow: true})
	if err != nil {
		t.Fatal(err)
	}
	plain, _, err := repo.fileHistoryContext(context.Background(), newName, false, 10)
	if err != nil {
		t.Fatal(err)
	}
	followed, _, err := repo.fileHistoryContext(context.Background(), newName, true, 10)
	if err != nil {
		t.Fatal(err)
	}
	subjects := func(rows []historyRow) string {
		values := make([]string, len(rows))
		for index, row := range rows {
			values[index] = row.subject
		}
		return strings.Join(values, "\n")
	}
	if strings.Contains(subjects(plain), "old-name change") || !strings.Contains(subjects(followed), "old-name change") || !strings.Contains(subjects(followed), "base") {
		t.Fatalf("rename search mismatch: plain=%v followed=%v", subjects(plain), subjects(followed))
	}
}

func TestInitialFileSearchPathIsRelativeToInvocationDirectory(t *testing.T) {
	path := fileHistoryInit(t, map[string]string{"docs/guide.md": "base\n"})
	repo, err := newRepository(filepath.Join(path, "docs"), historySpec{Revision: "HEAD", Path: "guide.md"})
	if err != nil || repo.searchPath != "docs/guide.md" {
		t.Fatalf("subdirectory path was not normalized: repo=%#v err=%v", repo, err)
	}
	if _, err = newRepository(filepath.Join(path, "docs"), historySpec{Revision: "HEAD", Path: "../../outside"}); err == nil || !strings.Contains(err.Error(), "outside the repository") {
		t.Fatalf("outside path was accepted: %v", err)
	}
	if _, err = newRepository(path, historySpec{Revision: "HEAD", Path: "docs", Follow: true}); err == nil || !strings.Contains(err.Error(), "not directories") {
		t.Fatalf("directory --follow was accepted: %v", err)
	}
	if _, _, err = repo.fileHistoryContext(context.Background(), "../outside", false, 10); err == nil || !strings.Contains(err.Error(), "outside the repository") {
		t.Fatalf("outside UI path was accepted: %v", err)
	}
}
