#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
pkg-config --atleast-version=3.24 gtk+-3.0 || { echo "GTK 3.24 or newer development files are required." >&2; exit 1; }

mkdir -p "$ROOT/bin"
echo "Building Giti with debug symbols (the first GTK build may take a few minutes)..."
go build -v -buildvcs=false -gcflags='giti=-N -l' -o "$ROOT/bin/giti-debug" "$ROOT"
rm -f "$ROOT/bin/giti-app-debug" "$ROOT/bin/giti-launcher-debug"
