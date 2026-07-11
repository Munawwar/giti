# GitSkim

A deliberately small, fast GTK 3 history and single-file diff viewer.

```sh
./gitskim
./gitskim HEAD
./gitskim main
./gitskim v1.0.0
./gitskim abc123
```

The initial history contains unstaged and staged entries when present, followed by ten commits. Select a history entry, then a changed file, to view only that file's stacked diff. Whitespace changes are ignored by default; enable **Show whitespace changes** to use Git's regular diff.

The application opens maximized by default. Use **Load 100 more** when the initial ten-commit history is not enough.

Enable **Show full file** to expand the selected diff with the unchanged surrounding lines. The option resets when the selection changes and is disabled for files larger than 2 MiB to avoid accidentally rendering an enormous document.

Rendered patches are capped at 8 MiB. GitSkim terminates larger diff output and shows a truncation notice instead of retaining an unbounded patch in memory. Hiding the resident window also releases its history, file list, and diff contents.

Closing the first window hides one resident process for up to 12 hours. A later invocation reuses that warm process; when its window is already visible, GitSkim opens a separate cold process that exits normally when closed. There is never a pool of dormant processes.

## Requirements

- Python 3
- PyGObject with GTK 3
- Git

These are already installed on the target Ubuntu system.
