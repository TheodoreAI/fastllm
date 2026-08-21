package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// idleTimeout is a belt-and-suspenders teardown: the primary way a
// Session dies is the WS handler's own defer on disconnect (see
// handler.go), which covers every normal case (tab closes, folder
// switches, browser tab closes cleanly). This timer only matters if that
// path is somehow skipped — a crash, a non-graceful navigation — so it's
// deliberately generous rather than tuned for responsiveness.
const idleTimeout = 10 * time.Minute

// rpcMessage is gopls's wire shape, decoded generically enough to tell
// apart the three things it can send: a response to one of our own Call
// requests (ID set, Method empty), a server-to-client request needing a
// reply (both set), or a notification (Method set, ID empty) — chiefly
// textDocument/publishDiagnostics.
type rpcMessage struct {
	ID     *int64          `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Session is one running gopls process for one project root, speaking
// LSP-over-stdio (Content-Length-framed JSON-RPC). Call/Notify are its
// two ways to talk to gopls; Forward relays an already-framed-as-JSON
// message from the browser (see handler.go) without the caller needing
// to know LSP method semantics.
type Session struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	notify func(method string, params json.RawMessage)

	writeMu sync.Mutex // serializes writes to stdin (Call/Notify/respondToServerRequest all write concurrently)

	mu        sync.Mutex
	nextID    int64
	pending   map[int64]chan rpcMessage
	closed    bool
	idleTimer *time.Timer
}

// Start spawns gopls with its working directory set to root (so it sees
// the real project — live file watching and git state, not a scratch
// copy, the same reasoning buildcheck.RunTestsInPlace uses). notify is
// called for every server-initiated notification gopls sends, chiefly
// textDocument/publishDiagnostics.
func Start(root string, notify func(method string, params json.RawMessage)) (*Session, error) {
	cmd := exec.Command("gopls")
	cmd.Dir = root

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	s := &Session{
		cmd:     cmd,
		stdin:   stdin,
		notify:  notify,
		pending: make(map[int64]chan rpcMessage),
	}

	go s.readLoop(stdout)
	go logLines(stderr)

	return s, nil
}

// logLines forwards gopls's own log output (not part of the LSP
// protocol — that's all on stdout/stdin) to fastllm's server log,
// prefixed so it's identifiable if something goes wrong.
func logLines(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		log.Printf("gopls: %s", scanner.Text())
	}
}

// Call sends a request and blocks until gopls responds or ctx is done.
func (s *Session) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("lsp: session closed")
	}
	s.nextID++
	id := s.nextID
	ch := make(chan rpcMessage, 1)
	s.pending[id] = ch
	s.mu.Unlock()
	s.resetIdleTimer()

	if err := s.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, err
	}

	select {
	case msg, ok := <-ch:
		if !ok {
			return nil, errors.New("lsp: session closed")
		}
		if msg.Error != nil {
			return nil, fmt.Errorf("lsp: %s: %s", method, msg.Error.Message)
		}
		return msg.Result, nil
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, ctx.Err()
	}
}

// Notify sends a fire-and-forget message — didOpen/didChange/didClose.
func (s *Session) Notify(method string, params any) error {
	s.resetIdleTimer()
	return s.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// Forward relays one already-parsed JSON-RPC object from the browser:
// a notification (no "id") goes straight to gopls via Notify; a request
// (has "id") goes through Call so its response gets gopls's own request
// tracking, and the result is written back to the browser via write,
// re-wrapped with the browser's original id so it can correlate the
// reply to whichever textDocument/definition (etc.) call it sent. This
// is the one place fastllm's own ids (from Call's nextID counter) and
// the browser's ids (from web/src/lsp.js's own counter) are kept from
// colliding — Call always mints a fresh internal id regardless of what
// the browser sent.
func (s *Session) Forward(raw json.RawMessage, write func(json.RawMessage) error) error {
	var probe struct {
		ID     json.RawMessage `json:"id,omitempty"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params,omitempty"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return err
	}

	if len(probe.ID) == 0 || string(probe.ID) == "null" {
		return s.Notify(probe.Method, probe.Params)
	}

	go func() {
		result, err := s.Call(context.Background(), probe.Method, probe.Params)
		resp := struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Result  json.RawMessage `json:"result,omitempty"`
			Error   *rpcError       `json:"error,omitempty"`
		}{JSONRPC: "2.0", ID: probe.ID}
		if err != nil {
			resp.Error = &rpcError{Code: -32000, Message: err.Error()}
		} else {
			resp.Result = result
		}
		b, err := json.Marshal(resp)
		if err != nil {
			return
		}
		_ = write(b)
	}()
	return nil
}

