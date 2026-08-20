//go:build darwin || linux

package terminal

import (
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty"
)

// ptySession adapts an *os.File (the PTY master, returned by
// pty.StartWithSize) plus the shell's *exec.Cmd to the Session interface —
// the Unix analog of conptySession in terminal_windows.go. Unlike ConPTY,
// closing the PTY master doesn't reliably kill a shell that's ignoring
// SIGHUP (e.g. one with a job stopped in the background), so Close also
// explicitly signals the process group.
type ptySession struct {
	f         *os.File
	cmd       *exec.Cmd
	closeOnce sync.Once
}

// defaultShell mirrors what every terminal emulator on macOS/Linux does:
// respect $SHELL (sh has no reliable way to discover a specific user's
// configured login shell otherwise — /etc/passwd requires cgo's os/user
// or parsing it by hand) and fall back to /bin/sh, which POSIX guarantees
// exists, if it's unset.
func defaultShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/sh"
}

func start(cols, rows int, workDir string) (Session, error) {
	// "-l" starts a login shell so the session picks up the user's normal
	// profile (PATH additions from .zprofile/.bash_profile, etc.) instead
	// of a bare, minimally-configured shell — matching what double-clicking
	// Terminal.app or opening a new shell tab in most Linux terminal
	// emulators does.
	cmd := exec.Command(defaultShell(), "-l")
	if workDir != "" {
		cmd.Dir = workDir
	}
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		return nil, err
	}
	return &ptySession{f: f, cmd: cmd}, nil
}

func (s *ptySession) Read(p []byte) (int, error)  { return s.f.Read(p) }
func (s *ptySession) Write(p []byte) (int, error) { return s.f.Write(p) }

func (s *ptySession) Resize(cols, rows int) error {
	return pty.Setsize(s.f, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
}

func (s *ptySession) Close() error {
	var err error
	s.closeOnce.Do(func() {
		err = s.f.Close()
		// Best-effort: closing the PTY master alone delivers SIGHUP to the
		// foreground process on most shells, but an explicit kill also
		// reaches processes that backgrounded themselves (disown, nohup)
		// or are otherwise not attached to the controlling terminal by the
		// time this runs — same intent as the Job Object kill-on-close
		// guarantee terminal_windows.go relies on, just without an OS
		// primitive as strong as Windows' Job Objects to enforce it.
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		_ = s.cmd.Wait()
	})
	return err
}
