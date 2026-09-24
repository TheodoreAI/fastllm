# fastllm

A local LLM chat app and autonomous coding harness. A single binary
(`cmd/server`) is both: run it with no arguments for the interactive terminal
UI, or with the `server` subcommand for a headless HTTP backend that streams
OpenAI-compatible chat and agent runs and stores conversations in SQLite.

FastLLM can use Ollama and configured cloud providers, expose OpenAI- and
Anthropic-compatible proxy endpoints, run coding tasks against a sandboxed
workspace, execute commands when enabled, and search or fetch the public web.
It also includes an interactive terminal UI and a one-shot CLI runner.

## Architecture

- `cmd/server` — the single binary: terminal UI by default, HTTP API with the `server` subcommand
- `internal/llm` — local and cloud model clients and routing
- `internal/store` — SQLite persistence for conversations, settings, and notes
- `internal/chat` — chat, compatibility API, and SSE agent handlers
- `internal/harness` — autonomous tool loop, permission monitor, workspace rules, undo journal, and TUI
- `internal/webtools` — public-web search and SSRF-protected page fetching

## Run it locally with Ollama (free, local models)

```
ollama pull llama3.1
ollama pull nomic-embed-text
```

Ollama exposes an OpenAI-compatible API on `localhost:11434/v1` by default,
which is what the server points to out of the box.

## Development and verification

Backend (hot-reload on save isn't wired up; restart manually or add `air`).
`cmd/server` starts the interactive terminal UI by default, so pass `server`
to start the HTTP application:

```
go run ./cmd/server server
```

Terminal UI:

```
go run ./cmd/server
```

Run the checks used before committing:

```
go test ./...
go vet ./...
gofmt -l .
```

The permission monitor's invariants (the I1-I10 list in
`internal/harness/monitor.go`) have property tests in
`internal/harness/fuzz_test.go`. `go test` replays their seeds and every input
the fuzzer has ever failed on (`internal/harness/testdata/fuzz`). To search for
new violations after changing the monitor, grants, path protection, or the
text sanitizer, fuzz each target for a while:

```
go test ./internal/harness -run '^$' -fuzz '^FuzzMonitorInvariants$' -fuzztime 60s
go test ./internal/harness -run '^$' -fuzz '^FuzzApprovalCannotOverrideDenial$' -fuzztime 60s
go test ./internal/harness -run '^$' -fuzz '^FuzzCommandGrantCoverage$' -fuzztime 60s
go test ./internal/harness -run '^$' -fuzz '^FuzzPathGrantCoverage$' -fuzztime 60s
go test ./internal/harness -run '^$' -fuzz '^FuzzSanitizers$' -fuzztime 60s
```

A failure writes its input under `testdata/fuzz`; commit it with the fix so it
stays a regression test.

## Agent CLI

Run a one-shot task in the current workspace:

```
go run ./cmd/server --task "inspect the project and fix the failing tests" --dir .
```

Omit `--task` for the interactive TUI. Useful flags include `--model`,
`--max-turns`, `--timeout`, `--think`, and `--no-commands`. The agent can read,
write, patch, regex-search, and glob files; run commands and manage background
processes; delegate bounded work to asynchronous child agents; search the web;
fetch public pages; and report a final result. Child-agent tools support spawn,
status/result inspection, follow-up messages, and cancellation. Delegation is
limited to three concurrent children and two levels deep. The TUI header and
`/status` show known/active child counts and aggregate child token usage;
`agent_status` reports per-agent tokens and results.
When inspecting a specific active child, it waits up to 60 seconds for completion
instead of encouraging rapid model-driven polling; pass `wait_seconds: 0` when an
immediate snapshot is actually needed.

Workspace access is confined to the selected directory. Web fetching rejects
loopback, private, multicast, and link-local destinations.

Interactive sessions autosave under `~/.fastllm/sessions`. Use `/sessions` to
list them, `/resume <id>` to reopen one, and `/new` to start fresh. Runtime
settings can be changed without restarting via `/set turns`, `/set timeout`,
`/set think`, `/set commands`, and `/set permissions`; `/set` displays their
current values. Permission modes are `ask` (the default for model-initiated
writes and commands), `read-only`, and `auto`. Direct shell-mode commands are
already explicit user actions and do not prompt again.

