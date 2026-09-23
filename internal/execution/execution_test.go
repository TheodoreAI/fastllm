package execution

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// sleepCommand and noisyCommand keep shell semantics unified across platforms.
func sleepCommand(seconds int) string {
	if runtime.GOOS == "windows" {
		return "Start-Sleep -Seconds " + strconv.Itoa(seconds)
	}
	return "sleep " + strconv.Itoa(seconds)
}

func noisyCommand(lines int) string {
	if runtime.GOOS == "windows" {
		return "1.." + strconv.Itoa(lines) + " | ForEach-Object { 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' }"
	}
	return "i=0; while [ $i -lt " + strconv.Itoa(lines) + " ]; do echo aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa; i=$((i+1)); done"
}

func modelScope(t *testing.T, opts Options) (*Manager, *Scope) {
	t.Helper()
	if opts.Workspace == "" {
		opts.Workspace = t.TempDir()
	}
	if opts.Policy == (Policy{}) {
		opts.Policy = LocalPolicy()
	}
	m := NewManager()
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	s, err := m.Open(context.Background(), opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return m, s
}

func TestStartDeniedWithoutFullPermission(t *testing.T) {
	// A local subprocess can write and reach the network, so anything short of
	// the full permission set must refuse to launch.
	for name, policy := range map[string]Policy{
		"no commands": {Commands: false, Write: true, Network: true},
		"no write":    {Commands: true, Write: false, Network: true},
		"no network":  {Commands: true, Write: true, Network: false},
	} {
		t.Run(name, func(t *testing.T) {
			_, s := modelScope(t, Options{Policy: policy})
			if _, err := s.Start(context.Background(), Command{Shell: "echo denied"}); !errors.Is(err, ErrDenied) {
				t.Fatalf("Start error = %v, want ErrDenied", err)
			}
		})
	}
}

func TestRunRequiresBoundScope(t *testing.T) {
	if _, err := Run(context.Background(), Command{Shell: "echo unscoped"}); !errors.Is(err, ErrNoScope) {
		t.Fatalf("Run error = %v, want ErrNoScope", err)
	}
}

func TestIsolationRequestIsRefusedNotDowngraded(t *testing.T) {
	m := NewManager()
	defer m.Close(context.Background())
	s, err := m.Open(context.Background(), Options{Workspace: t.TempDir(), Policy: LocalPolicy(), RequireIsolation: true})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Open error = %v, want ErrUnsupported", err)
	}
	if s != nil {
		t.Fatal("Open returned a scope; isolation must never fall back to local execution")
	}
}

func TestWorkingDirectoryCannotEscapeWorkspace(t *testing.T) {
	_, s := modelScope(t, Options{})
	for name, dir := range map[string]string{
		"parent":   "..",
		"nested":   filepath.Join("..", ".."),
		"absolute": t.TempDir(),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Start(context.Background(), Command{Shell: "echo escaped", Dir: dir}); err == nil {
				t.Fatal("Start accepted a working directory outside the workspace")
			}
		})
	}
}

func TestStructuredArgvSkipsTheShell(t *testing.T) {
	_, s := modelScope(t, Options{})
	result, err := Run(WithScope(context.Background(), s), Command{Executable: "go", Args: []string{"env", "GOOS"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := strings.TrimSpace(result.Stdout); got != runtime.GOOS {
		t.Fatalf("stdout = %q, want %q", got, runtime.GOOS)
	}
}

func TestCommandShapeIsValidated(t *testing.T) {
	_, s := modelScope(t, Options{})
	cases := map[string]Command{
		"neither executable nor shell": {},
		"both executable and shell":    {Executable: "go", Shell: "echo both"},
		"shell with argv":              {Shell: "echo argv", Args: []string{"extra"}},
	}
	for name, cmd := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Start(context.Background(), cmd); err == nil {
				t.Fatal("Start accepted an ambiguous command shape")
			}
		})
	}
}

