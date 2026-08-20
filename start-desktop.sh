#!/usr/bin/env bash
# macOS/Linux equivalent of start-desktop.bat — builds and launches the
# Wails native desktop app (cmd/desktop) instead of the browser-based
# server. See start.sh for the staleness-detection logic this mirrors.
set -u

APPDIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$APPDIR"

DESKTOPDIR="$APPDIR/cmd/desktop"
BINDIR="$DESKTOPDIR/build/bin"

# Wails packages a .app bundle on macOS but a bare binary on Linux;
# resolve which one applies (or would apply, pre-build) each time it's
# needed rather than caching it once, since a build can create the bundle.
resolve_exe() {
    if [ -d "$BINDIR/fastllm-desktop.app" ]; then
        echo "$BINDIR/fastllm-desktop.app"
    else
        echo "$BINDIR/fastllm-desktop"
    fi
}

# BSD stat (macOS) takes "-f <format>"; GNU stat (Linux) takes "-c
# <format>". Detect once by checking the output looks like a single
# timestamp (all digits) — see start.sh for why exit code alone isn't
# reliable here.
_probe="$(stat -f %m . 2>/dev/null)"
case "$_probe" in
    ''|*[!0-9]*) STAT_FLAVOR=gnu ;;
    *)           STAT_FLAVOR=bsd ;;
esac
unset _probe

mtime_of() {
    if [ "$STAT_FLAVOR" = bsd ]; then
        stat -f %m "$1" 2>/dev/null
    else
        stat -c %Y "$1" 2>/dev/null
    fi
}

newest_mtime_under() {
    find "$@" -type f 2>/dev/null | while read -r f; do mtime_of "$f"; done | sort -n | tail -n1
}

# The wails CLI isn't a Go module dependency (cmd/desktop only imports
# the wails/v2 library), so `go build` alone won't fetch it — install it
# once via `go install` if it's missing from PATH.
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

EXE="$(resolve_exe)"
EXE_MTIME="$(mtime_of "$EXE")"
GO_NEWEST="$(newest_mtime_under cmd internal go.mod go.sum)"
WEB_NEWEST="$(newest_mtime_under web/src web/package.json web/vite.config.js)"

need_build=0
[ -n "$GO_NEWEST" ] && { [ -z "$EXE_MTIME" ] || [ "$GO_NEWEST" -gt "$EXE_MTIME" ]; } && need_build=1
[ -n "$WEB_NEWEST" ] && { [ -z "$EXE_MTIME" ] || [ "$WEB_NEWEST" -gt "$EXE_MTIME" ]; } && need_build=1

if [ "$need_build" = 1 ] || [ ! -e "$EXE" ]; then
    echo "Source changed since last build, rebuilding fastllm desktop app..."
    (cd "$DESKTOPDIR" && wails build)
    if [ $? -ne 0 ]; then
        echo "Desktop build failed. See output above."
        exit 1
    fi
    EXE="$(resolve_exe)"
fi

if [ ! -e "$EXE" ]; then
    echo "$EXE not found and could not be built."
    exit 1
fi

# Wails' SingleInstanceLock (see cmd/desktop/main.go) means launching
# this while the app is already open just brings the existing window to
# front instead of starting a second instance, so no port/process check
# is needed here the way start.sh needs one for cmd/server.
if [ -d "$EXE" ]; then
    open "$EXE"
else
    chmod +x "$EXE"
    "$EXE" &
fi
