import os
import subprocess
import tempfile
import unittest

from gitskim import FULL_FILE_LIMIT, Repository


class RepositoryTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.path = self.temp.name
        self.git("init", "-b", "main")
        self.git("config", "user.name", "Test User")
        self.git("config", "user.email", "test@example.com")
        for number in range(12):
            self.write("history.txt", f"commit {number}\n")
            self.git("add", "history.txt")
            self.git("commit", "-m", f"commit {number}")
        self.git("tag", "v1")
        self.git("branch", "older", "HEAD~5")

    def tearDown(self):
        self.temp.cleanup()

    def git(self, *args):
        return subprocess.run(
            ["git", "-C", self.path, *args], check=True, text=True, capture_output=True
        ).stdout

    def write(self, name, content):
        with open(os.path.join(self.path, name), "w", encoding="utf-8") as stream:
            stream.write(content)

    def test_revision_forms_and_history_limit(self):
        for revision in ("HEAD", "main", "v1", self.git("rev-parse", "HEAD").strip()):
            repository = Repository(self.path, revision)
            commits = [row for row in repository.history(10) if row.kind == "commit"]
            self.assertEqual(len(commits), 10)
            self.assertEqual(commits[0].subject, "commit 11")
        older = next(
            row
            for row in Repository(self.path, "older").history(1)
            if row.kind == "commit"
        )
        self.assertEqual(older.subject, "commit 6")

    def test_selection_rows_and_single_file_diffs(self):
        self.write("staged.txt", "staged\n")
        self.git("add", "staged.txt")
        self.write("unstaged.txt", "unstaged\n")
        repository = Repository(self.path)
        rows = repository.history(10)
        self.assertEqual([row.kind for row in rows[:2]], ["unstaged", "staged"])
        unstaged, staged = rows[:2]
        self.assertEqual(
            [item.path for item in repository.changed_files(unstaged)], ["unstaged.txt"]
        )
        self.assertEqual([item.path for item in repository.changed_files(staged)], ["staged.txt"])
        self.assertIn(
            "+unstaged", repository.diff(unstaged, repository.changed_files(unstaged)[0])
        )

    def test_whitespace_is_ignored_by_default(self):
        self.write("history.txt", "commit        11\n")
        repository = Repository(self.path)
        self.assertEqual(repository.history(10)[0].kind, "commit")
        unstaged = repository.history(10, ignore_whitespace=False)[0]
        self.assertEqual(unstaged.kind, "unstaged")
        regular = repository.changed_files(unstaged, ignore_whitespace=False)
        self.assertEqual([item.path for item in regular], ["history.txt"])
        self.assertIn("commit        11", repository.diff(unstaged, regular[0], False))

    def test_commit_file_list_and_diff(self):
        repository = Repository(self.path, "HEAD")
        commit = next(row for row in repository.history(1) if row.kind == "commit")
        files = repository.changed_files(commit)
        self.assertEqual([item.path for item in files], ["history.txt"])
        self.assertIn("+commit 11", repository.diff(commit, files[0]))

    def test_full_file_context_and_size(self):
        self.write("context.txt", "".join(f"line {number}\n" for number in range(20)))
        self.git("add", "context.txt")
        self.git("commit", "-m", "add context fixture")
        self.write(
            "context.txt",
            "changed\n" + "".join(f"line {number}\n" for number in range(1, 20)),
        )
        repository = Repository(self.path)
        unstaged = repository.history(1)[0]
        changed_file = repository.changed_files(unstaged)[0]
        self.assertNotIn("line 19", repository.diff(unstaged, changed_file))
        self.assertIn("line 19", repository.diff(unstaged, changed_file, full_file=True))
        self.assertLess(repository.file_size(unstaged, changed_file), FULL_FILE_LIMIT)

    def test_limited_git_output(self):
        repository = Repository(self.path)
        output, truncated = repository.run_limited("show", "HEAD:history.txt", limit=5)
        self.assertEqual(output, "commi")
        self.assertTrue(truncated)
        self.assertIn("Diff truncated", repository.finish_diff(output, truncated))


if __name__ == "__main__":
    unittest.main()
