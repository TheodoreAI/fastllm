# Context Ledger (local)

Auto-generated — do not edit by hand.

## fastllm

### agent-lifecycle
- `Runner owns child-agent lifetime; children remain asynchronous and isolated across interactive turns, while Runner.Close cancels and joins all children at the request/session boundary.` — Preserves the existing asynchronous orchestration model while preventing request and session shutdown from leaking LLM work. (2026-09-22T01:47:32+00:00)

### agent-reliability-policy
- `Retry transient model failures up to 3 attempts with exponential backoff; reject malformed tool JSON after fenced-JSON normalization` — Improves resilience without retrying deterministic or user-canceled failures (2026-09-19T21:24:47+00:00)

### architecture-scope
- `Native interactive terminal TUI built on internal/harness; removed embedded web editor and terminal emulator components to streamline codebase and avoid IDE duplication` — External IDEs and native shells handle editing and terminal emulation better; focusing fastllm on native TUI harness and lightweight chat UI (2026-09-19T03:23:21+00:00)

### context-assembly
- `One builder (BuildSystemPrompt in internal/harness/promptcontext.go) assembles the system prompt inside Runner.Run: XML-tagged sections ordered stable->volatile (operating_rules, skills, project_rules, environment, active_skill, sandbox, mode). Callers pass only SystemPrompt (base override) and PromptExtra. History replays tool calls in matched pairs; compaction summaries are user-role <conversation_summary> messages.` — Three entry points built the prompt differently and the TUI sent rules twice. Tags give workspace files clear boundaries and provenance (untrusted data), stable-first order preserves vLLM/Anthropic prefix caches, mode last sits where attention is strongest, and mid-conversation system messages get hoisted to the top by Anthropic/Gemini. Considered: Markdown headings (collide with headings inside rules files). (2026-09-23T21:27:35+00:00)

### context-budget
- `Size the compaction budget from the active model's context window: endpoint context_window in config.json, then a built-in model-ID table, then a 32k default; compact at 75% of the window converted at 4 chars/token, with an 8k floor.` — A fixed 60k-char budget compacted a 200k model ~10x too early and a small model too late. Resolution is offline by design: the configured endpoints are frequently-unreachable tunnels, so a startup /v1/models probe would make the budget depend on whether a port happened to be forwarded. 75% reserves room for the reply (max_tokens 4-8k), tool schemas, and the chars/4 approximation understating code and JSON. (2026-09-22T04:07:03+00:00)

### credentials
- `config.json holds references only (api_key_file); SaveSettings relocates any inline api_key to ~/.fastllm/keys/<id> at 0600 and rewrites the entry to point at it` — Structural rather than advisory: config.json is the file most likely to be copied into a repo, gist, or bug report, so no code path present or future can write a credential into it. Relocation rather than stripping keeps existing inline keys working instead of silently deleting them. (2026-09-19T22:33:11+00:00)

### default-permission-mode
- `TUI and legacy REPL start in plan mode unless -mode is given; unset modes (old sessions, bare PermissionController, /import) also default to plan; resumed sessions keep their saved mode` — User asked for plan as the default; matches the capability-policy rule that an empty mode fails closed to plan (2026-09-24T02:02:40+00:00)

### escape-cancellation
- `Escape cancels the foreground agent turn or direct shell command, while explicitly backgrounded processes remain running; shell cancellation terminates the process tree with a direct-process fallback on Windows.` — Foreground-only cancellation matches user intent without destroying persistent servers; process-tree termination prevents orphaned children, and the Windows fallback handles restricted taskkill environments. (2026-09-22T02:41:49+00:00)

### frontend
- `One binary, cmd/server: no arguments launches the terminal UI, the 'server' subcommand runs the headless HTTP backend (/api/* and /v1/*, plain-text notice at /, 404 elsewhere). The React/Vite frontend in web/ and the duplicate cmd/cli binary are both deleted.` — The TUI reached parity and recent work all targeted it, so maintaining two UIs duplicated effort. Removal was gated on the stranded-data checklist rather than done outright: conversations lived only in SQLite, so an idempotent import (--import-conversations, plus /conversations and /import in the TUI) was built and verified against the real 22MB fastllm.db before deleting anything. Notes had 0 rows and were unwanted; cloud_providers had no row at all and config.json is already the real source for the TUI. cmd/cli was dropped because cmd/server already dispatches to the same TUI and was a strict superset for 1.2MB (18.7 vs 19.9MB) -- and the split had already caused a real defect, where the legacystore opener was wired into cmd/cli only, leaving the installed binary unable to import. Wire any TUI-facing dependency in cmd/server. The HTTP API/compat backend is deliberately NOT removed -- retiring the frontend is a separate decision from retiring the server. (2026-09-20T17:05:04+00:00)

