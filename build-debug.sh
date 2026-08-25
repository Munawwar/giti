#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
pkg-config --atleast-version=3.24 gtk+-3.0 || { echo "GTK 3.24 or newer development files are required." >&2; exit 1; }

mkdir -p "$ROOT/bin"
go build -buildvcs=false -gcflags='giti=-N -l' -o "$ROOT/bin/giti-debug" "$ROOT"
rm -f "$ROOT/bin/giti-app-debug" "$ROOT/bin/giti-launcher-debug"
