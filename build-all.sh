#!/usr/bin/env bash
# macOS/Linux equivalent of build-all.bat — builds the frontend, then the
# browser/server binary (cmd/server), and refreshes the copy on PATH. See
# start.sh for the staleness-aware single-target version; this script always
# does a full rebuild.
set -u

APPDIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$APPDIR"

echo "Building frontend..."
(cd "$APPDIR/web" && npm install && npm run build)
if [ $? -ne 0 ]; then
    echo "Frontend build failed. See output above."
    exit 1
fi

echo "Building fastllm (browser/server mode)..."
go build -o fastllm ./cmd/server

# Keep the copy on PATH in step with the repo build. Without this the two
# drift: start.sh runs the repo binary while typing `fastllm` runs the one in
# ~/.local/bin, and a stale install looks like a missing feature.
if [ -x "$HOME/.local/bin/fastllm" ]; then
    echo "Refreshing installed copy in ~/.local/bin ..."
    go build -o "$HOME/.local/bin/fastllm" ./cmd/server
fi
if [ $? -ne 0 ]; then
    echo "cmd/server build failed. See output above."
    exit 1
fi

echo
echo "Done:"
echo "  $APPDIR/fastllm"