func TestShellExitCodeAndStreams(t *testing.T) {
	_, s := modelScope(t, Options{})
	ctx := WithScope(context.Background(), s)

	result, err := Run(ctx, Command{Shell: "echo boundary"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(result.Stdout, "boundary") {
		t.Fatalf("stdout = %q, want it to contain %q", result.Stdout, "boundary")
	}
	if result.Reason != "exit" || result.ExitCode != 0 || result.Err() != nil {
		t.Fatalf("result = %+v, want a clean exit", result.ProcessState)
	}

	failed, err := Run(ctx, Command{Shell: "exit 3"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if failed.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3", failed.ExitCode)
	}
	if failed.Err() == nil {
		t.Fatal("Err() = nil for a nonzero exit")
	}
}

func TestTimeoutIsDistinguishedFromCancellation(t *testing.T) {
	_, s := modelScope(t, Options{})
	result, err := Run(WithScope(context.Background(), s), Command{Shell: sleepCommand(30), Timeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Reason != "timeout" {
		t.Fatalf("reason = %q, want %q", result.Reason, "timeout")
	}
	if !errors.Is(result.Err(), context.DeadlineExceeded) {
		t.Fatalf("Err() = %v, want DeadlineExceeded", result.Err())
	}
}

func TestCancelingWaitDoesNotStopTheProcess(t *testing.T) {
	_, s := modelScope(t, Options{})
	p, err := s.Start(context.Background(), Command{Shell: sleepCommand(30)})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := p.Wait(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait error = %v, want DeadlineExceeded", err)
	}
	if p.Snapshot().Exited {
		t.Fatal("canceling a Wait terminated the process")
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if state := p.Snapshot(); !state.Exited || state.Reason != "canceled" {
		t.Fatalf("state after Stop = %+v, want an explicit cancellation", state)
	}
}

func TestClosingScopeStopsItsProcesses(t *testing.T) {
	_, s := modelScope(t, Options{})
	p, err := s.Start(context.Background(), Command{Shell: sleepCommand(30)})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !p.Snapshot().Exited {
		t.Fatal("closing a scope left its process running")
	}
	if _, err := s.Start(context.Background(), Command{Shell: "echo after close"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Start after close = %v, want ErrClosed", err)
	}
}

func TestClosingManagerClosesEveryScope(t *testing.T) {
	m := NewManager()
	s, err := m.Open(context.Background(), Options{Workspace: t.TempDir(), Policy: LocalPolicy()})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	p, err := s.Start(context.Background(), Command{Shell: sleepCommand(30)})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatalf("Manager.Close: %v", err)
	}
	if !p.Snapshot().Exited {
		t.Fatal("closing the manager left a process running")
	}
	if err := s.Check(); err == nil {
		t.Fatal("scope still admits work after its manager closed")
	}
	if _, err := m.Open(context.Background(), Options{Workspace: t.TempDir(), Policy: LocalPolicy()}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Open after Manager.Close = %v, want ErrClosed", err)
	}
}

func TestBackgroundHandlesAreScopeOwned(t *testing.T) {
	m, first := modelScope(t, Options{})
	second, err := m.Open(context.Background(), Options{Workspace: t.TempDir(), Policy: LocalPolicy()})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	p, err := first.Start(context.Background(), Command{Shell: sleepCommand(30)})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	id := p.Snapshot().ID
	if _, err := first.Process(id); err != nil {
		t.Fatalf("owning scope could not resolve its own process: %v", err)
	}
	if _, err := second.Process(id); err == nil {
		t.Fatal("a process ID resolved in a scope that does not own it")
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestOutputIsBoundedWithTruncationIndicator(t *testing.T) {
	const limit = 512
	_, s := modelScope(t, Options{MaxOutputBytes: limit})
	result, err := Run(WithScope(context.Background(), s), Command{Shell: noisyCommand(400)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Truncated {
		t.Fatal("bounded output was not reported as truncated")
	}
	if len(result.Output) > limit {
		t.Fatalf("retained %d bytes of output, want at most %d", len(result.Output), limit)
	}
	if len(result.Output) == 0 {
		t.Fatal("bounded output dropped everything; the tail should survive")
	}
}

func TestReadOutputResumesFromCursor(t *testing.T) {
	_, s := modelScope(t, Options{})
	p, err := s.Start(context.Background(), Command{Shell: "echo first"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer p.Stop(context.Background())
	out, err := p.ReadOutput(context.Background(), 0)
	if err != nil {
		t.Fatalf("ReadOutput: %v", err)
	}
	if out.Truncated {
		t.Fatal("a cursor at zero reported truncation")
	}
	tail, err := p.ReadOutput(context.Background(), out.Next)
	if err != nil {
		t.Fatalf("ReadOutput: %v", err)
	}
	for _, chunk := range tail.Chunks {
		if chunk.Offset < out.Next {
			t.Fatalf("resumed read replayed data at offset %d, before cursor %d", chunk.Offset, out.Next)
		}
	}
}

func TestStdinIsReservedForDirectUserScopes(t *testing.T) {
	_, model := modelScope(t, Options{})
	if _, err := model.Start(context.Background(), Command{Shell: "echo model stdin", Stdin: strings.NewReader("hi")}); !errors.Is(err, ErrDenied) {
		t.Fatalf("model scope Start with stdin = %v, want ErrDenied", err)
	}

	m := NewManager()
	defer m.Close(context.Background())
	user, err := m.OpenUser(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("OpenUser: %v", err)
	}
	p, err := user.Start(context.Background(), Command{Shell: "echo user stdin", Stdin: strings.NewReader("hi")})
	if err != nil {
		t.Fatalf("user scope Start with stdin: %v", err)
	}
	if _, err := p.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestRunLocalReusesABoundScopeInsteadOfWidening(t *testing.T) {
	// The bound scope denies commands; an explicit local policy must not win.
	_, s := modelScope(t, Options{Policy: Policy{Commands: false, Write: true, Network: true}})
	ctx := WithScope(context.Background(), s)
	if _, err := RunLocal(ctx, t.TempDir(), LocalPolicy(), Command{Shell: "echo widened"}); err == nil {
		t.Fatal("RunLocal escaped the bound scope's policy")
	}
}

func TestRunLocalEstablishesAuthorityWhenUnbound(t *testing.T) {
	result, err := RunLocal(context.Background(), t.TempDir(), LocalPolicy(), Command{Shell: "echo standalone"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if !strings.Contains(result.Stdout, "standalone") {
		t.Fatalf("stdout = %q, want it to contain %q", result.Stdout, "standalone")
	}
}

func TestDeriveScratchKeepsPolicyAndRequiresAParent(t *testing.T) {
	if _, err := DeriveScratch(context.Background(), t.TempDir()); !errors.Is(err, ErrNoScope) {
		t.Fatalf("DeriveScratch error = %v, want ErrNoScope", err)
	}

	_, s := modelScope(t, Options{})
	scratch := t.TempDir()
	child, err := DeriveScratch(WithScope(context.Background(), s), scratch)
	if err != nil {
		t.Fatalf("DeriveScratch: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(scratch)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if child.Workspace() != resolved {
		t.Fatalf("scratch workspace = %q, want %q", child.Workspace(), resolved)
	}
	if err := child.Check(); err != nil {
		t.Fatalf("derived scope rejected the parent's policy: %v", err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestSanitizedEnvironmentDropsCredentialLikeNames(t *testing.T) {
	t.Setenv("FASTLLM_TEST_API_KEY", "secret")
	t.Setenv("FASTLLM_TEST_PLAIN", "visible")
	joined := strings.Join(SanitizedEnvironment(), "\n")
	if strings.Contains(joined, "FASTLLM_TEST_API_KEY") {
		t.Error("credential-like variable survived sanitization")
	}
	if !strings.Contains(joined, "FASTLLM_TEST_PLAIN=visible") {
		t.Error("ordinary variable was dropped")
	}
	if !strings.Contains(joined, "PATH=") {
		t.Error("PATH was dropped; commands would not resolve")
	}
}