### frontend-bundle-splitting
- `Obsolete -- no bundler in this project. The Vite/Rolldown code-splitting setup was deleted along with web/ when the React frontend was removed; see fastllm / frontend.` — Superseded by the frontend removal on 2026-09-20. Kept as a tombstone rather than left stating live policy, so a future session does not go looking for vite.config.js or try to re-tune chunk sizes for a bundler that is no longer part of the repo. (2026-09-20T16:46:31+00:00)

### harness-design
- `Shared internal engine (internal/harness) providing autonomous multi-turn tool loop (file tools + run_command), exposed via both cmd/cli and POST /api/harness/run` — Allows direct CLI execution from terminal while supporting programmatic benchmark sweeps from external scripts with identical behavior (2026-09-19T03:12:07+00:00)

### image-generation-integration
- `Expose OpenAI-compatible image generation as /image in both terminal modes, backed by internal/imagegen; resolve a configured image endpoint and persist validated decoded files under the active workspace instead of storing base64 in session history.` — Fits the terminal-only architecture and existing endpoint config while keeping large base64 payloads out of chat/session storage; preferred over adding a web renderer or coupling image output to the chat completion path. (2026-09-21T20:18:21+00:00)

### ornith-1.5-local-integration
- `Use the Ollama alias ornith-1.5:9b-fastllm with num_ctx 8192, explicit 8192 context in config, and a verified ornith-1.5 local-tool allowlist entry; keep muse-glimmer as the default pending broader comparison.` — The stock tag defaults to 4096 and rejected fastllm first-turn prompt at 4458 tokens. The 8K alias fit with 85% GPU/15% CPU and completed a five-turn glob/read/edit/read coding run with structured tool calls; changing the default from one small task would be premature. (2026-09-25T02:29:15+00:00)

### self-hosted-endpoints
- `Endpoints are tagged in config with "provider": "<name>" and resolved by config.ResolveProvider (env first, then the tagged entry); no host address or key path is compiled into the binary` — Three layers (harness CLI, llm router, chat handler) each carried a duplicate fallback chain ending in 127.0.0.1:8010 and ~/.osu-llm/vllm-api-key, shipping one developer's machine to every user. Tagging by provider lets code find the endpoint without knowing where it lives; an unconfigured machine resolves to zero values and callers report 'not configured' instead of dialing a meaningless port. (2026-09-19T22:42:14+00:00)

### self-hosted-provider-naming
- `Provider is called 'selfhosted' in Go identifiers, config tags, prefixes and user-facing strings; SQLite columns and HTTP JSON keys stay osu_* for storage/wire compatibility` — Renaming the wire and storage keys would need a DB migration and a frontend change for no functional gain; the visible surface is what leaked one site's naming into the project. Model-specific behaviour (name matching for tool/vision capability, alias collapsing, channel-format detection) moved to provider-level or config-level rules so no model name is compiled in. (2026-09-19T22:59:29+00:00)

### server-control-plane
- `Headless API binds 127.0.0.1 by default; every route but GET / needs the ~/.fastllm/server-token bearer token (FASTLLM_TOKEN overrides); Origin headers and non-loopback Host refused; bodies must be JSON; harness runs capped by FASTLLM_MAX_MODE (default edit) and FASTLLM_ALLOWED_ROOTS` — Kernel gap #1: the request body chose permission_mode/allow_commands/working_dir on an unauthenticated all-interfaces port, so any LAN host or web page could run commands; the monitor cannot protect against a caller allowed to be the user (2026-09-24T02:14:22+00:00)

### shell-mode
- `Integrated Shell Mode (/shell, /sh) and inline execution (!cmd, $ cmd) with live terminal IO, plus /c conversation and screen clearing` — Allows running interactive host commands directly inside the TUI without leaving the REPL, and keeps terminal UI uncluttered (2026-09-19T17:01:29+00:00)

### skill-command
- `Discover project and global skills from .agents, .claude, .codex, .opencode, OpenCode config, and legacy AGY .agent sources; project and nearer definitions override global ones. /skills and /skills list enumerate skills, /skills NAME shows details, /skills NAME TASK applies instructions for one run, and compact catalog metadata is advertised to the model.` — Broader cross-agent compatibility and model-visible discovery were explicitly requested; compact metadata limits context cost, while one-run body injection and project precedence retain the safety benefits of the previous project-local design. (2026-09-22T03:32:24+00:00)

