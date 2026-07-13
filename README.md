# Giti - a `gitk` alternative

A git history diff viewer for linux meant to be invoked quickly from a terminal (with window maximized), for the hybrid TUI-GUI workflow loving devs.

Keeping bloat low and starting up quickly were important design choices for repeat invokes from terminal. giti cold starts in ~200ms and warm starts in <100ms.

`giti` coincidentally 
means world/universe and life in Persian.

```sh
./giti
./giti HEAD
./giti main
./giti v1.0.0
./giti abc123
./giti -1
```

## Ubuntu installation

Install the native build dependencies:

```sh
sudo apt update
sudo apt install build-essential pkg-config libgtk-3-dev git
```

Giti also needs Go 1.24 or newer to build. Check with `go version`; if the
installed version is older, install a current release using the official
[Go installation instructions](https://go.dev/doc/install).

Build, test, and install both binaries together:

```sh
./build.sh
./test.sh
sudo install -m755 bin/giti-app /usr/local/bin/giti-app
sudo install -m755 bin/giti-launcher /usr/local/bin/giti
```

The test command includes a live GTK smoke test and therefore needs a graphical
display. You can then run `giti`, `giti main`, `giti v1.0.0`, or
`giti <sha>` from any repository.

If GTK 3 is installed at runtime but its development package is unavailable and
you cannot use `sudo`, `./bootstrap-dev-deps.sh` downloads the needed Ubuntu
development packages into the ignored `.deps` directory. The build scripts use
that private copy automatically. It can be deleted after installing
`libgtk-3-dev` system-wide.

## Features / Deets

The initial history contains unstaged and staged entries when present, followed by ten commits. Select a history entry, then a changed file, to view only that file's stacked diff. Whitespace changes are ignored by default; enable **Show whitespace changes** to use Git's regular diff.

For performance only a few commits are shown in graph initially. Use **Load more** when the initial ten-commit history is not enough.

Enable **Show full file** to expand the selected diff with the unchanged surrounding lines. The option resets when the selection changes and is disabled for files larger than 2 MiB to avoid accidentally rendering an enormous document.

Rendered patches are capped at 8 MiB. Giti terminates larger diff output and shows a truncation notice instead of retaining an unbounded patch in memory. Hiding the resident window also releases its history, file list, and diff contents.

### Cold start improvement with a background process

The first normal invocation starts the sole resident as a detached background
process, so its terminal returns immediately. Closing its window hides the
resident for up to 12 hours, and a later invocation reuses it. Invoking Giti
while its window is already visible does not create another process. `giti -1`
terminates the recorded resident and starts a fresh one. A process lock enforces
the single-resident rule even when launch commands race. Resident output is
written to `$XDG_RUNTIME_DIR/giti.log` (or `/tmp/giti-$UID/giti.log` when that
variable is unset).

## Debugging a crash

The debug build retains symbols and disables compiler optimization:

```sh
./build-debug.sh
GOTRACEBACK=crash bin/giti-launcher-debug HEAD 2>&1 | tee /tmp/giti-crash.log
```

Reproduce the crash, then keep `/tmp/giti-crash.log`. If Ubuntu reports a
core dump, `coredumpctl info giti-app-debug` shows its metadata and
`coredumpctl debug giti-app-debug` opens it in GDB. The release and debug
launchers use separate app binaries, but they intentionally share the resident
socket; close any running Giti process before starting a debug session.
