# Decision Ledger (local)

Auto-generated — do not edit by hand.

## fastllm

### architecture-scope
- `Native interactive terminal TUI built on internal/harness; removed embedded web editor and terminal emulator components to streamline codebase and avoid IDE duplication` — External IDEs and native shells handle editing and terminal emulation better; focusing fastllm on native TUI harness and lightweight chat UI (2026-09-19T03:23:21+00:00)

### frontend-bundle-splitting
- `Lazy-load MessageContent and use Vite 8 Rolldown codeSplitting groups capped at 300 KB for React, Markdown, KaTeX, highlighting, and remaining vendor modules.` — Reduces the initial app chunk from 864 KB to 82 KB, defers markdown-only code and CSS until needed, and removes the large-chunk warning; raising the warning threshold would not improve loading and manualChunks is deprecated in Vite 8. (2026-09-19T17:54:57+00:00)

### harness-design
- `Shared internal engine (internal/harness) providing autonomous multi-turn tool loop (file tools + run_command), exposed via both cmd/cli and POST /api/harness/run` — Allows direct CLI execution from terminal while supporting programmatic benchmark sweeps from external scripts with identical behavior (2026-09-19T03:12:07+00:00)

### shell-mode
- `Integrated Shell Mode (/shell, /sh) and inline execution (!cmd, $ cmd) with live terminal IO, plus /c conversation and screen clearing` — Allows running interactive host commands directly inside the TUI without leaving the REPL, and keeps terminal UI uncluttered (2026-09-19T17:01:29+00:00)

### tui-styling
- `Native zero-overhead modern ANSI/Unicode styling engine with Windows VT support, rounded cards, colored diffs, and structured tool blocks` — Zero new external dependencies, preserves fast streaming REPL responsiveness, provides clean monochrome/accent aesthetics matching web UI without emojis (2026-09-19T16:50:03+00:00)

### ui-architecture
- `Unified conversational canvas integrating Chat and Autonomous Agent with composer mode toggle (Chat/Agent) and inline collapsible agent turn cards` — Eliminates context fragmentation, enables conversational multi-turn follow-ups, persists agent runs in SQLite history, and removes duplicate headers and input controls (2026-09-19T16:36:33+00:00)

### web-fetch-network-policy
- `Allow only publicly routable HTTP(S) destinations; reject local, private, link-local, multicast, carrier-grade NAT, documentation, and reserved IP ranges at dial time.` — Preserves zero-configuration public browsing while preventing model-generated URLs and redirects from reaching services on the user machine or private network; unrestricted fetch was unsafe and a hostname allowlist was too restrictive. (2026-09-19T17:49:16+00:00)

### web-tools
- `Keyless DuckDuckGo search with Brave API fallback, and HTML-to-markdown web page fetcher for autonomous harness and chat` — Zero configuration required out of the box, provides live web information to both TUI and browser UI, with support for optional API key upgrades (2026-09-19T17:08:03+00:00)