Skills are discovered from project and user-level Codex, OpenCode, Claude,
`.agents`, and legacy AGY `.agent` skill directories. Use `/skills` or
`/skills list` to list them, `/skills NAME` to show a skill's details, or
`/skills NAME TASK` to apply it to one agent run. Project definitions override
global definitions; nearer project definitions and `.agents` sources take
precedence. Available names and descriptions are advertised to the model, but
full skill instructions are loaded only for an explicitly invoked run.
In the full-screen TUI, listing skills opens a scrollable modal; use arrow keys
or `j`/`k` to navigate, Enter to stage the highlighted skill as a `/skills NAME`
prompt ready for its task, and Esc or `q` to dismiss. The status bar also
shows current context usage against the automatic compaction threshold, turning
yellow at 75% and red at 90%.

Compaction is sized from the active model's context window rather than a fixed
character count, so a 200k-token model is not trimmed as aggressively as an 8k
one. The window is resolved per endpoint: an explicit `context_window` (in
tokens) in `config.json` wins, then a built-in table keyed on the model ID, then
a conservative 32k default. Set it explicitly for a self-hosted endpoint whose
model the table cannot recognize, or for a vLLM server started with a reduced
`--max-model-len`. Compaction runs at 75% of the window, leaving room for the
reply and tool schemas. `/compact` collapses older turns on demand without
waiting for that threshold.

Settings come from `~/.fastllm/config.json`, or from a workspace's own
`.fastllm/config.json` once you trust it. A workspace config ships with the
repository, so whoever wrote the repository wrote it: an endpoint there
receives your conversation, and an `api_key_file` there can send one of your
own keys with it. Until you run `/trust`, fastllm ignores it, uses your global
config, and shows what it would change. Trust is recorded by path and exact
contents in `~/.fastllm/trusted-configs.json`, so any later edit (a pull, say)
makes it untrusted again. `/untrust` reverses it. A config that fastllm itself
writes for you, such as through `/models add`, is trusted as written.
Headless runs never trust on their own; set `FASTLLM_TRUST_PROJECT_CONFIG=1`
where the repository is yours.

```json
{
  "id": "muse-glimmer",
  "url": "http://127.0.0.1:8010/v1",
  "context_window": 32768,
  "parameters": { "temperature": 0.2, "max_tokens": 4096 }
}
```

Use `/image <prompt>` to call the configured image-generation endpoint. FastLLM
selects `qwen-image` when present, otherwise the first configured model whose ID
or name contains `image`. The endpoint must implement OpenAI's
`POST /v1/images/generations` shape and return `data[].b64_json`. FastLLM decodes
and validates the response, then writes the image under `generated-images/` in
the active workspace; base64 is never added to session history.

## Permission modes

The harness treats the model as an untrusted process whose only system calls
are tool calls. Every call is decided by one reference monitor
(`internal/harness/monitor.go`). The tool list the model is offered, dispatch,
the subprocess broker, and the file writer are all views of that one decision,
so a prompt shapes behaviour but never grants anything.

| mode | reads | web | file edits | commands / processes | child agents |
|------|-------|-----|------------|----------------------|--------------|
| **plan** | yes | no | no | no | no |
| **agent** | yes | yes | ask | ask | ask |
| **edit** | yes | yes | yes | no | no |
| **full** | yes | yes | yes | yes | yes |

- **plan** explores read-only with no network, since a fetched URL can carry
  workspace contents out. It ends with `submit_plan`. The TUI then offers
  **Approve → Agent / Edit / Full-Access** or **Keep planning**. Approving
  switches the mode and starts a turn that carries out the plan. The model
  itself has no way to change the mode.
- **agent** asks before every mutating call: once, for the session, or deny.
  "For the session" covers what was approved, not the whole tool. The prompt
  says exactly what it grants:
  - A command of the form `program subcommand …` grants that prefix, so
    approving `go test ./...` also allows `go test -run X`.
  - Any other command is granted only exactly as written. That includes
    commands with shell syntax (`; & | $ ( ) < >`, backticks, newlines) and
    commands run through an interpreter or wrapper (`bash`, `python`, `sudo`,
    `env`, …).
  - A file edit grants its directory and everything below it. A file at the
    workspace root is granted alone.
  A child agent of an agent-mode run cannot ask, so it runs as plan.
