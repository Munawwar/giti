#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
PRIVATE_PC="$ROOT/.deps/gtk3/usr/lib/x86_64-linux-gnu/pkgconfig"
if ! pkg-config --atleast-version=3.24 gtk+-3.0 2>/dev/null && [ -d "$PRIVATE_PC" ]; then
    export PKG_CONFIG_PATH="$PRIVATE_PC${PKG_CONFIG_PATH:+:$PKG_CONFIG_PATH}"
fi
pkg-config --atleast-version=3.24 gtk+-3.0 || { echo "GTK 3.24 or newer development files are required." >&2; exit 1; }

GITI_GTK_TEST=1 go test ./...
GDK_DPI_SCALE=2 GITI_GTK_SCALE_TEST=1 go test -run '^TestGTKGraphTextScaling$' .
