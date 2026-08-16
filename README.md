# fastllm

A minimal, fast AnythingLLM-style chat app: Go backend (streaming chat + RAG),
React frontend, SQLite storage, in-memory vector search. Ships as a single
binary with the frontend embedded.

Features: model selection (auto-discovered from Ollama), retrieval-augmented
chat with per-answer source attribution, pasted-text and file indexing,
saved skills (named system-prompt presets), and Markdown/LaTeX/syntax-
highlighted chat rendering.

## Architecture

- `cmd/server` — entry point, wires everything together
- `internal/llm` — OpenAI-compatible client (works with OpenAI, Ollama, LM Studio)
- `internal/store` — SQLite persistence (messages, documents, chunks)
- `internal/vector` — in-memory cosine-similarity search over chunk embeddings
- `internal/chat` — HTTP handlers, including SSE-streamed chat responses
- `web` — React (Vite) frontend, embedded into the binary via `go:embed`

## Run it locally with Ollama (free, local models)

```
ollama pull llama3.1
ollama pull nomic-embed-text
```

Ollama exposes an OpenAI-compatible API on `localhost:11434/v1` by default,
which is what the server points to out of the box.

## Development

Backend (hot-reload on save isn't wired up; restart manually or add `air`):

```
go run ./cmd/server
```

Frontend (dev server proxies /api to :8080):

```
cd web
npm install
npm run dev
```

## Production build (single binary)

```
cd web && npm install && npm run build && cd ..
go build -o fastllm.exe ./cmd/server
./fastllm.exe
```

Visit http://localhost:8080.

## Configuration (env vars)

| Var | Default | Purpose |
|---|---|---|
| `FASTLLM_ADDR` | `:8080` | HTTP listen address |
| `FASTLLM_DB` | `fastllm.db` | SQLite file path |
| `LLM_BASE_URL` | `http://localhost:11434/v1` | OpenAI-compatible API base |
| `LLM_API_KEY` | (empty) | Bearer token, if required |
| `LLM_CHAT_MODEL` | `llama3.1` | Chat completion model |
| `LLM_EMBED_MODEL` | `nomic-embed-text` | Embedding model |

To use OpenAI instead: set `LLM_BASE_URL=https://api.openai.com/v1`,
`LLM_API_KEY=sk-...`, `LLM_CHAT_MODEL=gpt-4o-mini`, `LLM_EMBED_MODEL=text-embedding-3-small`.

## Notes on scale

The vector store is a linear-scan in-memory index — fine for thousands of
chunks, not millions. Swap `internal/vector` for Qdrant or `sqlite-vec` if
you outgrow it; the `Store` interface usage in `internal/chat/handler.go`
is the only place that would need to change.