- **edit** applies file edits without asking but never starts a process. This
  includes fused `then_run` follow-ups, and a refused `then_run` also blocks its
  edit.
- **full** allows everything the run's capabilities permit, without asking.

Some paths are protected in every mode, because writing them changes the
harness rather than the project:

- **`.git/`** is never written by file tools. Git runs commands named in its
  own files (`core.fsmonitor`, hooks, diff drivers), and the harness runs git in
  the background, so a write there would let edit mode start a process. The
  file layer refuses it again after resolving symlinks, case, and Windows short
  names, and the harness's own git calls pass `-c core.fsmonitor=false`,
  `--no-ext-diff`, and `--no-textconv` so config that is already present cannot
  run either.
- **fastllm configuration** (`.fastllm/`, rule files such as `AGENTS.md` and
  `CLAUDE.md`, and skills directories) always asks, even in edit and full, under
  its own label so a session grant for ordinary writes does not cover it. A run
  that cannot ask is refused.

Secrets get their own rule, because the web tools are a way out. Reading a
credentials file (`.env` and `.env.*` other than templates such as
`.env.example`, `*.pem`, `*.key`, SSH private keys, `.npmrc`, `.netrc`,
`.git-credentials`, and anything under `.ssh/`, `.aws/`, `.kube/`, and similar)
asks in every mode, judged by where the path really leads, so a symlink cannot
disguise one. A run that cannot ask is refused. `search_files` never scans
those files. Once one has been read, every `web_fetch` and `web_search` asks,
in every mode, showing the full URL or query and naming what was read. Each
approval covers only that exact request. This lasts as long as the secret can
still be in the conversation: until `/new`, `/clear`, or a resume, and child
agents share it with their parent. These rules cover the model's file and web
tools; a shell command you have allowed can still read and send anything, which
only the sandbox contains.

Text from the model, tools, and the programs they run is treated as untrusted
on screen too. Before the UI styles it, terminal escape sequences (cursor moves,
clipboard writes, title changes) and control characters are removed; command
output keeps only its colours. Bidi overrides and invisible tag characters,
which make text read differently from what it is, are shown as `⟨U+202E⟩`
markers. Approval prompts go further: every hidden or control character in the
request, including a carriage return that would overwrite the start of a
command, is spelled out, and a warning says the raw text is what will run.

In the TUI, **Shift+Tab** cycles plan → agent → edit → full. The header badge
shows the current mode. `/set permissions <mode>` works in both terminal modes.
Neither can change the mode while a turn is running.

Fail-closed defaults:

- An empty or unknown mode is **plan**.
- A one-shot `-task` run and a `POST /api/harness/run` without
  `permission_mode` therefore cannot change anything. Pass `-mode edit` or
  `-mode full` (or `"permission_mode"`) for unattended changes.
- A headless `agent` run has nobody to ask, so each of its asks is denied.
- The old names `ask`, `read-only`, and `auto` are accepted as agent, plan,
  and full.

## Undo

`/undo` reverts the file changes the model made during your last prompt, and
nothing else. Before the first change a prompt makes to a file, fastllm keeps
the file's previous contents (or notes that it did not exist). Undo puts them
back and deletes files the prompt created. Run it again to go back another
prompt. A file you have edited since the model wrote it is left alone and
listed; `/undo force` reverts it too. This works in any directory, git
repository or not, and never runs `git reset` or `git clean`, so your own
uncommitted work is safe. It covers changes made through the file tools, not
changes a shell command makes, and it lasts for the session (files over 10 MB
are not kept).

## Budgets

A model can loop, so every run has resource limits, like rlimits for a process.
A run is one prompt in the TUI, or one `-task` or API call. It includes every
tool turn and child agent: children draw on their parent's budget, so
delegating work cannot reset it.

