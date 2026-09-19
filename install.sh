#!/usr/bin/env bash
set -e

# fastllm Linux/macOS Installer
# Run with:
#   curl -fsSL https://raw.githubusercontent.com/theodoreai/fastllm/main/install.sh | bash

INSTALL_DIR="${HOME}/.local/bin"
mkdir -p "$INSTALL_DIR"

TARGET_BIN="${INSTALL_DIR}/fastllm"

echo "Installing fastllm to $INSTALL_DIR..."

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ -f "${SCRIPT_DIR}/go.mod" ]; then
    echo "Building from local source..."
    (cd "$SCRIPT_DIR" && go build -o "$TARGET_BIN" ./cmd/server)
elif command -v go >/dev/null 2>&1; then
    echo "Compiling via Go..."
    TEMP_DIR=$(mktemp -d)
    git clone --depth 1 https://github.com/theodoreai/fastllm.git "$TEMP_DIR"
    (cd "$TEMP_DIR" && go build -o "$TARGET_BIN" ./cmd/server)
    rm -rf "$TEMP_DIR"
else
    OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
    ARCH="$(uname -m)"
    case "$ARCH" in
        x86_64) ARCH="amd64" ;;
        aarch64|arm64) ARCH="arm64" ;;
    esac

    URL="https://github.com/theodoreai/fastllm/releases/latest/download/fastllm-${OS}-${ARCH}"
    echo "Downloading prebuilt binary from $URL..."
    curl -fsSL "$URL" -o "$TARGET_BIN"
fi

chmod +x "$TARGET_BIN"

# Ensure INSTALL_DIR is in PATH
if [[ ":$PATH:" != *":$INSTALL_DIR:"* ]]; then
    SHELL_PROFILE=""
    if [ -f "$HOME/.bashrc" ]; then
        SHELL_PROFILE="$HOME/.bashrc"
    elif [ -f "$HOME/.zshrc" ]; then
        SHELL_PROFILE="$HOME/.zshrc"
    elif [ -f "$HOME/.profile" ]; then
        SHELL_PROFILE="$HOME/.profile"
    fi

    if [ -n "$SHELL_PROFILE" ]; then
        echo "export PATH=\"\$HOME/.local/bin:\$PATH\"" >> "$SHELL_PROFILE"
        echo "Added $INSTALL_DIR to PATH in $SHELL_PROFILE"
    fi
fi

echo ""
echo "fastllm installed successfully!"
echo "Run 'fastllm' to launch the interactive REPL."