### sqlite-connection-policy
- `Use one database/sql connection for the low-volume local SQLite store and perform note appends as one UPSERT RETURNING statement.` — A single connection makes connection-scoped PRAGMAs reliable and avoids SQLITE_BUSY pool races; atomic SQL also protects against other writers. (2026-09-22T01:47:41+00:00)

### streaming-output
- `Stage one: stream assistant text via a new tool-aware StreamChatWithTools on the OpenAI-compatible client, surfaced as opt-in EventTokenDelta/EventTokenDiscard events; TUI commits whole lines only and can retract a turn's streamed text.` — internal/llm's five existing StreamChat impls take no tools and return no tool_calls, so the agent loop could not use them. Whole-line commits are required because FormatMarkdownWidth is line-based with code-fence state. Retraction is required because both the prose-tool-call salvage (parseFallbackToolCall) and a mid-stream retry re-emit text already shown. Measured 202ms to first token vs 701ms total on gemma-4-31b. (2026-09-22T04:32:19+00:00)

### subagent-polling-policy
- `A specific agent_status call waits up to 60 seconds by default for active-child completion, is parent-context cancelable, and supports wait_seconds 0 for an immediate snapshot; list-all remains immediate.` — Runtime waiting prevents fast local models from consuming every parent turn on tight status polling while preserving asynchronous child execution and explicit snapshots. (2026-09-20T16:00:10+00:00)

### tool-dispatch-architecture
- `Runner and both terminal modes execute autonomous tools through one strict typed executor with mode-specific context and callbacks.` — Removes two drifting switch implementations while preserving permission and live-output differences. (2026-09-22T01:47:38+00:00)

### tui-changes-column
- `Right-hand TUI column lists files changed by agent write/edit/patch tools this session, tallied in memory from tool args; shown only when frame >= 100 cols` — No git or disk polling needed and works outside git repos; hidden on narrow terminals to keep the transcript readable; shell-mode edits are not tracked (2026-09-23T03:56:33+00:00)

### tui-command-suggestions
- `Slash-command dropdown above the input box, driven by commandSections in commands.go (single source for /help, suggestions and legacy completion); prefix-then-substring matching; stage-2 argument values; Tab completes, smart Enter runs unless a <required> arg is missing; destructive commands (/undo /clear /exit /discard /delete-session /kill and aliases) only complete from the list` — User chose dropdown + smart Enter + argument values; the destructive guard stops a half-typed prefix like /u from rolling back work; one table ends the drift between help and completion (2026-09-24T01:53:57+00:00)

### tui-line-editor
- `Use reeflective/readline behind an interactiveInput abstraction with scanner fallback` — Provides cross-platform history, arrows, Ctrl-R, multiline input, and programmable completion while preserving piped-input tests (2026-09-19T21:24:47+00:00)

### tui-markdown-rendering
- `Dependency-free ANSI renderer for headings, bullets, quotes, fenced code, inline code, emphasis, links, rules, diffs, and structured tool previews.` — Improves terminal readability without adding a heavy TUI framework or duplicating browser-grade layout behavior. (2026-09-19T18:10:49+00:00)

### tui-permission-model
- `Four session modes enforced by one reference monitor (internal/harness/monitor.go): plan (read-only, no network, ends in submit_plan + user approval to agent/edit/full), agent (ask per mutating call; default in TUI), edit (file edits unasked, no subprocess/then_run/delegation), full (unasked). Shift+Tab cycles in the TUI; mode changes refused mid-turn; grants never persisted; legacy ask/read-only/auto accepted.` — User wants the harness to act like a kernel treating the LLM as untrusted: modes must be enforced (tool list, dispatch, broker, file writer are all views of one authorize), not prompt-only. Considered keeping 3 modes + prompt injection for plan; rejected as prompt-only enforcement. (2026-09-23T20:52:09+00:00)

### tui-runtime-controls
- `Session-scoped /set controls for max turns, command timeout, thinking level, and command-tool availability; settings autosave with the session.` — Allows safe live tuning without restarting and makes resumed behavior reproducible; global-only config would leak task-specific choices into unrelated work. (2026-09-19T18:10:49+00:00)

### tui-session-lifecycle
- `Autosave sessions, recover the latest unclosed non-empty session, support explicit --resume last, and prune empty sessions after 24h plus non-empty sessions beyond 100` — Recovers crashes without age-deleting meaningful content; count cap prevents unbounded accumulation (2026-09-19T21:24:47+00:00)

