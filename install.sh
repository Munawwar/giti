#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
PREFIX=${HOME}/.local
USER_INSTALL=true
SKIP_DEPS=false
BUILD_FRESH=ask

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
        --build) BUILD_FRESH=true ;;
        --prebuilt) BUILD_FRESH=false ;;
        --skip-deps) SKIP_DEPS=true ;;
        -h|--help)
            cat <<'EOF'
Usage: ./install.sh [--user] [--system] [--prefix PATH] [--build|--prebuilt] [--skip-deps]

Builds and installs Giti, its desktop entry, and its hicolor icon.
  --user       install in ~/.local (the default)
  --system     install under /usr/local
  --prefix     install under PATH and PATH/share
  --build      build fresh from source
  --prebuilt   install the included Linux x86_64 release binary
  --skip-deps  do not install missing Ubuntu build dependencies
EOF
            exit 0
            ;;
        *) echo "unknown option: $1" >&2; exit 2 ;;
    esac
    shift
done

. "$ROOT/data/install-paths.sh"

if [ "$BUILD_FRESH" = ask ]; then
    if [ -t 0 ]; then
        printf 'Build fresh from source? [y/N] '
        read -r answer || answer=
        case "$answer" in [Yy]|[Yy][Ee][Ss]) BUILD_FRESH=true ;; *) BUILD_FRESH=false ;; esac
    else
        BUILD_FRESH=false
    fi
fi

GITI_BIN=$ROOT/bin/giti
if [ "$BUILD_FRESH" = false ]; then
    test "$(uname -s)" = Linux && test "$(uname -m)" = x86_64 || { echo "The included binary is Linux x86_64; rerun with --build on this platform." >&2; exit 1; }
    test -x "$GITI_BIN" || { echo "Included binary is missing; rerun with --build." >&2; exit 1; }
fi

if [ "$SKIP_DEPS" = false ] && [ "$BUILD_FRESH" = true ] && { ! command -v gcc >/dev/null || ! command -v git >/dev/null || ! command -v pkg-config >/dev/null || ! pkg-config --exists gtk+-3.0; }; then
    command -v apt-get >/dev/null || { echo "Install gcc, git, pkg-config, and GTK 3 development files, then rerun." >&2; exit 1; }
    SUDO=
    if [ "$(id -u)" -ne 0 ]; then
        command -v sudo >/dev/null || { echo "sudo is needed to install missing system dependencies." >&2; exit 1; }
        SUDO=sudo
    fi
    $SUDO apt-get update
    $SUDO apt-get install -y build-essential pkg-config libgtk-3-dev git
fi

if [ "$SKIP_DEPS" = false ] && [ "$BUILD_FRESH" = false ] && { ! command -v git >/dev/null || ! ldd "$GITI_BIN" 2>/dev/null | grep -q 'libgtk-3.so.0 => /'; }; then
    command -v apt-get >/dev/null || { echo "Install Git and GTK 3 runtime libraries, then rerun." >&2; exit 1; }
    SUDO=
    if [ "$(id -u)" -ne 0 ]; then
        command -v sudo >/dev/null || { echo "sudo is needed to install missing runtime dependencies." >&2; exit 1; }
        SUDO=sudo
    fi
    $SUDO apt-get update
    $SUDO apt-get install -y libgtk-3-0 git
fi

if [ "$BUILD_FRESH" = true ]; then
    command -v go >/dev/null || { echo "Go 1.24 or newer is required; see https://go.dev/doc/install" >&2; exit 1; }
    go version | awk '{sub(/^go/, "", $3); split($3, version, "."); exit !(version[1] > 1 || version[1] == 1 && version[2] >= 24)}' || {
        echo "Go 1.24 or newer is required; see https://go.dev/doc/install" >&2
        exit 1
    }
    "$ROOT/build.sh"
fi

SUDO=
if [ "$USER_INSTALL" = false ] && [ ! -w "$PREFIX" ]; then
    command -v sudo >/dev/null || { echo "sudo is needed to install under $PREFIX." >&2; exit 1; }
    SUDO=sudo
fi

$SUDO install -d "$PREFIX/bin" "$DATA_HOME/applications" "$DATA_HOME/icons/hicolor/scalable/apps" "$DATA_HOME/icons/hicolor/256x256/apps" \
    "$BASH_COMPLETION_HOME" "$ZSH_COMPLETION_HOME" "$FISH_COMPLETION_HOME"
if [ -L "$PREFIX/bin/giti" ]; then
    $SUDO rm "$PREFIX/bin/giti"
fi
$SUDO rm -f "$PREFIX/bin/giti-app"
$SUDO install -m755 "$GITI_BIN" "$PREFIX/bin/giti"
$SUDO install -m644 "$ROOT/logo/giti-logo.svg" "$DATA_HOME/icons/hicolor/scalable/apps/$APP_ID.svg"
$SUDO install -m644 "$ROOT/logo/giti-logo.png" "$DATA_HOME/icons/hicolor/256x256/apps/$APP_ID.png"
sed "s|^Exec=.*|Exec=$PREFIX/bin/giti|" "$ROOT/data/$APP_ID.desktop" | $SUDO tee "$DATA_HOME/applications/$APP_ID.desktop" >/dev/null
$SUDO install -m644 "$ROOT/completions/giti.bash" "$BASH_COMPLETION_HOME/giti"
$SUDO install -m644 "$ROOT/completions/_giti" "$ZSH_COMPLETION_HOME/_giti"
$SUDO install -m644 "$ROOT/completions/giti.fish" "$FISH_COMPLETION_HOME/giti.fish"

if command -v update-desktop-database >/dev/null; then
    $SUDO update-desktop-database "$DATA_HOME/applications" || true
fi
if command -v gtk-update-icon-cache >/dev/null; then
    $SUDO gtk-update-icon-cache -t -f "$DATA_HOME/icons/hicolor" || true
fi

echo "Installed Giti. Open it from the app launcher or run $PREFIX/bin/giti. Shell completions are installed for Bash, Zsh, and Fish."
