# Decision Ledger (local)

Auto-generated — do not edit by hand.

## fastllm

### agent-reliability-policy
- `Retry transient model failures up to 3 attempts with exponential backoff; reject malformed tool JSON after fenced-JSON normalization` — Improves resilience without retrying deterministic or user-canceled failures (2026-09-19T21:24:47+00:00)

### architecture-scope
- `Native interactive terminal TUI built on internal/harness; removed embedded web editor and terminal emulator components to streamline codebase and avoid IDE duplication` — External IDEs and native shells handle editing and terminal emulation better; focusing fastllm on native TUI harness and lightweight chat UI (2026-09-19T03:23:21+00:00)

### credentials
- `config.json holds references only (api_key_file); SaveSettings relocates any inline api_key to ~/.fastllm/keys/<id> at 0600 and rewrites the entry to point at it` — Structural rather than advisory: config.json is the file most likely to be copied into a repo, gist, or bug report, so no code path present or future can write a credential into it. Relocation rather than stripping keeps existing inline keys working instead of silently deleting them. (2026-09-19T22:33:11+00:00)

### frontend
- `Terminal UI (cmd/cli) is the only user interface; the React/Vite frontend in web/ is deleted and cmd/server runs headless, serving /api/* and /v1/* with a plain-text notice at / and 404 for every other path.` — The TUI reached parity and recent work all targeted it, so maintaining two UIs duplicated effort. Removal was gated on the stranded-data checklist rather than done outright: conversations lived only in SQLite, so an idempotent import (fastllm-cli --import-conversations, plus /conversations and /import in the TUI) was built and verified against the real 22MB fastllm.db before deleting anything. Notes had 0 rows and were unwanted; cloud_providers had no row at all and config.json is already the real source for the TUI. The HTTP API/compat backend is deliberately NOT removed -- retiring the frontend is a separate decision from retiring the server. (2026-09-20T16:46:20+00:00)

### frontend-bundle-splitting
- `Obsolete -- no bundler in this project. The Vite/Rolldown code-splitting setup was deleted along with web/ when the React frontend was removed; see fastllm / frontend.` — Superseded by the frontend removal on 2026-09-20. Kept as a tombstone rather than left stating live policy, so a future session does not go looking for vite.config.js or try to re-tune chunk sizes for a bundler that is no longer part of the repo. (2026-09-20T16:46:31+00:00)

### harness-design
- `Shared internal engine (internal/harness) providing autonomous multi-turn tool loop (file tools + run_command), exposed via both cmd/cli and POST /api/harness/run` — Allows direct CLI execution from terminal while supporting programmatic benchmark sweeps from external scripts with identical behavior (2026-09-19T03:12:07+00:00)

### self-hosted-endpoints
- `Endpoints are tagged in config with "provider": "<name>" and resolved by config.ResolveProvider (env first, then the tagged entry); no host address or key path is compiled into the binary` — Three layers (harness CLI, llm router, chat handler) each carried a duplicate fallback chain ending in 127.0.0.1:8010 and ~/.osu-llm/vllm-api-key, shipping one developer's machine to every user. Tagging by provider lets code find the endpoint without knowing where it lives; an unconfigured machine resolves to zero values and callers report 'not configured' instead of dialing a meaningless port. (2026-09-19T22:42:14+00:00)

### self-hosted-provider-naming
- `Provider is called 'selfhosted' in Go identifiers, config tags, prefixes and user-facing strings; SQLite columns and HTTP JSON keys stay osu_* for storage/wire compatibility` — Renaming the wire and storage keys would need a DB migration and a frontend change for no functional gain; the visible surface is what leaked one site's naming into the project. Model-specific behaviour (name matching for tool/vision capability, alias collapsing, channel-format detection) moved to provider-level or config-level rules so no model name is compiled in. (2026-09-19T22:59:29+00:00)

### shell-mode
- `Integrated Shell Mode (/shell, /sh) and inline execution (!cmd, $ cmd) with live terminal IO, plus /c conversation and screen clearing` — Allows running interactive host commands directly inside the TUI without leaving the REPL, and keeps terminal UI uncluttered (2026-09-19T17:01:29+00:00)

### subagent-polling-policy
- `A specific agent_status call waits up to 60 seconds by default for active-child completion, is parent-context cancelable, and supports wait_seconds 0 for an immediate snapshot; list-all remains immediate.` — Runtime waiting prevents fast local models from consuming every parent turn on tight status polling while preserving asynchronous child execution and explicit snapshots. (2026-09-20T16:00:10+00:00)