### tui-session-picker
- `Modal picker via /sessions and bare /resume (/sessions list keeps the table); current-dir sessions first, Tab toggles this-dir-only; typing filters; F2 (alias Ctrl+R) inline rename, Delete (alias Ctrl+D) delete with y/n confirm; control runes never enter the filter; refuses to open during a run` — User's Windows terminal delivered Ctrl+D as a rune event (appeared as a space in the filter), so primary actions moved to keys Bubble Tea maps by virtual-key code (2026-09-24T01:23:43+00:00)

### tui-session-storage
- `Autosaved per-session JSON files under ~/.fastllm/sessions with restrictive permissions, atomic temp-file replacement, and /sessions, /resume, /new commands.` — Keeps the standalone CLI portable and independent of the web SQLite database while preserving provider-agnostic message history; SQLite coupling and one monolithic history file were rejected. (2026-09-19T18:10:48+00:00)

### tui-styling
- `Native ANSI/Unicode styling with Windows VT support, rounded cards, colored diffs, structured tool blocks, no emojis; all colours come from the active theme (see tui-theme), truecolour downsampled via termenv, 16-colour fallback when not a TTY` — Original zero-dependency styling kept; hue-named helpers now map to theme roles so call sites are unchanged (2026-09-24T01:40:08+00:00)

### tui-theme
- `Theme registry in internal/harness/theme.go (nord default; zinc, terminal, tokyo-night, catppuccin, gruvbox); /theme picker with live preview; choice saved to ~/.fastllm/preferences.json, not config.json` — User wanted new colours without layout changes; one palette now drives both the lipgloss chrome and the ANSI text helpers; config.json can be per-project and is rewritten wholesale, so UI prefs live apart (2026-09-24T01:40:08+00:00)

### ui-architecture
- `Unified conversational canvas integrating Chat and Autonomous Agent with composer mode toggle (Chat/Agent) and inline collapsible agent turn cards` — Eliminates context fragmentation, enables conversational multi-turn follow-ups, persists agent runs in SQLite history, and removes duplicate headers and input controls (2026-09-19T16:36:33+00:00)

### usage-accounting
- `Return provider usage in llm.ChatResult for the same completion and route it through ChatWithUsage; do not use shared LastUsage state.` — Request-scoped results remain correct under concurrent chats, unlike mutable last-request state. (2026-09-22T01:47:35+00:00)

### web-fetch-network-policy
- `Allow only publicly routable HTTP(S) destinations; reject local, private, link-local, multicast, carrier-grade NAT, documentation, and reserved IP ranges at dial time.` — Preserves zero-configuration public browsing while preventing model-generated URLs and redirects from reaching services on the user machine or private network; unrestricted fetch was unsafe and a hostname allowlist was too restrictive. (2026-09-19T17:49:16+00:00)

### web-tools
- `Keyless DuckDuckGo search with Brave API fallback, and HTML-to-markdown web page fetcher for autonomous harness and chat` — Zero configuration required out of the box, provides live web information to both TUI and browser UI, with support for optional API key upgrades (2026-09-19T17:08:03+00:00)

### workspace-config-trust
- `Project .fastllm/config.json is used only when trusted: TOFU keyed by canonical path + SHA-256 of exact bytes in ~/.fastllm/trusted-configs.json (FASTLLM_TRUST_STORE overrides the location); any edit revokes; untrusted configs are skipped by LoadSettings (global applies) and shown via ReviewProjectConfig with /trust and /untrust; SaveSettings trusts project configs it writes; FASTLLM_TRUST_PROJECT_CONFIG=1 opts headless runs in` — Kernel gap #3: a cloned repo's config replaced the global one, so it could point an endpoint at its own server and reuse the user's api_key_file to exfiltrate the key. Skipping inside LoadSettings made all five callers safe without changing its signature (2026-09-24T02:43:00+00:00)

## fastllm-harness

### subagent-orchestration
- `Use native asynchronous child Runner tasks with isolated context, shared workspace, structured lifecycle tools, max 3 concurrent children, and max depth 2; ask/read-only children cannot mutate.` — Native orchestration reuses the harness and yields structured results/cancellation; subprocess agents lost coordination, while synchronous delegation prevented useful parallel work. (2026-09-20T15:40:34+00:00)

## harness