| Limit | Default | Set with |
|---|---|---|
| wall-clock time | 30m | `/set duration 45m`, `-max-duration`, `"budget":{"max_duration":...}` |
| tokens | unlimited | `/set tokens 200k`, `-max-tokens` |
| estimated cost | unlimited | `/set cost 2.50`, `-max-cost` |
| file changes | 500 per run, 50 MB written | `"budget":{"max_writes":...,"max_write_bytes":...}` |
| web requests | 100 per run | `"budget":{"max_web_requests":...}` |
| background processes | 8 running at once | fixed |

`off` (or a negative number in the API) removes a limit. Time is enforced as a
deadline on everything the run does. Tokens and cost are checked before each
model call, and the run stops with the reason. With a cost limit set, a billable
model whose price is unknown stops the run rather than going unmetered. When
file changes or web requests run out, the tool call is refused with the reason
and the model is told to wrap up. `/set` shows the current budget, and it is
saved with the session.

## Agent permissions

`spawn_agent` accepts `capabilities` containing `read`, `write`, `network`,
`commands`, and `delegate`. Omitted or null lists inherit the parent's list;
explicit lists are intersected with it. An empty list (`[]`) grants no optional
tools. Workspace reads remain available in all cases. At the root, an omitted
list preserves the normal tools allowed by the permission mode and command setting.
Aliases such as `filesystem_write`, `web`, `shell`, and `spawn_agent` are supported.

`network_policy: "none"` disables model web tools and shell execution. Children
inherit this restriction and cannot relax it to `"public"`. Public-web tools
continue to reject private/local destinations. These policies govern model tools;
they do not block requests to the configured LLM endpoint.

Model shells are **not OS-sandboxed** by default: they can write files and make
network requests. `/set sandbox on` (or `-sandbox`) runs them isolated, with no
network and no access outside the workspace, on Windows (AppContainer) and macOS
(Seatbelt); see `docs/execution-boundary.md`. Consequently, `commands` also requires both `write` and `network`;
with either denied, foreground commands, background commands, and fused
`then_run` commands are blocked. A blocked fused command also prevents its file
mutation. When shells are enabled, environment filtering removes variables whose
names look sensitive, but does not isolate the process or protect credentials
stored on disk. Direct user shell commands (`!cmd`, `$ cmd`, `/shell`) retain
the user's authority.

Every decision the monitor makes is appended to an audit log, one JSON object
per line: the tool, the full effect (file bodies are sized, not copied), a
SHA-256 of the arguments, whether it was allowed, and what decided it: the mode
table, a capability, a protected path, no one to ask, or the user. For user
decisions it also records how: approved once, approved for the session with the
grant ID it created, covered by an existing grant, or denied. Terminal sessions
log to `~/.fastllm/audit/<session-id>.jsonl`, `-task` runs to
`task-<time>.jsonl`, and the API server to `server-<date>.jsonl`
(`FASTLLM_AUDIT_DIR` moves them). Child agents log into their parent's file.
`/audit [n]` shows the latest decisions in the TUI. The log sits outside every
workspace, so file tools cannot touch it; it is append-only by convention, not
cryptographically sealed.

Both terminal UIs support `/permissions [list]`,
`/permissions revoke <grant-id|tool>`, and `/permissions clear`. In `agent` mode,
session approvals are tracked with IDs and workspace scope. Revocation makes
subsequent calls prompt again; it does not undo work or stop already running
commands. Directory, session, and permission-mode changes clear grants. Grants
are never persisted. Tool catalogs and dispatch checks enforce capability
restrictions independently of interactive approvals.

## Server security

The API is the harness's control plane: a request chooses its own permission
mode, working directory, and command access. The reference monitor cannot
defend against a caller who is allowed to act as the user, so the server
decides who that is:

- It listens on `127.0.0.1` unless `FASTLLM_ADDR` says otherwise, and warns
  when bound to a non-loopback address.
- Every route except `GET /` requires the token from `~/.fastllm/server-token`
  (created at 0600 on first start) as `Authorization: Bearer <token>` or
  `x-api-key: <token>`. Point external tools that use `/v1/chat/completions`
  or `/v1/messages` at the server with this token as their API key.
