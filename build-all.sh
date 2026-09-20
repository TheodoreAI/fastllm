#!/usr/bin/env bash
# macOS/Linux equivalent of build-all.bat — builds the terminal UI (cmd/cli)
# and the headless API server (cmd/server), and refreshes the copies on PATH.
# See start.sh for the staleness-aware version; this script always does a full
# rebuild.
set -u

APPDIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$APPDIR"

echo "Building fastllm-cli (terminal UI)..."
go build -o fastllm-cli ./cmd/cli
if [ $? -ne 0 ]; then
    echo "cmd/cli build failed. See output above."
    exit 1
fi

echo "Building fastllm (headless API server)..."
go build -o fastllm ./cmd/server
if [ $? -ne 0 ]; then
    echo "cmd/server build failed. See output above."
    exit 1
fi

# Keep the copies on PATH in step with the repo build. Without this the two
# drift: start.sh runs the repo binary while typing `fastllm` runs the one in
# ~/.local/bin, and a stale install looks like a missing feature.
if [ -x "$HOME/.local/bin/fastllm" ]; then
    echo "Refreshing installed copy in ~/.local/bin ..."
    go build -o "$HOME/.local/bin/fastllm" ./cmd/server
fi
if [ -x "$HOME/.local/bin/fastllm-cli" ]; then
    go build -o "$HOME/.local/bin/fastllm-cli" ./cmd/cli
fi

echo
echo "Done:"
echo "  $APPDIR/fastllm-cli   (terminal UI)"
echo "  $APPDIR/fastllm       (headless API server)"