### audit-log
- `admit = decide + AuditLog.Record: one JSONL record per monitor decision (time, depth, mode, tool, consentSummary, args SHA-256, allowed, layer, via) in ~/.fastllm/audit/<session>.jsonl (task-<time>, server-<date>; FASTLLM_AUDIT_DIR overrides); children share the parent's log; approvers report Via through ConsentRequest.Via; append via O_APPEND open-write-close; failures never affect the run; /audit [n] in the TUI` — Kernel gap #7: there was no record of what the harness allowed or refused. Recording at admit, the single choke point, makes the log complete by construction; Via distinguishes a live approval from a session grant, which a bool return could not (2026-09-24T02:49:01+00:00)

### capability-policy
- `policyForRequest is now a view of monitor.decideTool; capabilities/network_policy only narrow the mode table. Empty or unknown mode fails closed to plan, so headless -task and HTTP runs without a mode cannot mutate (-mode / permission_mode opt in). Child of agent runs as plan. Shell still requires write+network. then_run is stripped from mutation schemas wherever run_command is denied.` — Single reference monitor with fail-closed defaults; previous empty-mode headless runs were effectively full access. (2026-09-23T20:52:09+00:00)

### execution-boundary
- `Implemented and validated: trusted execution broker in internal/execution owns a Runner-lifetime manager and immutable per-run/per-agent scopes bound to one workspace and policy. All model or workspace-triggered subprocesses (Git, builds, lint, background commands, fused follow-ups) route through it. Local backend only; RequireIsolation returns ErrUnsupported with no fallback. Direct user shell uses a separately constructed trusted scope. Regression suite in internal/execution plus an AST architecture test pinning exec.Command to the backend and the audited folder picker.` — The design from the prior session is now the shipped structure: production exec.Command sites are confined to internal/execution/local.go and internal/folderpicker, and the boundary's claims (denial, workspace escape, isolation refusal, cancellation vs timeout, scope-owned handles, bounded output, stdin restriction) are enforced by tests rather than only documented. (2026-09-23T01:36:51+00:00)

### exfiltration-policy
- `Monitor invariant I10: read_file of a secret path (isSecretPath on canonical path; .env templates exempt) asks in every mode (ConsentRequest.Always) and is refused headless; search_files skips secret files; an approved secret read marks a SessionTaint shared with child agents and, in the TUI/REPL, with every turn until /new, /clear or resume; while tainted, web_fetch/web_search ask in every mode with scopeExact grants (one exact request). Legacy PermissionController still refuses non-Always asks outside agent mode` — Kernel gap #5: agent/edit/full allowed web tools unasked, so read .env then fetch ?k=<secret> exfiltrated silently. Taint on secret reads (not every read) stops that without prompting on every fetch; shell exfiltration is left to the sandbox (2026-09-24T02:55:50+00:00)

### protected-paths
- `Monitor invariant I8: model write tools never write inside .git (monitor + file layer on canonical path); writes to .fastllm/, rule files (candidateRuleFiles) and skills dirs (projectSkillSources) always Ask, labelled '<tool> (fastllm configuration)' so ordinary session grants do not cover them, refused when no one can be asked; harness git runs with -c core.fsmonitor=false and diffs with --no-ext-diff --no-textconv; hooks and filters left enabled` — Reproduced: edit mode wrote core.fsmonitor into .git/config and the TUI's background git status executed it, violating I4. Hooks/filters stay on because user commits and git-lfs need them and they are unplantable once .git is unwritable (2026-09-24T02:23:21+00:00)

### run-budgets
- `RunRequest.Budget (JSON) + shared budgetMeter per run tree (children inherit): time as ctx deadline (default 30m), tokens/cost checked before each model call (default unlimited; unpriced billable model stops a cost-limited run), writes 500/50MB and web 100 charged after admit, 8 concurrent background processes; TUI /set tokens|cost|duration saved in InteractiveRuntime; per prompt, not per session` — P2 #9: a looping model had only max-turns and command timeouts; sharing the meter with children stops delegation from resetting limits, and tokens/cost default to unlimited because local models are free and token counts are model-relative (2026-09-24T03:12:21+00:00)

