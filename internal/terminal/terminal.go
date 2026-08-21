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

// ErrServerElevated is returned instead of spawning a session when the
// fastllm server process itself is running elevated (admin). Refusing
// outright — rather than spawning an elevated shell — is the guardrail:
// a ConPTY-spawned child inherits the parent's token with no separate UAC
// prompt, so a shell handed to the browser would otherwise be elevated
// with no indication of that to whoever's typing into it. Run fastllm as
// a standard user to use the terminal.
var ErrServerElevated = errors.New("fastllm is running elevated (as Administrator) — the terminal refuses to start an elevated shell; restart fastllm without admin rights to use it")

// Session is a live, interactive shell process attached to a pseudo
// console. Read/Write carry the raw terminal byte stream (including ANSI
// escape sequences); Resize adjusts the pseudo console's character grid.
type Session interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Resize(cols, rows int) error
	Close() error
}

// Start launches a new shell session with the given initial size. workDir
// sets the session's starting directory — pass "" to use the process's
// own default (whatever directory the server itself was launched from).
// extraEnv is appended to the session's environment (e.g. OPENAI_BASE_URL —
// see handler.go's use of Gate.InjectEnv/BaseURLHolder); pass nil for none.
func Start(cols, rows int, workDir string, extraEnv []string) (Session, error) {
	return start(cols, rows, workDir, extraEnv)
}
