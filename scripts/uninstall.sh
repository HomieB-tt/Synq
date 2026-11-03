#!/usr/bin/env bash
set -euo pipefail

# uninstall.sh - Uninstalls Synq CLI binary, desktop launchers, and icons

PREFIX="${PREFIX:-$HOME/.local}"
BINDIR="$PREFIX/bin"
APPDIR="$PREFIX/share/applications"
ICONDIR="$PREFIX/share/icons/hicolor"

OS="$(uname -s)"

echo "Uninstalling Synq..."

if [ "$OS" = "Darwin" ]; then
    # Remove macOS files
    rm -f "/usr/local/bin/synq"
    rm -f "$BINDIR/synq"
    rm -rf "$HOME/Applications/Synq.app"
    rm -rf "/Applications/Synq.app"
    echo "Removed Synq binary and Synq.app."
else
    # Remove Linux files
    rm -f "$BINDIR/synq"
    rm -f "$BINDIR/synq-desktop"
    rm -f "$APPDIR/synq.desktop"
    rm -f "$ICONDIR/256x256/apps/synq.png"
    rm -f "$ICONDIR/scalable/apps/synq.svg"

    # Update desktop and icon caches if tools are available
    command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database "$APPDIR" || true
    command -v gtk-update-icon-cache >/dev/null 2>&1 && gtk-update-icon-cache -f -t "$ICONDIR" || true

    echo "Removed Synq binaries, desktop entry, and icons."
fi

# Check for --purge flag to delete data store
PURGE=false
for arg in "$@"; do
    if [ "$arg" = "--purge" ]; then
        PURGE=true
    fi
done

DATA_DIR="$HOME/.local/share/synq"
if [ "$OS" = "Darwin" ]; then
    DATA_DIR="$HOME/Library/Application Support/synq"
fi

if [ "$PURGE" = "true" ]; then
    if [ -d "$DATA_DIR" ]; then
        rm -rf "$DATA_DIR"
        echo "Purged Synq data directory ($DATA_DIR)."
    fi
else
    if [ -d "$DATA_DIR" ]; then
        echo ""
        echo "Note: Your account keys and data in $DATA_DIR were preserved."
        echo "To delete your data as well, run: $0 --purge"
    fi
fi

echo ""
echo "Synq has been successfully uninstalled."
