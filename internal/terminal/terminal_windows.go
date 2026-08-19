//go:build windows

package terminal

import (
	"log"
	"sync"
	"unsafe"

	"github.com/UserExistsError/conpty"
	"golang.org/x/sys/windows"
)

// setConsoleCtrlHandler is unwrapped by golang.org/x/sys/windows, so it's
// declared directly against kernel32.dll here — same approach
// jobobject_windows.go uses for its own handful of raw Job Object calls.
var (
	modKernel32Ctrl           = windows.NewLazySystemDLL("kernel32.dll")
	procSetConsoleCtrlHandler = modKernel32Ctrl.NewProc("SetConsoleCtrlHandler")
)

// restoreDefaultCtrlCHandling calls SetConsoleCtrlHandler(NULL, FALSE),
// which un-installs any Ctrl+C/Ctrl+Break handler fastllm.exe's own
// process (or the Go runtime, which installs one implicitly) may have
// registered on its console. Without this, a raw 0x03 byte written into
// ConPTY's input pipe is delivered as a CTRL_C_EVENT to fastllm's own
// console process group first — where a registered handler can consume it
// — rather than propagating down into whatever foreign child process
// (ping, npm, python, a long-running script, etc.) is currently running
// inside the pseudoconsole, which is the actual target the user is trying
// to interrupt. This is the same fix VS Code's node-pty applies
// immediately after spawning its own ConPTY host (see PtyStartProcess in
// node-pty's src/win/conpty.cc) — confirmed 2026-08-19 against node-pty's
// current source after a user reported Ctrl+C not stopping a child
// process (but working fine against an idle PowerShell prompt) in
// fastllm's own terminal, which is exactly this failure mode.
func restoreDefaultCtrlCHandling() error {
	ret, _, err := procSetConsoleCtrlHandler.Call(0, 0)
	if ret == 0 {
		return err
	}
	return nil
}

// conptySession adapts *conpty.ConPty to the Session interface — mainly so
// handler.go never imports conpty directly, keeping the Windows-only
// dependency isolated to this file. job is non-zero when the child shell
// was successfully placed under a kill-on-close Job Object (see
// jobobject_windows.go) so it can't outlive fastllm.exe even if this
// process is killed rather than shut down cleanly.
//
// closeOnce guards against Close being called more than once — it has at
// least two legitimate independent callers (the WS handler's own cleanup
// in handler.go, and Registry.CloseAll on app shutdown, which can race
// against a session that's already tearing itself down), and neither
// conpty.ConPty.Close nor windows.CloseHandle on the job handle tolerates
// being called twice: both operate on raw OS handles with no double-close
// guard of their own, and a second CloseHandle on an already-closed (and
// possibly already-reused) handle value is undefined behavior — observed
// in practice as the whole fastllm process silently dying with no Go
// panic, since it happens at the OS handle-table level.
type conptySession struct {
	cpty      *conpty.ConPty
	job       windows.Handle
	closeOnce sync.Once
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

	// Best-effort, same reasoning as the Job Object below: if this fails,
	// Ctrl+C still works for interrupting PowerShell's own idle prompt —
	// it just may not reliably reach a foreign child process running
	// inside the session, same as before this call existed.
	if err := restoreDefaultCtrlCHandling(); err != nil {
		log.Printf("terminal: SetConsoleCtrlHandler(NULL, FALSE) failed, Ctrl+C may not reach child processes: %v", err)
	}

	// Best-effort: a shell that isn't placed under the job still works
	// exactly as before (relying on ConPTY pipe teardown to end it), so a
	// failure here shouldn't fail the whole session — it only means the
	// hard kill-on-crash guarantee doesn't apply to this one session.
	var job windows.Handle
	if j, err := newKillOnCloseJob(); err == nil {
		if err := assignProcessToJob(j, cpty.Pid()); err == nil {
			job = j
		} else {
			windows.CloseHandle(j)
		}
	}

	return &conptySession{cpty: cpty, job: job}, nil
}

func (s *conptySession) Read(p []byte) (int, error)  { return s.cpty.Read(p) }
func (s *conptySession) Write(p []byte) (int, error) { return s.cpty.Write(p) }
func (s *conptySession) Resize(cols, rows int) error { return s.cpty.Resize(cols, rows) }

func (s *conptySession) Close() error {
	var err error
	s.closeOnce.Do(func() {
		err = s.cpty.Close()
		if s.job != 0 {
			// Closing the job handle without ever calling TerminateJobObject
			// still kills every assigned process immediately, because the job
			// was created with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE — this is
			// the same mechanism that protects against an abrupt fastllm exit,
			// just invoked explicitly here for the graceful-shutdown path so
			// there's no reliance on the shell noticing its pipes closed.
			windows.CloseHandle(s.job)
		}
	})
	return err
}
