#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
pkg-config --atleast-version=3.24 gtk+-3.0 || { echo "GTK 3.24 or newer development files are required." >&2; exit 1; }

# Keep test windows off the desktop on a virtual display; GITI_TEST_VISIBLE=1 shows them.
if [ -z "${GITI_TEST_VISIBLE:-}" ] && command -v xvfb-run >/dev/null; then
	unset WAYLAND_DISPLAY
	export GDK_BACKEND=x11
	set -- xvfb-run -a
fi

GITI_GTK_TEST=1 "$@" go test ./...
GDK_DPI_SCALE=2 GITI_GTK_SCALE_TEST=1 "$@" go test -run '^TestGTKGraphTextScaling$' .
