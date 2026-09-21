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
- `internal/harness` — autonomous tool loop, workspace rules, checkpoints, and TUI
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
writes and commands), `read-only`, and `auto`. Direct Shell Mode commands are
already explicit user actions and do not prompt again.

Use `/image <prompt>` to call the configured image-generation endpoint. FastLLM
selects `qwen-image` when present, otherwise the first configured model whose ID
or name contains `image`. The endpoint must implement OpenAI's
`POST /v1/images/generations` shape and return `data[].b64_json`. FastLLM decodes
and validates the response, then writes the image under `generated-images/` in
the active workspace; base64 is never added to session history.

## Production build (single binary)

```
go build -o fastllm.exe ./cmd/server
```

One binary does both jobs: run it with no arguments for the terminal UI, or
with the `server` subcommand for the headless API on http://localhost:8080.
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
| `FASTLLM_ADDR` | `:8080` | HTTP listen address |
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

