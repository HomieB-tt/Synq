#!/usr/bin/env bash
set -euo pipefail

# install.sh - Installs Synq CLI binary and Desktop launcher for the current user

PREFIX="${PREFIX:-$HOME/.local}"
BINDIR="$PREFIX/bin"
APPDIR="$PREFIX/share/applications"
ICONDIR="$PREFIX/share/icons/hicolor"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." 2>/dev/null || pwd)"

# Detect base directory with files
if [ -f "$SCRIPT_DIR/synq" ]; then
    BASE_DIR="$SCRIPT_DIR"
elif [ -f "$ROOT_DIR/synq" ]; then
    BASE_DIR="$ROOT_DIR"
elif [ -f "./synq" ]; then
    BASE_DIR="."
else
    echo "Error: synq binary not found. Please build synq or run install.sh from the release directory." >&2
    exit 1
fi

OS="$(uname -s)"

if [ "$OS" = "Darwin" ]; then
    echo "Installing Synq for macOS..."

    # Install CLI binary
    TARGET_BIN="/usr/local/bin/synq"
    if [ -w "/usr/local/bin" ]; then
        install -m 755 "$BASE_DIR/synq" "$TARGET_BIN"
    else
        mkdir -p "$BINDIR"
        install -m 755 "$BASE_DIR/synq" "$BINDIR/synq"
        TARGET_BIN="$BINDIR/synq"
    fi

    # Install Synq.app to ~/Applications or /Applications
    MACOS_APP_DIR="$HOME/Applications/Synq.app"
    mkdir -p "$HOME/Applications"
    rm -rf "$MACOS_APP_DIR"
    mkdir -p "$MACOS_APP_DIR/Contents/MacOS" "$MACOS_APP_DIR/Contents/Resources"

    if [ -f "$BASE_DIR/assets/icon/synq.icns" ]; then
        cp "$BASE_DIR/assets/icon/synq.icns" "$MACOS_APP_DIR/Contents/Resources/synq.icns"
    fi
    if [ -f "$BASE_DIR/assets/macos/Info.plist" ]; then
        cp "$BASE_DIR/assets/macos/Info.plist" "$MACOS_APP_DIR/Contents/Info.plist"
    fi
    printf 'APPL????' > "$MACOS_APP_DIR/Contents/PkgInfo"
    if [ -f "$BASE_DIR/assets/macos/synq-launcher" ]; then
        cp "$BASE_DIR/assets/macos/synq-launcher" "$MACOS_APP_DIR/Contents/MacOS/synq-launcher"
        chmod +x "$MACOS_APP_DIR/Contents/MacOS/synq-launcher"
    fi
    cp "$BASE_DIR/synq" "$MACOS_APP_DIR/Contents/MacOS/synq"
    chmod +x "$MACOS_APP_DIR/Contents/MacOS/synq"

    echo ""
    echo "Installation complete!"
    echo "  - CLI: $TARGET_BIN"
    echo "  - App: $MACOS_APP_DIR (Available in Spotlight / Launchpad)"
else
    # Linux / BSD
    echo "Installing Synq for Linux..."

    mkdir -p "$BINDIR" "$APPDIR" "$ICONDIR/256x256/apps" "$ICONDIR/scalable/apps"

    install -m 755 "$BASE_DIR/synq" "$BINDIR/synq"

    if [ -f "$BASE_DIR/assets/icon/synq-256.png" ]; then
        install -m 644 "$BASE_DIR/assets/icon/synq-256.png" "$ICONDIR/256x256/apps/synq.png"
    fi

    if [ -f "$BASE_DIR/assets/icon/synq.svg" ]; then
        install -m 644 "$BASE_DIR/assets/icon/synq.svg" "$ICONDIR/scalable/apps/synq.svg"
    fi

    if [ -f "$BASE_DIR/assets/icon/synq.desktop" ]; then
        install -m 644 "$BASE_DIR/assets/icon/synq.desktop" "$APPDIR/synq.desktop"
    fi

    command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database "$APPDIR" || true
    command -v gtk-update-icon-cache >/dev/null 2>&1 && gtk-update-icon-cache -f -t "$ICONDIR" || true

    echo ""
    echo "Installation complete!"
    echo "  - CLI: $BINDIR/synq"
    if [[ ":$PATH:" != *":$BINDIR:"* ]]; then
        echo "    (Note: Add $BINDIR to your PATH if it's not already included)"
    fi
    echo "  - Desktop: Synq is now available in your application menu with icon."
fi
