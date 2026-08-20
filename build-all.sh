#!/usr/bin/env bash
# macOS/Linux equivalent of build-all.bat — builds the frontend once, then
# both the browser/server binary (cmd/server) and the native desktop app
# (cmd/desktop, via Wails). See start.sh / start-desktop.sh for the
# single-target, staleness-aware versions of each of these; this script
# always does a full rebuild of both, unconditionally.
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
if [ $? -ne 0 ]; then
    echo "cmd/server build failed. See output above."
    exit 1
fi

# The wails CLI isn't a Go module dependency, so `go build` alone won't
# fetch it — install it once via `go install` if it's missing, same as
# start-desktop.sh.
if ! command -v wails >/dev/null 2>&1; then
    echo "wails CLI not found, installing..."
    if ! go install github.com/wailsapp/wails/v2/cmd/wails@latest; then
        echo "Failed to install wails CLI. See output above."
        exit 1
    fi
    GOBIN="$(go env GOBIN)"
    [ -z "$GOBIN" ] && GOBIN="$(go env GOPATH)/bin"
    PATH="$GOBIN:$PATH"
    if ! command -v wails >/dev/null 2>&1; then
        echo "wails installed to $GOBIN but that's not on PATH — add it and re-run."
        exit 1
    fi
fi

echo "Building fastllm-desktop (Wails desktop app)..."
(cd "$APPDIR/cmd/desktop" && wails build -s)
if [ $? -ne 0 ]; then
    echo "cmd/desktop build failed. See output above."
    exit 1
fi

# Wails packages a .app bundle on macOS but a bare binary on Linux.
BINDIR="$APPDIR/cmd/desktop/build/bin"
if [ -d "$BINDIR/fastllm.app" ]; then
    DESKTOP_OUT="$BINDIR/fastllm.app"
else
    DESKTOP_OUT="$BINDIR/fastllm-desktop"
fi

echo
echo "Done:"
echo "  $APPDIR/fastllm"
echo "  $DESKTOP_OUT"