- Requests with a body must be `application/json`; requests carrying an
  `Origin` header, or a `Host` that is not loopback or listed in
  `FASTLLM_ALLOWED_HOSTS`, are refused. Browser pages therefore cannot call
  the API, including through DNS rebinding.
- A harness run may not request a mode above `FASTLLM_MAX_MODE` (default
  `edit`, which never starts processes), and its `working_dir` must lie inside
  `FASTLLM_ALLOWED_ROOTS`.

```
curl -H "Authorization: Bearer $(cat ~/.fastllm/server-token)"      -H "Content-Type: application/json"      -d '{"task":"summarise README.md"}' http://127.0.0.1:8080/api/harness/run
```

## Production build (single binary)

```
go build -o fastllm.exe ./cmd/server
```

One binary does both jobs: run it with no arguments for the terminal UI, or
with the `server` subcommand for the headless API on http://127.0.0.1:8080
(token required; see [Server security](#server-security)).
There is no browser UI. `build-all.sh` / `build-all.bat` wrap this.

`cmd/server/rsrc_windows_*.syso` embed the app icon (`cmd/server/icon/fastllm.ico`)
into `fastllm.exe` on Windows builds — `go build` picks them up automatically,
nothing extra to run. Regenerate them after changing the icon with:

```
cd cmd/server
go run github.com/tc-hib/go-winres@latest simply --icon icon/fastllm.ico
```

## Launching like a regular app (Windows)

Double-click `start-fastllm.vbs` (or the Desktop shortcut it's used to create)
to start the headless API server with no console window. It:

- detects an already-running `fastllm.exe` and checks whether the exe file
  on disk is newer than that process's start time (i.e. it was rebuilt
  while running) — if current, it reports the port; if stale, it stops the
  process first so the next step actually picks up the new build. A port
  held by something other than our own exe is left untouched.
- checks whether Go source is newer than the last build (comparing file
  timestamps) and rebuilds only when stale; otherwise it skips straight to
  launch, no build overhead
- if a rebuild fails, it prints the build output and pauses instead of
  launching a stale/missing binary

See `start.bat` for the underlying logic. For interactive use, run
`fastllm` with no arguments rather than this launcher.

## Configuration (env vars)

| Var | Default | Purpose |
|---|---|---|
| `FASTLLM_ADDR` | `127.0.0.1:8080` | HTTP listen address (loopback only by default) |
| `FASTLLM_TOKEN` | (from `~/.fastllm/server-token`) | API token; overrides the generated file |
| `FASTLLM_MAX_MODE` | `edit` | Highest `permission_mode` an API run may request |
| `FASTLLM_ALLOWED_ROOTS` | files root, else server's cwd | `working_dir` must be inside one of these (path-list separated) |
| `FASTLLM_ALLOWED_HOSTS` | (none) | Extra `Host` names accepted besides loopback, comma separated |
| `FASTLLM_AUDIT_DIR` | `~/.fastllm/audit` | Where permission audit logs are written |
| `FASTLLM_TRUST_PROJECT_CONFIG` | (unset) | `1` trusts every workspace `.fastllm` config (CI only) |
| `FASTLLM_TRUST_STORE` | `~/.fastllm/trusted-configs.json` | Where workspace-config trust is recorded |
| `FASTLLM_DB` | `fastllm.db` | SQLite file path |
| `LLM_BASE_URL` | `http://localhost:11434/v1` | OpenAI-compatible API base |
| `LLM_API_KEY` | (empty) | Bearer token, if required |
| `LLM_CHAT_MODEL` | `llama3.1` | Chat completion model |
| `FASTLLM_FILES_ROOT` | (disabled) | Initial sandboxed file-access root |
| `FASTLLM_FILES_WRITE` | (disabled) | Enable initial file writes when non-empty |
| `BRAVE_SEARCH_API_KEY` | (empty) | Prefer Brave Search; otherwise use DuckDuckGo |

To use an OpenAI-compatible endpoint instead, set `LLM_BASE_URL`,
`LLM_API_KEY`, and `LLM_CHAT_MODEL`. Additional cloud-provider credentials can
be configured from the Settings panel.

## License

This project is licensed under the Apache License 2.0. See the [LICENSE](LICENSE) file for details.

