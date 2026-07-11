import os
import subprocess
import tempfile
import unittest

from gitskim import Repository


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

    def test_selection_rows_and_single_file_diffs(self):
        self.write("staged.txt", "staged\n")
        self.git("add", "staged.txt")
        self.write("unstaged.txt", "unstaged\n")
        repository = Repository(self.path)
        rows = repository.history(10)
        self.assertEqual([row.kind for row in rows[:2]], ["unstaged", "staged"])
        unstaged, staged = rows[:2]
        self.assertEqual([item.path for item in repository.changed_files(unstaged)], ["unstaged.txt"])
        self.assertEqual([item.path for item in repository.changed_files(staged)], ["staged.txt"])
        self.assertIn("+unstaged", repository.diff(unstaged, repository.changed_files(unstaged)[0]))

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


if __name__ == "__main__":
    unittest.main()
