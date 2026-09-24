//go:build darwin

package execution

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These prove the Seatbelt profile's promises on a real Mac. Each denial is
// paired with the same command on the local backend, which must succeed, so
// a failure inside the sandbox is the sandbox's doing, not a broken command.
//
//	go test ./internal/execution -run Seatbelt -v

func seatbeltScopes(t *testing.T, workspace string) (sandboxed, local *Scope) {
	t.Helper()
	backend := NewSeatbeltBackend()
	if err := backend.Available(context.Background()); err != nil {
		t.Skipf("Seatbelt unavailable: %v", err)
	}
	m := NewManager()
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	if err := m.Register(backend); err != nil {
		t.Fatalf("Register: %v", err)
	}
	var err error
	sandboxed, err = m.Open(context.Background(), Options{
		Workspace: workspace, Policy: LocalPolicy(),
		Backend: SeatbeltBackendName, RequireIsolation: true, Timeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("Open sandboxed: %v", err)
	}
	local, err = m.Open(context.Background(), Options{Workspace: workspace, Policy: LocalPolicy(), Timeout: time.Minute})
	if err != nil {
		t.Fatalf("Open local: %v", err)
	}
	return sandboxed, local
}

func shell(t *testing.T, s *Scope, script string) Result {
	t.Helper()
	result, err := Run(WithScope(context.Background(), s), Command{Shell: script})
	if err != nil {
		t.Fatalf("Run on %s: %v", s.Backend(), err)
	}
	return result
}

// deniedInSandbox runs script in both scopes and requires it to work locally
// and fail in the sandbox.
func deniedInSandbox(t *testing.T, sandboxed, local *Scope, what, script string) {
	t.Helper()
	if r := shell(t, local, script); r.ExitCode != 0 {
		t.Fatalf("%s: the local control run failed, so the test proves nothing: %s %s", what, r.Stdout, r.Stderr)
	}
	if r := shell(t, sandboxed, script); r.ExitCode == 0 {
		t.Fatalf("%s: allowed inside the sandbox: %s", what, r.Stdout)
	}
}

func TestSeatbeltRunsCommandsInTheWorkspace(t *testing.T) {
	workspace := t.TempDir()
	sandboxed, _ := seatbeltScopes(t, workspace)
	r := shell(t, sandboxed, "echo hello > out.txt && cat out.txt && pwd")
	if r.ExitCode != 0 || !strings.Contains(r.Stdout, "hello") {
		t.Fatalf("a workspace write failed: %d %s %s", r.ExitCode, r.Stdout, r.Stderr)
	}
	if data, _ := os.ReadFile(filepath.Join(workspace, "out.txt")); strings.TrimSpace(string(data)) != "hello" {
		t.Fatal("the sandboxed write did not land in the real workspace")
	}
}

func TestSeatbeltBlocksWritesOutsideTheWorkspace(t *testing.T) {
	workspace := t.TempDir()
	sandboxed, local := seatbeltScopes(t, workspace)
	home, _ := os.UserHomeDir()
	target := filepath.Join(home, ".fastllm-seatbelt-test-"+strings.ReplaceAll(t.Name(), "/", "_"))
	t.Cleanup(func() { _ = os.Remove(target) })
	deniedInSandbox(t, sandboxed, local, "write to home", "echo x > '"+target+"' && rm '"+target+"'")
	outside := filepath.Join(os.TempDir(), "fastllm-seatbelt-outside")
	t.Cleanup(func() { _ = os.Remove(outside) })
	deniedInSandbox(t, sandboxed, local, "write to /tmp", "echo x > '"+outside+"' && rm '"+outside+"'")
}

func TestSeatbeltBlocksReadingTheHomeDirectory(t *testing.T) {
	workspace := t.TempDir()
	sandboxed, local := seatbeltScopes(t, workspace)
	home, _ := os.UserHomeDir()
	secret, err := os.CreateTemp(home, ".fastllm-seatbelt-secret-")
	if err != nil {
		t.Skipf("cannot plant a test secret in the home directory: %v", err)
	}
	_, _ = secret.WriteString("sk-test-secret\n")
	secret.Close()
	t.Cleanup(func() { _ = os.Remove(secret.Name()) })
	deniedInSandbox(t, sandboxed, local, "read a file in home", "cat '"+secret.Name()+"'")
}

func TestSeatbeltBlocksTheNetwork(t *testing.T) {
	workspace := t.TempDir()
	sandboxed, local := seatbeltScopes(t, workspace)
	probe := "/usr/bin/curl -sS -o /dev/null --max-time 8 https://example.com"
	if r := shell(t, local, probe); r.ExitCode != 0 {
		t.Skipf("no network for the control run: %s", r.Stderr)
	}
	if r := shell(t, sandboxed, probe); r.ExitCode == 0 {
		t.Fatal("the sandbox reached the network")
	}
}

func TestSeatbeltRunsTheGoToolchain(t *testing.T) {
	workspace := t.TempDir()
	sandboxed, local := seatbeltScopes(t, workspace)
	if r := shell(t, local, "go version"); r.ExitCode != 0 {
		t.Skip("go is not on PATH")
	}
	if err := os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module probe\n\ngo 1.21\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main\n\nfunc main() { println(\"built\") }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := shell(t, sandboxed, "go run .")
	if r.ExitCode != 0 || !strings.Contains(r.Stdout+r.Stderr, "built") {
		t.Fatalf("go run inside the sandbox failed: %s %s", r.Stdout, r.Stderr)
	}
}

func TestSeatbeltGivesToolsAPrivateHome(t *testing.T) {
	workspace := t.TempDir()
	sandboxed, _ := seatbeltScopes(t, workspace)
	home, _ := os.UserHomeDir()
	r := shell(t, sandboxed, `echo "$HOME" && touch "$HOME/ok"`)
	if r.ExitCode != 0 || strings.Contains(r.Stdout, home+"\n") {
		t.Fatalf("HOME inside the sandbox should be private and writable: %s %s", r.Stdout, r.Stderr)
	}
}
