#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
if pkg-config --exists gtk+-3.0; then
    echo "System GTK 3 development files are already available."
    exit 0
fi

DEPS="$ROOT/.deps"
mkdir -p "$DEPS/debs" "$DEPS/gtk3"
cd "$DEPS/debs"
apt-get download \
    libgtk-3-dev \
    libatk-bridge2.0-dev \
    libatk1.0-dev \
    libatspi2.0-dev \
    libdbus-1-dev

for package in ./*.deb; do
    dpkg-deb -x "$package" "$DEPS/gtk3"
done

prefix="$DEPS/gtk3/usr"
for library in "$prefix/lib/x86_64-linux-gnu"/*.so; do
    if [ -L "$library" ] && [ ! -e "$library" ]; then
        target=$(readlink "$library")
        ln -sfn "/usr/lib/x86_64-linux-gnu/$target" "$library"
    fi
done
for pc in "$prefix/lib/x86_64-linux-gnu/pkgconfig"/*.pc; do
    sed -i "s|^prefix=/usr$|prefix=$prefix|" "$pc"
done

PKG_CONFIG_PATH="$prefix/lib/x86_64-linux-gnu/pkgconfig" pkg-config --modversion gtk+-3.0