### tui-line-editor
- `Use reeflective/readline behind an interactiveInput abstraction with scanner fallback` — Provides cross-platform history, arrows, Ctrl-R, multiline input, and programmable completion while preserving piped-input tests (2026-09-19T21:24:47+00:00)

### tui-markdown-rendering
- `Dependency-free ANSI renderer for headings, bullets, quotes, fenced code, inline code, emphasis, links, rules, diffs, and structured tool previews.` — Improves terminal readability without adding a heavy TUI framework or duplicating browser-grade layout behavior. (2026-09-19T18:10:49+00:00)

### tui-permission-model
- `Three session-scoped modes for model-initiated mutations: ask by default, read-only removes mutating tools, and auto permits autonomous execution. Ask supports once/session-tool/deny; grants are never persisted or restored.` — Ask is the safest usable default, read-only provides a hard capability boundary, and auto preserves unattended workflows; direct user-entered shell commands are already explicit authorization and do not reprompt. (2026-09-19T21:05:55+00:00)

### tui-runtime-controls
- `Session-scoped /set controls for max turns, command timeout, thinking level, and command-tool availability; settings autosave with the session.` — Allows safe live tuning without restarting and makes resumed behavior reproducible; global-only config would leak task-specific choices into unrelated work. (2026-09-19T18:10:49+00:00)

### tui-session-lifecycle
- `Autosave sessions, recover the latest unclosed non-empty session, support explicit --resume last, and prune empty sessions after 24h plus non-empty sessions beyond 100` — Recovers crashes without age-deleting meaningful content; count cap prevents unbounded accumulation (2026-09-19T21:24:47+00:00)

### tui-session-storage
- `Autosaved per-session JSON files under ~/.fastllm/sessions with restrictive permissions, atomic temp-file replacement, and /sessions, /resume, /new commands.` — Keeps the standalone CLI portable and independent of the web SQLite database while preserving provider-agnostic message history; SQLite coupling and one monolithic history file were rejected. (2026-09-19T18:10:48+00:00)

### tui-styling
- `Native zero-overhead modern ANSI/Unicode styling engine with Windows VT support, rounded cards, colored diffs, and structured tool blocks` — Zero new external dependencies, preserves fast streaming REPL responsiveness, provides clean monochrome/accent aesthetics matching web UI without emojis (2026-09-19T16:50:03+00:00)

### ui-architecture
- `Unified conversational canvas integrating Chat and Autonomous Agent with composer mode toggle (Chat/Agent) and inline collapsible agent turn cards` — Eliminates context fragmentation, enables conversational multi-turn follow-ups, persists agent runs in SQLite history, and removes duplicate headers and input controls (2026-09-19T16:36:33+00:00)

### web-fetch-network-policy
- `Allow only publicly routable HTTP(S) destinations; reject local, private, link-local, multicast, carrier-grade NAT, documentation, and reserved IP ranges at dial time.` — Preserves zero-configuration public browsing while preventing model-generated URLs and redirects from reaching services on the user machine or private network; unrestricted fetch was unsafe and a hostname allowlist was too restrictive. (2026-09-19T17:49:16+00:00)

### web-tools
- `Keyless DuckDuckGo search with Brave API fallback, and HTML-to-markdown web page fetcher for autonomous harness and chat` — Zero configuration required out of the box, provides live web information to both TUI and browser UI, with support for optional API key upgrades (2026-09-19T17:08:03+00:00)

## fastllm-harness

### subagent-orchestration
- `Use native asynchronous child Runner tasks with isolated context, shared workspace, structured lifecycle tools, max 3 concurrent children, and max depth 2; ask/read-only children cannot mutate.` — Native orchestration reuses the harness and yields structured results/cancellation; subprocess agents lost coordination, while synchronous delegation prevented useful parallel work. (2026-09-20T15:40:34+00:00)

## osu-cluster-scripts

### obsolete-model-script-cleanup
- `Archive model-specific scripts when their checkpoints and derived artifacts are absent; retain generic launchers and recoverable diagnostics.` — A dated archive removes active-directory clutter without irreversible deletion; generic scripts and diagnostics remain useful across models. (2026-09-20T14:54:49+00:00)
