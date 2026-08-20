#!/usr/bin/env bash
# Hot-reload dev mode for the Wails desktop app — `wails dev` runs the
# Vite dev server inside the app window, so React/CSS edits show up
# instantly and Go changes trigger a fast incremental rebuild + restart,
# unlike start-desktop.sh's full `wails build` + relaunch every run. Not
# a replacement for start-desktop.sh: this opens its own dev window, and
# is meant for iterating on UI/UX changes, not for producing the
# installable .app.
set -u

APPDIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$APPDIR"

# .env is gitignored — see start.sh for why. `wails dev` has no
# equivalent of start-desktop.sh's own sourcing, so this does the same
# thing here: without it, LLM_BASE_URL/LLM_CHAT_MODEL overrides
# wouldn't reach the dev-mode app either.
if [ -f "$APPDIR/.env" ]; then
    set -a
    # shellcheck disable=SC1091
    source "$APPDIR/.env"
    set +a
fi

# Same on-demand install as start-desktop.sh — cmd/desktop only imports
# the wails/v2 library, so `go build`/`wails dev` alone won't fetch the
# CLI itself.
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

cd "$APPDIR/cmd/desktop"
exec wails dev
