package terminal

import (
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"

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

// NewHandler returns the /api/terminal/ws handler. Registering it at all
// is the caller's responsibility, gated on FASTLLM_TERMINAL_ENABLED — this
// function does not check that env var itself, since by the time a request
// reaches here the route either exists or doesn't.
//
// What this handler does enforce, independently of that gate: the request
// must originate from loopback. FASTLLM_ADDR can bind to all interfaces
// (its default, ":8080", does exactly that) with no enforcement anywhere
// else in this app that the caller is local — a shell endpoint is the one
// place that gap can't be inherited silently.
func NewHandler(registry *Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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

		session, err := Start(defaultCols, defaultRows)
		if err != nil {
			log.Printf("terminal: failed to start session: %v", err)
			conn.Close(websocket.StatusInternalError, err.Error())
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
