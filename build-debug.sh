#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
PRIVATE_PC="$ROOT/.deps/gtk3/usr/lib/x86_64-linux-gnu/pkgconfig"
if [ -d "$PRIVATE_PC" ]; then
    export PKG_CONFIG_PATH="$PRIVATE_PC${PKG_CONFIG_PATH:+:$PKG_CONFIG_PATH}"
fi

mkdir -p "$ROOT/bin"
go build -gcflags='giti=-N -l' -o "$ROOT/bin/giti-app-debug" "$ROOT"
go build -gcflags='giti/cmd/giti-launcher=-N -l' -o "$ROOT/bin/giti-launcher-debug" "$ROOT/cmd/giti-launcher"
