#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
PRIVATE_PC="$ROOT/.deps/gtk3/usr/lib/x86_64-linux-gnu/pkgconfig"
if ! pkg-config --atleast-version=3.24 gtk+-3.0 2>/dev/null && [ -d "$PRIVATE_PC" ]; then
    export PKG_CONFIG_PATH="$PRIVATE_PC${PKG_CONFIG_PATH:+:$PKG_CONFIG_PATH}"
fi
pkg-config --atleast-version=3.24 gtk+-3.0 || { echo "GTK 3.24 or newer development files are required." >&2; exit 1; }

mkdir -p "$ROOT/bin"
go build -buildvcs=false -gcflags='giti=-N -l' -o "$ROOT/bin/giti-debug" "$ROOT"
rm -f "$ROOT/bin/giti-app-debug" "$ROOT/bin/giti-launcher-debug"
