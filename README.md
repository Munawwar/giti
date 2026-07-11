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

## Ubuntu installation

Install the native build dependencies:

```sh
sudo apt update
sudo apt install build-essential pkg-config libgtk-3-dev git
```

GitSkim also needs Go 1.24 or newer to build. Check with `go version`; if the
installed version is older, install a current release using the official
[Go installation instructions](https://go.dev/doc/install).

Build, test, and install both binaries together:

```sh
./build.sh
./test.sh
sudo install -m755 bin/gitskim-app /usr/local/bin/gitskim-app
sudo install -m755 bin/gitskim-launcher /usr/local/bin/gitskim
```

The test command includes a live GTK smoke test and therefore needs a graphical
display. You can then run `gitskim`, `gitskim main`, `gitskim v1.0.0`, or
`gitskim <sha>` from any repository.

If GTK 3 is installed at runtime but its development package is unavailable and
you cannot use `sudo`, `./bootstrap-dev-deps.sh` downloads the needed Ubuntu
development packages into the ignored `.deps` directory. The build scripts use
that private copy automatically. It can be deleted after installing
`libgtk-3-dev` system-wide.

## Debugging a crash

The debug build retains symbols and disables compiler optimization:

```sh
./build-debug.sh
GOTRACEBACK=crash bin/gitskim-launcher-debug HEAD 2>&1 | tee /tmp/gitskim-crash.log
```

Reproduce the crash, then keep `/tmp/gitskim-crash.log`. If Ubuntu reports a
core dump, `coredumpctl info gitskim-app-debug` shows its metadata and
`coredumpctl debug gitskim-app-debug` opens it in GDB. The release and debug
launchers use separate app binaries, but they intentionally share the resident
socket; close any running GitSkim process before starting a debug session.
