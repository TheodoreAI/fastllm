#!/usr/bin/env bash
# macOS/Linux equivalent of start.bat — see that file for the Windows
# version and the comments explaining the staleness-detection logic this
# mirrors line for line.
set -u

APPDIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$APPDIR"

# .env is gitignored — a place for machine-local overrides (e.g.
# LLM_BASE_URL/LLM_CHAT_MODEL pointed at a local llama-server instead of
# the Ollama default) that have no business being committed, since
# they're specific to whatever's actually running on this machine. Not
# required; nothing changes if it's absent.
if [ -f "$APPDIR/.env" ]; then
    set -a
    # shellcheck disable=SC1091
    source "$APPDIR/.env"
    set +a
fi

EXE="$APPDIR/fastllm"
PORT=8080

open_browser() {
    if command -v open >/dev/null 2>&1; then
        open "http://localhost:$PORT"        # macOS
    elif command -v xdg-open >/dev/null 2>&1; then
        xdg-open "http://localhost:$PORT"    # Linux
    fi
}

# BSD stat (macOS) takes "-f <format>"; GNU stat (Linux) takes "-c
# <format>" and treats -f as an unrelated "show filesystem info" flag
# that exits 0 with the wrong (multi-line) output instead of erroring —
# so detecting the flavor by exit code alone doesn't work. Detect once by
# checking the output actually looks like a single timestamp (all
# digits), which only the correct flag/flavor pairing produces.
_probe="$(stat -f %m . 2>/dev/null)"
case "$_probe" in
    ''|*[!0-9]*) STAT_FLAVOR=gnu ;;
    *)           STAT_FLAVOR=bsd ;;
esac
unset _probe

# mtime_of PATH: prints PATH's modification time as a Unix timestamp, or
# nothing if it doesn't exist. Used below purely as a boolean/comparison
# check via [ -n "$(...)" ] and numeric -gt.
mtime_of() {
    if [ "$STAT_FLAVOR" = bsd ]; then
        stat -f %m "$1" 2>/dev/null
    else
        stat -c %Y "$1" 2>/dev/null
    fi
}

# If something is already listening on 8080, check whether it's our own
# fastllm binary and whether the binary on disk has been rebuilt since
# that process started (Go embeds web/dist at build time, so a newer
# binary on disk means the running process is serving a stale embed). If
# so, stop it so the staleness check below rebuilds/relaunches instead of
# silently reopening a browser tab against stale code. If the port is
# held by something else entirely, leave it alone.
PORT_PID="$(lsof -tiTCP:"$PORT" -sTCP:LISTEN 2>/dev/null | head -n1)"
if [ -n "$PORT_PID" ]; then
    # The "txt" fd row is the process's own executable — lsof -p's other
    # rows (open sockets, cwd, etc.) don't carry a filesystem path in NAME
    # at all, so matching on any row ending in "fastllm" would either
    # miss the real path or match the wrong row.
    PROC_PATH="$(lsof -p "$PORT_PID" 2>/dev/null | awk '$4=="txt" {print $NF; exit}')"
    OUR_EXE_REAL="$(cd "$APPDIR" && [ -f fastllm ] && pwd)/fastllm"
    if [ -z "$PROC_PATH" ] || [ "$PROC_PATH" != "$OUR_EXE_REAL" ]; then
        echo "Port $PORT is in use by something other than fastllm — opening it as-is."
        open_browser
        exit 0
    fi

    EXE_MTIME="$(mtime_of "$EXE")"
    # Process start time isn't portable to fetch cheaply across macOS/Linux
    # without extra tooling, so fall back to comparing against the binary's
    # own mtime recorded the last time this script launched it.
    STAMP="$APPDIR/.fastllm.launched"
    LAUNCH_MTIME="$(mtime_of "$STAMP")"
    if [ -n "$EXE_MTIME" ] && { [ -z "$LAUNCH_MTIME" ] || [ "$EXE_MTIME" -gt "$LAUNCH_MTIME" ]; }; then
        kill "$PORT_PID" 2>/dev/null
        sleep 0.3
    else
        open_browser
        exit 0
    fi
fi

# Decide whether a rebuild is needed: newest mtime among Go source and
# the frontend source tree vs. the binary / web/dist. Skips
# node_modules (irrelevant + huge, would slow this down a lot).
newest_mtime_under() {
    find "$@" -type f 2>/dev/null | while read -r f; do mtime_of "$f"; done | sort -n | tail -n1
}

EXE_MTIME="$(mtime_of "$EXE")"
DIST_MTIME="$(mtime_of "$APPDIR/web/dist/index.html")"
GO_NEWEST="$(newest_mtime_under cmd internal go.mod go.sum)"
WEB_NEWEST="$(newest_mtime_under web/src web/package.json web/vite.config.js)"

need_backend=0
need_frontend=0
[ -n "$GO_NEWEST" ] && { [ -z "$EXE_MTIME" ] || [ "$GO_NEWEST" -gt "$EXE_MTIME" ]; } && need_backend=1
[ -n "$WEB_NEWEST" ] && { [ -z "$DIST_MTIME" ] || [ "$WEB_NEWEST" -gt "$DIST_MTIME" ]; } && need_frontend=1

if [ "$need_backend" = 1 ] || [ "$need_frontend" = 1 ]; then
    echo "Source changed since last build, rebuilding fastllm..."

    if [ "$need_frontend" = 1 ]; then
        echo "  Building frontend..."
        (cd "$APPDIR/web" && npm run build)
        if [ $? -ne 0 ]; then
            echo "Frontend build failed. See output above."
            exit 1
        fi
    fi

    echo "  Building backend..."
    (cd "$APPDIR" && go build -o fastllm ./cmd/server)
    if [ $? -ne 0 ]; then
        echo "Backend build failed. See output above."
        exit 1
    fi
fi

if [ ! -f "$EXE" ]; then
    echo "fastllm binary not found and could not be built."
    exit 1
fi

chmod +x "$EXE"
nohup "$EXE" > "$APPDIR/fastllm.log" 2>&1 &
touch "$APPDIR/.fastllm.launched"

# Wait for the server to come up, then open the browser.
for _ in $(seq 1 30); do
    if command -v nc >/dev/null 2>&1 && nc -z localhost "$PORT" 2>/dev/null; then
        break
    fi
    sleep 0.3
done

open_browser
