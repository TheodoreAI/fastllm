//go:build windows

package terminal

import (
	"unsafe"

	"github.com/UserExistsError/conpty"
	"golang.org/x/sys/windows"
)

// conptySession adapts *conpty.ConPty to the Session interface — mainly so
// handler.go never imports conpty directly, keeping the Windows-only
// dependency isolated to this file.
type conptySession struct {
	cpty *conpty.ConPty
}

// isElevated reports whether the current process token has an elevated
// (admin) integrity level — the same check Explorer uses to decide
// whether to show the UAC shield on a running process. A ConPTY-spawned
// child inherits the parent's token as-is; there's no separate consent
// prompt the way double-clicking an app marked "requires administrator"
// would show, so if fastllm.exe itself happens to be running elevated,
// every terminal session spawned from it would silently be elevated too
// with no indication to whoever's typing into the browser. Checked fresh
// on every spawn (not cached at startup) since it's a cheap syscall and
// this is the one place it actually matters.
func isElevated() (bool, error) {
	token := windows.GetCurrentProcessToken()
	var elevation uint32
	var outLen uint32
	err := windows.GetTokenInformation(
		token,
		windows.TokenElevation,
		(*byte)(unsafe.Pointer(&elevation)),
		uint32(unsafe.Sizeof(elevation)),
		&outLen,
	)
	if err != nil {
		return false, err
	}
	return elevation != 0, nil
}

func start(cols, rows int, workDir string) (Session, error) {
	elevated, err := isElevated()
	if err != nil {
		return nil, err
	}
	if elevated {
		return nil, ErrServerElevated
	}

	opts := []conpty.ConPtyOption{conpty.ConPtyDimensions(cols, rows)}
	if workDir != "" {
		opts = append(opts, conpty.ConPtyWorkDir(workDir))
	}
	cpty, err := conpty.Start("powershell.exe -NoLogo", opts...)
	if err != nil {
		return nil, err
	}
	return &conptySession{cpty: cpty}, nil
}

func (s *conptySession) Read(p []byte) (int, error)  { return s.cpty.Read(p) }
func (s *conptySession) Write(p []byte) (int, error) { return s.cpty.Write(p) }
func (s *conptySession) Resize(cols, rows int) error  { return s.cpty.Resize(cols, rows) }
func (s *conptySession) Close() error                 { return s.cpty.Close() }
