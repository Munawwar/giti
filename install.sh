#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
APP_ID=io.github.Munawwar.Giti
PREFIX=${HOME}/.local
USER_INSTALL=true
SKIP_DEPS=false

while [ "$#" -gt 0 ]; do
    case "$1" in
        --user)
            PREFIX=${HOME}/.local
            USER_INSTALL=true
            ;;
        --system)
            PREFIX=/usr/local
            USER_INSTALL=false
            ;;
        --prefix)
            shift
            test "$#" -gt 0 || { echo "--prefix needs a path" >&2; exit 2; }
            PREFIX=$1
            USER_INSTALL=false
            ;;
        --skip-deps) SKIP_DEPS=true ;;
        -h|--help)
            cat <<'EOF'
Usage: ./install.sh [--user] [--system] [--prefix PATH] [--skip-deps]

Builds and installs Giti, its desktop entry, and its hicolor icon.
  --user       install in ~/.local (the default)
  --system     install under /usr/local
  --prefix     install under PATH and PATH/share
  --skip-deps  do not install missing Ubuntu build dependencies
EOF
            exit 0
            ;;
        *) echo "unknown option: $1" >&2; exit 2 ;;
    esac
    shift
done

if [ "$USER_INSTALL" = true ]; then
    DATA_HOME=${XDG_DATA_HOME:-${HOME}/.local/share}
else
    DATA_HOME=$PREFIX/share
fi

if [ "$SKIP_DEPS" = false ] && { ! command -v gcc >/dev/null || ! command -v git >/dev/null || ! command -v pkg-config >/dev/null || ! pkg-config --exists gtk+-3.0; }; then
    command -v apt-get >/dev/null || { echo "Install gcc, git, pkg-config, and GTK 3 development files, then rerun." >&2; exit 1; }
    SUDO=
    if [ "$(id -u)" -ne 0 ]; then
        command -v sudo >/dev/null || { echo "sudo is needed to install missing system dependencies." >&2; exit 1; }
        SUDO=sudo
    fi
    $SUDO apt-get update
    $SUDO apt-get install -y build-essential pkg-config libgtk-3-dev git
fi

command -v go >/dev/null || { echo "Go 1.24 or newer is required; see https://go.dev/doc/install" >&2; exit 1; }
go version | awk '{sub(/^go/, "", $3); split($3, version, "."); exit !(version[1] > 1 || version[1] == 1 && version[2] >= 24)}' || {
    echo "Go 1.24 or newer is required; see https://go.dev/doc/install" >&2
    exit 1
}

"$ROOT/build.sh"

SUDO=
if [ "$USER_INSTALL" = false ] && [ ! -w "$PREFIX" ]; then
    command -v sudo >/dev/null || { echo "sudo is needed to install under $PREFIX." >&2; exit 1; }
    SUDO=sudo
fi

$SUDO install -d "$PREFIX/bin" "$DATA_HOME/applications" "$DATA_HOME/icons/hicolor/scalable/apps" "$DATA_HOME/icons/hicolor/256x256/apps"
if [ -L "$PREFIX/bin/giti" ]; then
    $SUDO rm "$PREFIX/bin/giti"
fi
if [ -L "$PREFIX/bin/giti-app" ]; then
    $SUDO rm "$PREFIX/bin/giti-app"
fi
$SUDO install -m755 "$ROOT/bin/giti-launcher" "$PREFIX/bin/giti"
$SUDO install -m755 "$ROOT/bin/giti-app" "$PREFIX/bin/giti-app"
$SUDO install -m644 "$ROOT/logo/giti-logo.svg" "$DATA_HOME/icons/hicolor/scalable/apps/$APP_ID.svg"
$SUDO install -m644 "$ROOT/logo/giti-logo.png" "$DATA_HOME/icons/hicolor/256x256/apps/$APP_ID.png"
sed "s|^Exec=.*|Exec=$PREFIX/bin/giti|" "$ROOT/data/$APP_ID.desktop" | $SUDO tee "$DATA_HOME/applications/$APP_ID.desktop" >/dev/null

if command -v update-desktop-database >/dev/null; then
    $SUDO update-desktop-database "$DATA_HOME/applications" || true
fi
if command -v gtk-update-icon-cache >/dev/null; then
    $SUDO gtk-update-icon-cache -t -f "$DATA_HOME/icons/hicolor" || true
fi

echo "Installed Giti. Open it from the app launcher or run $PREFIX/bin/giti."
