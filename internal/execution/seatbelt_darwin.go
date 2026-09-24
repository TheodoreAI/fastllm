//go:build darwin

package execution

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SeatbeltBackendName identifies the macOS isolated backend.
const SeatbeltBackendName = "seatbelt"

const sandboxExec = "/usr/bin/sandbox-exec"

// IsolatedBackend returns this platform's isolated backend.
func IsolatedBackend() (Backend, bool) { return NewSeatbeltBackend(), true }

// NewSeatbeltBackend runs each command under sandbox-exec with the profile in
// seatbelt_profile.go. The kernel applies the profile to the process and
// every descendant, so confinement needs no mediation by fastllm after
// launch. Nothing is granted persistently, so there is nothing to revoke.
func NewSeatbeltBackend() Backend { return &seatbeltBackend{} }

type seatbeltBackend struct{}

func (*seatbeltBackend) Name() string   { return SeatbeltBackendName }
func (*seatbeltBackend) Isolated() bool { return true }

// Available proves the sandbox works here by running a trivial command under
// a profile that denies by default. sandbox-exec is deprecated by Apple but
// present and working on current macOS; if that changes, this fails and
// isolation is refused rather than downgraded.
func (*seatbeltBackend) Available(ctx context.Context) error {
	if _, err := os.Stat(sandboxExec); err != nil {
		return fmt.Errorf("seatbelt sandbox unavailable: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// Controller work, through the local backend like every other launch.
	result, err := RunLocal(ctx, os.TempDir(), LocalPolicy(), Command{
		Executable: sandboxExec,
		Args:       []string{"-p", "(version 1)(deny default)(allow process-exec)(allow file-read*)", "/usr/bin/true"},
	})
	if err == nil {
		err = result.Err()
	}
	if err != nil {
		return fmt.Errorf("seatbelt sandbox unavailable: %v: %s", err, strings.TrimSpace(result.Stderr))
	}
	return nil
}

// Open prepares a private scratch directory and resolves the paths the
// profile grants. Each command then runs through the local backend's process
// handling, wrapped in sandbox-exec.
func (*seatbeltBackend) Open(ctx context.Context, spec ScopeSpec) (BackendScope, error) {
	workspace, err := filepath.EvalSymlinks(spec.Workspace)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace for sandbox: %w", err)
	}
	scratch, err := os.MkdirTemp("", "fastllm-sandbox-")
	if err != nil {
		return nil, err
	}
	if scratch, err = filepath.EvalSymlinks(scratch); err != nil {
		return nil, err
	}
	home := filepath.Join(scratch, "home")
	cache := filepath.Join(scratch, "go-build")
	for _, dir := range []string{home, cache} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			_ = os.RemoveAll(scratch)
			return nil, err
		}
	}
	var reads []string
	for _, dir := range goReadPaths(workspace) {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			reads = append(reads, real)
		}
	}

	inner := spec
	inner.Environment = withEnvironment(spec.Environment, map[string]string{
		// The user's home is unreadable inside, so tools get their own.
		"HOME":    home,
		"TMPDIR":  scratch + "/",
		"GOCACHE": cache,
	})
	return &seatbeltScope{
		local:     &localScope{spec: inner, lifetime: ctx},
		workspace: workspace,
		scratch:   scratch,
		reads:     reads,
	}, nil
}

type seatbeltScope struct {
	local     *localScope
	workspace string
	scratch   string
	reads     []string
}

func (s *seatbeltScope) Start(ctx context.Context, launch Launch) (Process, error) {
	argv := []string{"/bin/sh", "-c", launch.Command.Shell}
	if launch.Command.Executable != "" {
		path, err := exec.LookPath(launch.Command.Executable)
		if err != nil {
			return nil, err
		}
		argv = append([]string{path}, launch.Command.Args...)
	}
	wrapped := launch
	wrapped.Command = Command{
		Executable: sandboxExec,
		Args:       seatbeltArgs(s.workspace, s.scratch, s.reads, argv),
		Stdin:      launch.Command.Stdin,
	}
	return s.local.Start(ctx, wrapped)
}

// Close removes the scratch directory; the scope's processes are already
// stopped by its lifetime.
func (s *seatbeltScope) Close(context.Context) error {
	if s.scratch == "" {
		return nil
	}
	return os.RemoveAll(s.scratch)
}

// withEnvironment returns base with overrides replacing or adding variables.
func withEnvironment(base []string, overrides map[string]string) []string {
	out := make([]string, 0, len(base)+len(overrides))
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if _, replaced := overrides[name]; !replaced {
			out = append(out, kv)
		}
	}
	for name, value := range overrides {
		out = append(out, name+"="+value)
	}
	return out
}

// Nothing is granted persistently on macOS, so revocation has nothing to do.

func RevokeIsolatedBackend() error         { return nil }
func IsolationIdentity() (string, error)   { return "", nil }
func IsolationSetupPresent() (bool, error) { return false, nil }
func RevokeIsolationSetup(string) error    { return nil }
