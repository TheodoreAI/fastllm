// Command server runs the fastllm backend: a chat API backed by an
// OpenAI-compatible LLM, SQLite persistence, and an in-memory vector
// index for retrieval-augmented answers. It also serves the built
// React frontend from web/dist when present. All the actual wiring
// (DB, LLM client, routes) lives in internal/appserver, shared with
// cmd/desktop's Wails-based native app.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"fastllm/internal/appserver"
	"fastllm/internal/harness"
	"fastllm/internal/lsp"
	"fastllm/internal/terminal"
)

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "run" || os.Args[1] == "harness") {
		os.Exit(harness.RunCLI(os.Args[2:]))
	}

	addr := getenv("FASTLLM_ADDR", ":8080")

	cfg, err := appserver.ConfigFromEnv()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	built, err := appserver.Build(cfg)
	if err != nil {
		log.Fatalf("build server: %v", err)
	}
	defer built.DB.Close()

	// Settings → Terminal's "point local AI CLIs at fastllm" toggle (see
	// terminal.BaseURLHolder) needs this server's own loopback address —
	// not known until now, since addr can bind any interface (":8080"
	// binds all of them) while a spawned shell always reaches this same
	// process via 127.0.0.1 regardless of what it's bound to.
	if _, port, err := net.SplitHostPort(addr); err == nil && port != "" {
		built.TerminalBaseURL.Set(fmt.Sprintf("http://127.0.0.1:%s", port))
	}

	server := &http.Server{Addr: addr, Handler: built.Mux}
	built.Mux.HandleFunc("POST /api/quit", quitHandler(server, built.TerminalRegistry, built.LSPRegistry))

	log.Printf("fastllm listening on %s (llm backend: %s)", addr, cfg.LLMBaseURL)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

// quitHandler lets the frontend request a clean shutdown (used by the
// titlebar Quit button) — this is a locally-run desktop-style app with no
// remote exposure, so no auth is needed beyond it already listening on
// localhost. Responds first, then shuts down from a goroutine so the
// response actually reaches the browser before the process exits.
func quitHandler(server *http.Server, terminalRegistry *terminal.Registry, lspRegistry *lsp.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		go func() {
			time.Sleep(200 * time.Millisecond)
			terminalRegistry.CloseAll()
			lspRegistry.CloseAll()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			server.Shutdown(ctx)
			os.Exit(0)
		}()
	}
}