### sandbox-backend
- `Windows 'appcontainer' backend: one fixed zero-capability AppContainer identity (no network, loopback included), suspended CreateProcess into a kill-on-close Job Object capped at 512 processes. Grants only the workspace (full, tree) and the go env GOMODCACHE tree (read-execute, covers the auto-selected toolchain). Commands start in a drive letter mapped to the workspace (DefineDosDevice, pushed per scope and popped on close, shared across scopes/processes by stacking); no grants on any folder above the workspace and no administrator step. The model is told commands see the workspace as that drive. Opt-in via /set sandbox and -sandbox; -revoke-sandbox removes all grants (elevating only to remove a legacy C:\Users grant) and the identity.` — Measured on a controlled tree: PowerShell needs read-attributes and git/MSYS needs list+traverse+read-attributes on every parent directory of the working directory. Under C:\Users that would require an admin grant on C:\Users and would expose the names of everything in the home folder. The isolatedWin32 capabilities are rejected for plain AppContainers. A drive root has no parents, so the drive letter gives full PowerShell and git functionality (incl. stash create for checkpoints) with strictly less exposure; the user chose it over the list-grant option. Negative control: launching at the host path instead of the drive fails git and PowerShell even with the earlier grants present. (2026-09-23T03:35:40+00:00)

### sandbox-backend-macos
- `macOS isolated backend = Seatbelt via /usr/bin/sandbox-exec, wrapping the local backend: deny-default SBPL profile; reads limited to system dirs + workspace + private scratch + go env GOROOT/GOMODCACHE; writes only workspace + scratch; no network; HOME/TMPDIR/GOCACHE in scratch; paths passed as -D params; Available probes a deny-default profile; nothing persistent to revoke. Profile generation is untagged (tested on every OS); containment tests are darwin-only and not yet run on a Mac` — Mirror the Windows AppContainer grant set; home unreadable so ~/.ssh etc. stay out; sandbox-exec is deprecated but used by Codex/Bazel/Chromium and fails closed via Available if removed (2026-09-24T03:31:32+00:00)

### sandbox-opt-in
- `The sandbox is opt-in per session: RunRequest.Sandbox, /set sandbox on|off in both terminal modes (persisted in InteractiveRuntime), and -sandbox on the CLI; -revoke-sandbox undoes grants. Child agents inherit Sandbox and cannot opt out. sandboxOptions registers the platform IsolatedBackend and sets RequireIsolation, so an unavailable backend fails the run instead of falling back; /set sandbox on is refused where none exists.` — User chose opt-in. The first sandboxed open blocks for a one-time module-cache grant and git does not yet work inside the container, so defaulting it on would silently break checkpoints; inheritance keeps a child from escaping a parent's sandbox (children inherit or narrow). (2026-09-23T03:14:45+00:00)

### scoped-grants
- `Monitor invariant I9: Authorize takes a ConsentRequest{Tool, Summary, Scope}; session grants cover a GrantScope, not a tool: 'program subcommand' commands grant that prefix, everything else (shell syntax incl. newline, interpreters/wrappers) is exact-only; writes grant the file's directory tree, root-level files alone; other tools stay tool-wide` — Kernel gap #4: HasGrant matched tool names, so approving 'go test' for the session approved 'curl | sh'. Structured consent replaced summary-string parsing; a test caught newline-collapsing normalization letting 'go test\ncurl evil' match a go test grant (2026-09-24T02:35:26+00:00)

### undo-journal
- `/undo is a WriteJournal of model file-tool writes: pre-image before the first write per path per top-level run (children share the group), hash of the model's last write after; Undo reverts the latest group, deletes created files, skips files changed since (force overrides); in-memory per session (TUI resets on /dir), 10MB per file, 256MB total; git CreateCheckpoint/Rollback removed` — P2 #11: TUI /undo never worked (checkpoints were created on the runner's throwaway manager) and the legacy one ran reset --hard + clean -fd, destroying the user's own uncommitted work; journaling only the model's writes is precise and works outside git (2026-09-24T03:17:01+00:00)

### untrusted-text-rendering
- `Model, tool and program text is sanitized in the formatters that take raw text (FormatMarkdownWidth, FormatToolResult, FormatToolCall, FormatTerminalBox, change rows, /ps) before styling: escapes and C0/C1 controls stripped, command output keeps SGR colour only, bidi/tag characters always shown as markers; FormatPermissionPrompt reveals every hidden character and warns (I7)` — Sanitizing at formatter entry covers live, replayed and legacy-REPL output in one place each without altering tool arguments used for logic; our own ANSI is added after, so it is never confused with untrusted escapes (2026-09-24T02:28:37+00:00)

## osu-cluster-scripts

### obsolete-model-script-cleanup
- `Archive model-specific scripts when their checkpoints and derived artifacts are absent; retain generic launchers and recoverable diagnostics.` — A dated archive removes active-directory clutter without irreversible deletion; generic scripts and diagnostics remain useful across models. (2026-09-20T14:54:49+00:00)
