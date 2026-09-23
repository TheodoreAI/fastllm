//go:build windows

package execution

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// Each containment test pairs the sandboxed command with the same command on
// the local backend. The local run must succeed, so a denial proves the
// sandbox did the denying rather than the command being broken.

func containerScopes(t *testing.T, workspace string) (sandboxed, local *Scope) {
	t.Helper()
	backend := NewAppContainerBackend()
	if err := backend.Available(context.Background()); err != nil {
		t.Skipf("AppContainer unavailable: %v", err)
	}
	m := NewManager()
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	if err := m.Register(backend); err != nil {
		t.Fatalf("Register: %v", err)
	}
	sandboxed, err := m.Open(context.Background(), Options{
		Workspace: workspace, Policy: LocalPolicy(),
		Backend: AppContainerBackendName, RequireIsolation: true,
		Timeout: 2 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Open sandboxed: %v", err)
	}
	local, err = m.Open(context.Background(), Options{Workspace: workspace, Policy: LocalPolicy(), Timeout: 2 * time.Minute})
	if err != nil {
		t.Fatalf("Open local: %v", err)
	}
	return sandboxed, local
}

func runIn(t *testing.T, s *Scope, cmd Command) Result {
	t.Helper()
	result, err := Run(WithScope(context.Background(), s), cmd)
	if err != nil {
		t.Fatalf("Run on %s: %v", s.Backend(), err)
	}
	return result
}

func TestAppContainerScopeReportsIsolation(t *testing.T) {
	sandboxed, local := containerScopes(t, t.TempDir())
	if !sandboxed.Isolated() || sandboxed.Backend() != AppContainerBackendName {
		t.Fatalf("sandboxed scope backend = %q isolated = %v", sandboxed.Backend(), sandboxed.Isolated())
	}
	if local.Isolated() {
		t.Fatal("local scope reports isolation")
	}
}

func TestAppContainerShellRunsInTheWorkspace(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "sub"), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	sandboxed, _ := containerScopes(t, workspace)

	// The workspace sits under the user profile, which the container cannot
	// read: PowerShell only lands here because of the workspace drive.
	result := runIn(t, sandboxed, Command{Shell: "Set-Content inside.txt -Value sandbox-ok; Get-Content inside.txt; (Get-Location).Path; $PWD.ProviderPath; cmd /c cd"})
	lines := strings.Fields(result.Stdout)
	if result.ExitCode != 0 || len(lines) != 4 {
		t.Fatalf("exit %d, output:\n%s", result.ExitCode, result.Output)
	}
	if lines[0] != "sandbox-ok" {
		t.Errorf("relative write/read = %q", lines[0])
	}
	if lines[1] != workspaceDrive+`:\` {
		t.Errorf("PowerShell location = %q, want %q", lines[1], workspaceDrive+`:\`)
	}
	for i, label := range map[int]string{2: "provider path", 3: "native working directory"} {
		if got := strings.TrimRight(lines[i], `\`); !strings.EqualFold(got, resolved) {
			t.Errorf("%s = %q, want the real workspace %q", label, got, resolved)
		}
	}

	nested := runIn(t, sandboxed, Command{Shell: "(Get-Location).Path; cmd /c cd", Dir: "sub"})
	if !strings.Contains(nested.Stdout, workspaceDrive+`:\sub`) || !strings.Contains(strings.ToLower(nested.Stdout), strings.ToLower(filepath.Join(resolved, "sub"))) {
		t.Errorf("subdirectory launch did not land in sub:\n%s", nested.Output)
	}

	absolute := filepath.Join(workspace, "absolute.txt")
	written := runIn(t, sandboxed, Command{Shell: "Set-Content -LiteralPath " + psLiteral(absolute) + " -Value abs-ok; Get-Content -LiteralPath " + psLiteral(absolute)})
	if !strings.Contains(written.Stdout, "abs-ok") {
		t.Errorf("absolute path inside the workspace failed:\n%s", written.Output)
	}
}

func TestAppContainerCannotReadOutsideTheWorkspace(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("FASTLLM-SECRET-7f3a"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	sandboxed, local := containerScopes(t, t.TempDir())
	read := Command{Shell: "Get-Content -LiteralPath " + psLiteral(secret)}

	if control := runIn(t, local, read); !strings.Contains(control.Stdout, "FASTLLM-SECRET-7f3a") {
		t.Fatalf("control: local backend could not read the secret, so this test proves nothing:\n%s", control.Output)
	}
	result := runIn(t, sandboxed, read)
	if strings.Contains(result.Output, "FASTLLM-SECRET-7f3a") {
		t.Fatal("sandbox read a file outside its workspace")
	}
	if result.ExitCode == 0 {
		t.Fatalf("read outside workspace exited 0:\n%s", result.Output)
	}
}

func TestAppContainerCannotWriteOutsideTheWorkspace(t *testing.T) {
	outside := t.TempDir()
	sandboxed, local := containerScopes(t, t.TempDir())
	write := func(name string) Command {
		return Command{Shell: "Set-Content -LiteralPath " + psLiteral(filepath.Join(outside, name)) + " -Value escaped"}
	}

	runIn(t, local, write("control.txt"))
	if _, err := os.Stat(filepath.Join(outside, "control.txt")); err != nil {
		t.Fatalf("control: local backend could not write outside, so this test proves nothing: %v", err)
	}
	runIn(t, sandboxed, write("escape.txt"))
	if _, err := os.Stat(filepath.Join(outside, "escape.txt")); err == nil {
		t.Fatal("sandbox wrote a file outside its workspace")
	}
}

func TestAppContainerHasNoNetwork(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer listener.Close()
	accepted := make(chan struct{}, 8)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
			accepted <- struct{}{}
		}
	}()
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	// Stop on the first error, or PowerShell reports the failed connect and
	// then prints 'connected' anyway.
	connect := Command{Shell: "$ErrorActionPreference = 'Stop'; $c = New-Object Net.Sockets.TcpClient; if (-not $c.ConnectAsync('127.0.0.1', " + port + ").Wait(5000)) { throw 'connect timed out' }; 'connected'; $c.Close()"}
	sandboxed, local := containerScopes(t, t.TempDir())

	if control := runIn(t, local, connect); !strings.Contains(control.Stdout, "connected") {
		t.Fatalf("control: local backend could not connect, so this test proves nothing:\n%s", control.Output)
	}
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("control connection never reached the listener")
	}

	result := runIn(t, sandboxed, connect)
	if strings.Contains(result.Stdout, "connected") || result.ExitCode == 0 {
		t.Fatalf("sandbox connected to a host service:\n%s", result.Output)
	}
	select {
	case <-accepted:
		t.Fatal("listener accepted a connection from the sandbox")
	case <-time.After(500 * time.Millisecond):
	}
}

func TestAppContainerKeepsProcessSemantics(t *testing.T) {
	sandboxed, _ := containerScopes(t, t.TempDir())

	streams := runIn(t, sandboxed, Command{Shell: "Write-Output to-stdout; [Console]::Error.WriteLine('to-stderr'); exit 3"})
	if streams.ExitCode != 3 || streams.Reason != "exit" {
		t.Fatalf("exit = %d reason = %q, want 3/exit", streams.ExitCode, streams.Reason)
	}
	if !strings.Contains(streams.Stdout, "to-stdout") || strings.Contains(streams.Stdout, "to-stderr") {
		t.Errorf("stdout = %q", streams.Stdout)
	}
	if !strings.Contains(streams.Stderr, "to-stderr") {
		t.Errorf("stderr = %q", streams.Stderr)
	}

	timedOut := runIn(t, sandboxed, Command{Shell: "Start-Sleep -Seconds 30", Timeout: 2 * time.Second})
	if timedOut.Reason != "timeout" || !errors.Is(timedOut.Err(), context.DeadlineExceeded) {
		t.Fatalf("reason = %q, want timeout", timedOut.Reason)
	}

	p, err := sandboxed.Start(context.Background(), Command{Shell: "Start-Sleep -Seconds 30"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if state := p.Snapshot(); !state.Exited || state.Reason != "canceled" {
		t.Fatalf("state after Stop = %+v", state)
	}
}

func TestAppContainerKillsDescendantsWhenTheCommandExits(t *testing.T) {
	sandboxed, _ := containerScopes(t, t.TempDir())
	// The command starts a long-lived child, prints its PID, and exits.
	result := runIn(t, sandboxed, Command{Shell: "$p = [Diagnostics.Process]::Start('powershell.exe', '-NoProfile -Command Start-Sleep -Seconds 60'); $p.Id"})
	pid, err := strconv.Atoi(strings.TrimSpace(result.Stdout))
	if err != nil {
		t.Fatalf("no child PID in output:\n%s", result.Output)
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return // already gone
	}
	defer windows.CloseHandle(handle)
	if event, _ := windows.WaitForSingleObject(handle, 5000); event != windows.WAIT_OBJECT_0 {
		t.Fatalf("descendant %d outlived its command", pid)
	}
}

func TestAppContainerBuildsAndRunsGo(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not installed")
	}
	workspace := t.TempDir()
	// Match this repository's go directive, so the build uses the same
	// auto-selected toolchain a real fastllm workspace would.
	goMod, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatalf("read repository go.mod: %v", err)
	}
	directive := "go 1.25.0"
	for _, line := range strings.Split(string(goMod), "\n") {
		if strings.HasPrefix(line, "go ") {
			directive = strings.TrimSpace(line)
		}
	}
	files := map[string]string{
		"go.mod":  "module sandboxed\n\n" + directive + "\n",
		"main.go": "package main\n\nfunc main() { println(\"built-in-sandbox\") }\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	sandboxed, _ := containerScopes(t, workspace)
	result := runIn(t, sandboxed, Command{Executable: "go", Args: []string{"run", "."}})
	if result.ExitCode != 0 || !strings.Contains(result.Output, "built-in-sandbox") {
		t.Fatalf("go run in sandbox failed: exit %d\n%s", result.ExitCode, result.Output)
	}
}

func TestRevokeRemovesATreeGrant(t *testing.T) {
	identity, err := NewAppContainerBackend().(*appContainerBackend).profile()
	if err != nil {
		t.Skipf("AppContainer unavailable: %v", err)
	}
	shared := t.TempDir()
	sandboxed, _ := containerScopes(t, t.TempDir())
	write := func(name string) bool {
		target := filepath.Join(shared, name)
		runIn(t, sandboxed, Command{Shell: "Set-Content -LiteralPath " + psLiteral(target) + " -Value x"})
		_, err := os.Stat(target)
		return err == nil
	}

	if write("before.txt") {
		t.Fatal("control: sandbox could write before any grant, so this test proves nothing")
	}
	if granted, err := grantTree(shared, identity.sid, fileAllAccess); err != nil || !granted {
		t.Fatalf("grantTree = %v, %v", granted, err)
	}
	if granted, err := grantTree(shared, identity.sid, fileAllAccess); err != nil || granted {
		t.Fatalf("second grantTree = %v, %v; want it to find the first grant", granted, err)
	}
	if !write("granted.txt") {
		t.Fatal("sandbox could not write after a tree grant")
	}
	if err := revokeTree(shared, identity.sid); err != nil {
		t.Fatalf("revokeTree: %v", err)
	}
	if write("revoked.txt") {
		t.Fatal("sandbox could still write after the grant was revoked")
	}
	if direct, err := directGrant(shared, identity.sid); err != nil || direct {
		t.Fatalf("direct grant remains after revoke: %v, %v", direct, err)
	}
}

func TestGrantRecordDeduplicates(t *testing.T) {
	record := grantRecord{path: filepath.Join(t.TempDir(), "grants.txt")}
	if paths, err := record.paths(); err != nil || len(paths) != 0 {
		t.Fatalf("empty record = %v, %v", paths, err)
	}
	for _, path := range []string{`C:\ws`, `c:\WS`, `C:\cache`} {
		if err := record.add(path); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	paths, err := record.paths()
	if err != nil {
		t.Fatalf("paths: %v", err)
	}
	if len(paths) != 2 || paths[0] != `C:\ws` || paths[1] != `C:\cache` {
		t.Fatalf("paths = %q, want the case-only duplicate collapsed", paths)
	}
}

func TestAppContainerRunsGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	// Known gap: Git for Windows (MSYS) checks every directory above its
	// working directory and fails on C:\Users, which the container cannot
	// read. Remove this skip with the fix.
	t.Skip(`known gap: git cannot resolve a working directory under C:\Users inside the container`)
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "tracked.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	sandboxed, _ := containerScopes(t, workspace)
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "tracked.txt"},
		{"commit", "-q", "-m", "first"},
	} {
		if result := runIn(t, sandboxed, Command{Executable: "git", Args: args}); result.ExitCode != 0 {
			t.Fatalf("git %s: exit %d\n%s", strings.Join(args, " "), result.ExitCode, result.Output)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, "tracked.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// Checkpoints are built on stash create, which writes a commit object.
	stash := runIn(t, sandboxed, Command{Executable: "git", Args: []string{"stash", "create"}})
	if stash.ExitCode != 0 || len(strings.TrimSpace(stash.Stdout)) < 7 {
		t.Fatalf("git stash create: exit %d\n%s", stash.ExitCode, stash.Output)
	}
}
