package lsp

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strings"

	"fastllm/internal/files"

	"github.com/coder/websocket"
)

// NewHandler returns the /api/editor/lsp/ws handler. Unlike
// terminal.NewHandler, the route is only ever registered when
// lsp.Available() found gopls at startup (see appserver.Build) — there's
// no live-toggleable "LSP enabled" setting the way Settings → Terminal
// has, so there's nothing for an always-registered-but-gated route to
// preserve; a missing gopls just means the route doesn't exist, same as
// any other unimplemented endpoint.
//
// Every connection must still originate from loopback, same requirement
// and same reasoning as terminal.NewHandler: FASTLLM_ADDR can bind every
// interface, and gopls (like a shell) has no sandboxing of its own.
//
// fileReader supplies gopls's workspace root — whatever folder is
// currently opened in the Editor, read fresh on every connect so a
// folder change picked up between connects is honored.
func NewHandler(registry *Registry, fileReader *files.Reader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isLoopback(r.RemoteAddr) {
			http.Error(w, "lsp is only reachable from localhost", http.StatusForbidden)
			return
		}
		if !fileReader.Enabled() {
			http.Error(w, "file access is not enabled", http.StatusForbidden)
			return
		}
		root := fileReader.GetRoot()

		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			// Same allowlist, same reasoning, as terminal.NewHandler's
			// OriginPatterns — see that doc comment for the full
			// Wails-webview explanation (wails.localhost / bare "wails" /
			// the *:* wildcards for `wails dev`'s dynamic port).
			OriginPatterns: []string{"wails.localhost", "wails.localhost:*", "wails", "localhost", "localhost:*", "127.0.0.1:*"},
		})
		if err != nil {
			log.Printf("lsp: websocket accept failed: %v", err)
			return
		}

		ctx := r.Context()
		writeToConn := func(msg json.RawMessage) error {
			return conn.Write(ctx, websocket.MessageText, msg)
		}

		session, err := Start(root, func(method string, params json.RawMessage) {
			b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
			if err != nil {
				return
			}
			_ = writeToConn(b)
		})
		if err != nil {
			log.Printf("lsp: failed to start gopls: %v", err)
			conn.Close(websocket.StatusInternalError, "spawn failed")
			return
		}
		registry.Swap(session)
		defer registry.Swap(nil)

		// The initialize/initialized handshake happens here, server-side,
		// before the relay loop starts — web/src/lsp.js never sends
		// initialize itself, keeping the frontend client free of LSP
		// bootstrapping.
		if _, err := session.Call(ctx, "initialize", initializeParams(root)); err != nil {
			log.Printf("lsp: initialize failed: %v", err)
			conn.Close(websocket.StatusInternalError, "initialize failed")
			return
		}
		if err := session.Notify("initialized", map[string]any{}); err != nil {
			log.Printf("lsp: initialized notify failed: %v", err)
		}

		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				break
			}
			// Every inbound frame is bare JSON-RPC — didOpen/didChange/
			// didClose (notifications) or definition/completion/hover
			// (requests) — Forward tells them apart by whether "id" is
			// present, same rule the JSON-RPC spec itself uses, so this
			// handler never needs its own per-method dispatch.
			if err := session.Forward(data, writeToConn); err != nil {
				log.Printf("lsp: forward failed: %v", err)
			}
		}

		session.Close()
		conn.Close(websocket.StatusNormalClosure, "")
	}
}

// initializeParams declares just enough client capability for the four
// v1 features (diagnostics push, completion, hover, go-to-definition) —
// gopls tailors what it sends/accepts based on what the client claims to
// support here.
func initializeParams(root string) map[string]any {
	return map[string]any{
		"processId": nil,
		"rootUri":   pathToFileURI(root),
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"synchronization":    map[string]any{"didSave": true},
				"publishDiagnostics": map[string]any{},
				"completion":         map[string]any{"completionItem": map[string]any{"snippetSupport": false}},
				"hover":              map[string]any{"contentFormat": []string{"plaintext", "markdown"}},
				"definition":         map[string]any{},
			},
		},
	}
}

// pathToFileURI converts a filesystem path to a file:// URI. Windows
// drive-letter paths (C:\foo\bar) need a leading slash before the drive
// letter once backslashes are normalized to forward slashes
// (file:///C:/foo/bar) — gopls (like every other LSP server) expects
// that exact shape on Windows.
func pathToFileURI(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return "file://" + p
}

func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
