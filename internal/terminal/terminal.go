// Package terminal spawns an interactive PowerShell session per WebSocket
// connection, giving the web UI a real integrated terminal (like VS Code's).
// Unlike every other feature in this app, a shell has no sandbox — it's
// full command execution as whatever OS user runs the server — so this
// package is only ever wired up when the caller (cmd/server) has confirmed
// FASTLLM_TERMINAL_ENABLED is set, and the HTTP handler itself additionally
// rejects any non-loopback connection regardless of what FASTLLM_ADDR is
// bound to. See handler.go for that enforcement.
//
// Session is implemented per-platform (see terminal_windows.go,
// terminal_other.go) since a real interactive pseudo-console is only
// available on Windows via ConPTY today.
package terminal

import "errors"

// ErrUnsupported is returned on platforms without a PTY implementation.
var ErrUnsupported = errors.New("terminal is not supported on this platform")

// Session is a live, interactive shell process attached to a pseudo
// console. Read/Write carry the raw terminal byte stream (including ANSI
// escape sequences); Resize adjusts the pseudo console's character grid.
type Session interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Resize(cols, rows int) error
	Close() error
}

// Start launches a new shell session with the given initial size.
func Start(cols, rows int) (Session, error) {
	return start(cols, rows)
}
