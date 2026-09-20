#!/usr/bin/env bash
# macOS/Linux equivalent of build-all.bat — builds fastllm and refreshes the
# copy on PATH. One binary does both jobs: no arguments launches the terminal
# UI, the "server" subcommand runs the headless API. See start.sh for the
# staleness-aware version; this script always does a full rebuild.
set -u

APPDIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$APPDIR"

echo "Building fastllm..."
go build -o fastllm ./cmd/server
if [ $? -ne 0 ]; then
    echo "Build failed. See output above."
    exit 1
fi

# Keep the copy on PATH in step with the repo build. Without this the two
# drift: start.sh runs the repo binary while typing `fastllm` runs the one in
# ~/.local/bin, and a stale install looks like a missing feature.
if [ -x "$HOME/.local/bin/fastllm" ]; then
    echo "Refreshing installed copy in ~/.local/bin ..."
    go build -o "$HOME/.local/bin/fastllm" ./cmd/server
fi

echo
echo "Done:"
echo "  $APPDIR/fastllm          (no args = terminal UI, 'server' = API)"
