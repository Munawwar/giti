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

## Requirements

- Python 3
- PyGObject with GTK 3
- Git

These are already installed on the target Ubuntu system.
