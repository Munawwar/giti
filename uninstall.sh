#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
PREFIX=${HOME}/.local
USER_INSTALL=true

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
        -h|--help)
            cat <<'EOF'
Usage: ./uninstall.sh [--user] [--system] [--prefix PATH]

Removes the Giti binary, desktop entry, icons, and shell completions.
System libraries installed as dependencies are left in place.
EOF
            exit 0
            ;;
        *) echo "unknown option: $1" >&2; exit 2 ;;
    esac
    shift
done

. "$ROOT/data/install-paths.sh"

SUDO=
if [ "$USER_INSTALL" = false ] && [ -e "$PREFIX" ] && [ ! -w "$PREFIX" ]; then
    command -v sudo >/dev/null || { echo "sudo is needed to uninstall from $PREFIX." >&2; exit 1; }
    SUDO=sudo
fi

$SUDO rm -f \
    "$PREFIX/bin/giti" \
    "$PREFIX/bin/giti-app" \
    "$DATA_HOME/applications/$APP_ID.desktop" \
    "$DATA_HOME/icons/hicolor/scalable/apps/$APP_ID.svg" \
    "$DATA_HOME/icons/hicolor/256x256/apps/$APP_ID.png" \
    "$BASH_COMPLETION_HOME/giti" \
    "$ZSH_COMPLETION_HOME/_giti" \
    "$FISH_COMPLETION_HOME/giti.fish"

if command -v update-desktop-database >/dev/null && [ -d "$DATA_HOME/applications" ]; then
    $SUDO update-desktop-database "$DATA_HOME/applications" || true
fi
if command -v gtk-update-icon-cache >/dev/null && [ -d "$DATA_HOME/icons/hicolor" ]; then
    $SUDO gtk-update-icon-cache -t -f "$DATA_HOME/icons/hicolor" || true
fi

echo "Uninstalled Giti. System libraries and user preferences were left in place."
