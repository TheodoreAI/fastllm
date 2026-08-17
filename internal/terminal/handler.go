package terminal

import (
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"

	"fastllm/internal/files"

	"github.com/coder/websocket"
)

const (
	defaultCols = 80
	defaultRows = 24
)

// controlMessage is sent by the client as a text frame to request a resize.
// PTY input itself always arrives as binary frames (raw keystrokes), so a
// text frame unambiguously means "this is a control message, not input."
type controlMessage struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// NewHandler returns the /api/terminal/ws handler. The route is always
// registered (net/http.ServeMux can't un-register a route once the server
// is running), so gate.Enabled() — backed by the live-toggleable Settings
// UI value — is the actual enforcement point for whether the feature is
// on at all. Disabled looks identical to "this route doesn't exist" (404)
// rather than a 403, revealing nothing about the feature's existence.
//
// Independently of that gate, every request must also originate from
// loopback. FASTLLM_ADDR can bind to all interfaces (its default, ":8080",
// does exactly that) with no enforcement anywhere else in this app that
// the caller is local — a shell endpoint is the one place that gap can't
// be inherited silently. Both checks must pass; neither substitutes for
// the other.
//
// fileReader supplies the session's starting directory: whatever folder
// is currently opened in the Editor (files.Reader.GetRoot()), so the
// terminal lands somewhere relevant instead of the server's own launch
// directory. Read fresh on every connect (not passed once at startup)
// since the opened folder can change live via Settings → File access
// without a restart, same as the reader itself.
func NewHandler(registry *Registry, gate *Gate, fileReader *files.Reader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !gate.Enabled() {
			http.NotFound(w, r)
			return
		}
		if !isLoopback(r.RemoteAddr) {
			http.Error(w, "terminal is only reachable from localhost", http.StatusForbidden)
			return
		}

		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			// The default (no OriginPatterns) already restricts to the
			// request's own host, which is correct here — this frontend
			// and this API are always same-origin. Left unset deliberately
			// rather than opting into cross-origin.
		})
		if err != nil {
			log.Printf("terminal: websocket accept failed: %v", err)
			return
		}

		session, err := Start(defaultCols, defaultRows, fileReader.GetRoot())
		if err != nil {
			log.Printf("terminal: failed to start session: %v", err)
			// A short, stable machine-readable reason rather than err's full
			// prose: WS close reasons are capped at 123 bytes by the
			// protocol, and the frontend needs something to key off of
			// precisely (e.g. show a permanent "won't work until you
			// restart non-elevated" message instead of a generic
			// "disconnected, try again" for this specific, deterministic
			// failure) rather than pattern-matching on wording that might
			// change.
			reason := "spawn failed"
			if err == ErrServerElevated {
				reason = "elevated"
			}
			conn.Close(websocket.StatusInternalError, reason)
			return
		}
		registry.add(session)
		defer registry.remove(session)
		defer session.Close()

		ctx := r.Context()
		done := make(chan struct{})

		// PTY -> WebSocket: relay raw output as binary frames.
		go func() {
			defer close(done)
			buf := make([]byte, 32*1024)
			for {
				n, err := session.Read(buf)
				if n > 0 {
					if writeErr := conn.Write(ctx, websocket.MessageBinary, buf[:n]); writeErr != nil {
						return
					}
				}
				if err != nil {
					if err != io.EOF {
						log.Printf("terminal: session read error: %v", err)
					}
					return
				}
			}
		}()

		// WebSocket -> PTY: keystrokes (binary) and resize requests (text/JSON).
	readLoop:
		for {
			select {
			case <-done:
				break readLoop
			default:
			}

			msgType, data, err := conn.Read(ctx)
			if err != nil {
				break
			}

			switch msgType {
			case websocket.MessageBinary:
				if _, err := session.Write(data); err != nil {
					break readLoop
				}
			case websocket.MessageText:
				var msg controlMessage
				if err := json.Unmarshal(data, &msg); err == nil && msg.Type == "resize" && msg.Cols > 0 && msg.Rows > 0 {
					_ = session.Resize(msg.Cols, msg.Rows)
				}
			}
		}

		session.Close()
		<-done
		conn.Close(websocket.StatusNormalClosure, "")
	}
}

func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
