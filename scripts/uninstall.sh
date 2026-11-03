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

# --purge deletes your identity keys and settings: there is no undo, so it
# asks first (pass --yes to skip the question, e.g. in a script).
PURGE=false
ASSUME_YES=false
for arg in "$@"; do
    case "$arg" in
        --purge) PURGE=true ;;
        --yes|-y) ASSUME_YES=true ;;
    esac
done

# Where Synq keeps its identity and preferences: Go's os.UserConfigDir()
# plus "synq". The received-file cache lives under os.UserCacheDir().
if [ "$OS" = "Darwin" ]; then
    DATA_DIR="$HOME/Library/Application Support/synq"
    CACHE_DIR="$HOME/Library/Caches/synq"
else
    DATA_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/synq"
    CACHE_DIR="${XDG_CACHE_HOME:-$HOME/.cache}/synq"
fi

if [ "$PURGE" = "true" ]; then
    if [ -d "$DATA_DIR" ]; then
        if [ "$ASSUME_YES" != "true" ]; then
            if [ ! -t 0 ]; then
                echo "Refusing to purge without a terminal to confirm on. Re-run with --purge --yes." >&2
                exit 1
            fi
            echo ""
            echo "This will permanently delete your Synq identity keys and settings in:"
            echo "  $DATA_DIR"
            echo "Without the keys (and your passphrase) your account cannot be recovered."
            printf 'Type "delete" to continue: '
            read -r answer
            if [ "$answer" != "delete" ]; then
                echo "Cancelled. Your data was not touched."
                exit 1
            fi
        fi
        rm -rf "$DATA_DIR"
        echo "Purged Synq data directory ($DATA_DIR)."
    fi
    rm -rf "$CACHE_DIR"
else
    if [ -d "$DATA_DIR" ]; then
        echo ""
        echo "Note: Your account keys and data in $DATA_DIR were preserved."
        echo "To delete your data as well, run: $0 --purge"
    fi
fi

echo ""
echo "Synq has been successfully uninstalled."