// Close tears down the gopls process. Safe to call more than once (the
// WS handler's defer and Registry.Swap/CloseAll can both reach it for
// the same Session) — same requirement terminal.Session documents for
// the same reason.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	if s.idleTimer != nil {
		s.idleTimer.Stop()
	}
	pending := s.pending
	s.pending = nil
	s.mu.Unlock()

	for _, ch := range pending {
		close(ch)
	}
	_ = s.stdin.Close()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	// Reap in the background rather than blocking the caller (typically
	// an HTTP handler's defer) on however long it takes stdout to drain
	// after the kill signal.
	go func() { _ = s.cmd.Wait() }()
	return nil
}

func (s *Session) resetIdleTimer() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if s.idleTimer == nil {
		s.idleTimer = time.AfterFunc(idleTimeout, func() { _ = s.Close() })
		return
	}
	s.idleTimer.Reset(idleTimeout)
}

// send frames one outgoing message as Content-Length-prefixed JSON, the
// wire shape gopls expects on stdin. Content-Length framing is added
// here and stripped in readLoop below — the one protocol-translation
// seam in this package; WS frames to the browser never carry it (see
// handler.go).
func (s *Session) send(msg any) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return writeFramedMessage(s.stdin, b)
}

func writeFramedMessage(w io.Writer, body []byte) error {
	if _, err := io.WriteString(w, fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}

func (s *Session) readLoop(stdout io.Reader) {
	r := bufio.NewReader(stdout)
	for {
		length, err := readContentLength(r)
		if err != nil {
			s.Close()
			return
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(r, body); err != nil {
			s.Close()
			return
		}

		var msg rpcMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			continue
		}

		switch {
		case msg.ID != nil && msg.Method == "":
			// A response to one of our own Call requests.
			s.mu.Lock()
			ch, ok := s.pending[*msg.ID]
			if ok {
				delete(s.pending, *msg.ID)
			}
			s.mu.Unlock()
			if ok {
				ch <- msg
			}
		case msg.ID != nil && msg.Method != "":
			// A request FROM gopls TO us (e.g. workspace/configuration) —
			// must be answered or gopls can stall waiting for a reply
			// fastllm never sends any client-side settings for.
			go s.respondToServerRequest(*msg.ID, msg.Method, msg.Params)
		case msg.Method != "":
			if s.notify != nil {
				s.notify(msg.Method, msg.Params)
			}
		}
	}
}

func (s *Session) respondToServerRequest(id int64, method string, params json.RawMessage) {
	var result any
	if method == "workspace/configuration" {
		var p struct {
			Items []json.RawMessage `json:"items"`
		}
		_ = json.Unmarshal(params, &p)
		result = make([]any, len(p.Items))
	}
	_ = s.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

// readContentLength reads LSP's header block (one or more "Key: value"
// lines terminated by a blank line) and returns the declared body
// length. Only Content-Length is used; Content-Type (if present) is
// ignored, matching every other minimal LSP client.
func readContentLength(r *bufio.Reader) (int, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return 0, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if name, value, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return 0, err
			}
			length = n
		}
	}
	if length < 0 {
		return 0, errors.New("lsp: message missing Content-Length header")
	}
	return length, nil
}
