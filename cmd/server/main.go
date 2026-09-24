// Command server runs the fastllm backend: a chat API backed by an
// OpenAI-compatible LLM, SQLite persistence, and an in-memory vector
// index for retrieval-augmented answers. It is headless — the terminal
// UI it launches by default replaced the browser frontend. All the wiring
// (DB, LLM client, routes) lives in internal/appserver.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"fastllm/internal/appserver"
	"fastllm/internal/harness"
	"fastllm/internal/legacystore"
)

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	// The TUI needs the legacy-database opener installed before it runs;
	// without it /conversations and /import report the feature as missing.
	harness.OpenLegacyConversations = legacystore.Installer()

	// If explicit server command is given, start HTTP backend server
	if len(os.Args) > 1 && os.Args[1] == "server" {
		runServer(os.Args[2:])
		return
	}

	// If explicit run or harness command is given, pass remaining args
	if len(os.Args) > 1 && (os.Args[1] == "run" || os.Args[1] == "harness" || os.Args[1] == "tui") {
		os.Exit(harness.RunCLI(os.Args[2:]))
	}

	// Otherwise, default to launching the interactive TUI REPL
	os.Exit(harness.RunCLI(os.Args[1:]))
}

func runServer(args []string) {

	// Loopback by default: the API can run commands, so reaching it from the
	// network must be a deliberate choice (FASTLLM_ADDR=0.0.0.0:8080).
	addr := getenv("FASTLLM_ADDR", "127.0.0.1:8080")

	cfg, err := appserver.ConfigFromEnv()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	tokenPath, err := appserver.DefaultTokenPath()
	if err != nil {
		log.Fatalf("locate server token: %v", err)
	}
	if cfg.Token, err = appserver.LoadOrCreateToken(tokenPath); err != nil {
		log.Fatalf("load server token: %v", err)
	}

	built, err := appserver.Build(cfg)
	if err != nil {
		log.Fatalf("build server: %v", err)
	}
	defer built.DB.Close()

	server := &http.Server{Addr: addr, Handler: built.HTTP}
	built.Mux.HandleFunc("POST /api/quit", quitHandler(server))

	log.Printf("fastllm listening on %s (llm backend: %s)", addr, cfg.LLMBaseURL)
	tokenSource := tokenPath
	if os.Getenv("FASTLLM_TOKEN") != "" {
		tokenSource = "FASTLLM_TOKEN"
	}
	log.Printf("requests need the token from %s (Authorization: Bearer <token>); runs are capped at %q mode, working_dir within %v",
		tokenSource, cfg.MaxMode, built.Handler.AllowedRoots)
	if !appserver.IsLoopbackAddr(addr) {
		log.Printf("WARNING: %s accepts connections from other machines. Anyone with the token can run tasks here; set FASTLLM_ALLOWED_HOSTS to the names clients will use.", addr)
	}
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

// quitHandler lets the frontend request a clean shutdown (used by the
// titlebar Quit button). Responds first, then shuts down from a goroutine so the
// response actually reaches the browser before the process exits.
func quitHandler(server *http.Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		go func() {
			time.Sleep(200 * time.Millisecond)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			server.Shutdown(ctx)
			os.Exit(0)
		}()
	}
}
