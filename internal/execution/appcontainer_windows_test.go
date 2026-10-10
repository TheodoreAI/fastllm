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
	canonical, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := revokeWorkspaceIdentity(workspaceContainerName(canonical)); err != nil {
			t.Errorf("revoke test workspace: %v", err)
		}
	})
	m := NewManager()
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	if err := m.Register(backend); err != nil {
		t.Fatalf("Register: %v", err)
	}
	sandboxed, err = m.Open(context.Background(), Options{
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

const sandboxStarted = "FASTLLM-SANDBOX-COMMAND-STARTED"

// A denied operation only demonstrates confinement if the shell actually ran.
// A DLL initialization failure must never count as a successful containment test.
func assertSandboxStarted(t *testing.T, result Result) {
	t.Helper()
	if !strings.Contains(result.Stdout, sandboxStarted) {
		t.Fatalf("sandbox command did not start: exit %d\n%s", result.ExitCode, result.Output)
	}
}

func psLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// assertShellAndGitWork checks that PowerShell and native programs start at
// the workspace drive, that work there lands in the real workspace, and that
// git can commit: PowerShell and git are the tools that check every directory
// above their working directory.
func assertShellAndGitWork(t *testing.T, workspace string) {
	t.Helper()
	if err := os.Mkdir(filepath.Join(workspace, "sub"), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	sandboxed, _ := containerScopes(t, workspace)
	root := sandboxed.CommandWorkspace()
	if len(root) != 3 || root[1:] != `:\` {
		t.Fatalf("command workspace = %q, want a drive root", root)
	}

	shell := runIn(t, sandboxed, Command{Shell: "Set-Content inside.txt -Value sandbox-ok; Get-Content inside.txt; (Get-Location).Path; cmd /c cd"})
	lines := strings.Fields(shell.Stdout)
	if shell.ExitCode != 0 || len(lines) != 3 || lines[0] != "sandbox-ok" {
		t.Fatalf("shell in workspace: exit %d\n%s", shell.ExitCode, shell.Output)
	}
	for i, label := range map[int]string{1: "PowerShell location", 2: "native working directory"} {
		if !strings.EqualFold(lines[i], root) {
			t.Errorf("%s = %q, want the workspace drive %q", label, lines[i], root)
		}
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "inside.txt")); err != nil || !strings.Contains(string(data), "sandbox-ok") {
		t.Errorf("a write at the drive did not land in the real workspace: %q, %v", data, err)
	}
	nested := runIn(t, sandboxed, Command{Shell: "(Get-Location).Path", Dir: "sub"})
	if got := strings.TrimSpace(nested.Stdout); !strings.EqualFold(got, root+"sub") {
		t.Errorf("subdirectory location = %q\n%s", got, nested.Output)
	}

	if _, err := exec.LookPath("git"); err != nil {
		t.Log("git is not installed; skipping the git half")
		return
	}
	if err := os.WriteFile(filepath.Join(workspace, "tracked.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	identity := []string{"-c", "user.name=fastllm-test", "-c", "user.email=test@fastllm.invalid"}
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "tracked.txt"},
		append(append([]string{}, identity...), "commit", "-q", "-m", "first"),
	} {
		if result := runIn(t, sandboxed, Command{Executable: "git", Args: args}); result.ExitCode != 0 {
			t.Fatalf("git %s: exit %d\n%s", strings.Join(args, " "), result.ExitCode, result.Output)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, "tracked.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// Checkpoints are built on stash create, which writes a commit object.
	stash := runIn(t, sandboxed, Command{Executable: "git", Args: append(append([]string{}, identity...), "stash", "create")})
	if stash.ExitCode != 0 || len(strings.TrimSpace(stash.Stdout)) < 7 {
		t.Fatalf("git stash create: exit %d\n%s", stash.ExitCode, stash.Output)
	}
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

func TestAppContainerShellAndGitWork(t *testing.T) {
	// A temp directory sits under the user profile, which the container can
	// neither read nor list: exactly the case the workspace drive exists for.
	assertShellAndGitWork(t, t.TempDir())
}

func TestWorkspaceDriveLivesAsLongAsItsScopes(t *testing.T) {
	workspace := t.TempDir()
	m := NewManager()
	defer m.Close(context.Background())
	if err := m.Register(NewAppContainerBackend()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	open := func() *Scope {
		s, err := m.Open(context.Background(), Options{Workspace: workspace, Policy: LocalPolicy(), Backend: AppContainerBackendName, RequireIsolation: true})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		return s
	}
	first, second := open(), open()
	t.Cleanup(func() {
		if err := revokeWorkspaceIdentity(workspaceContainerName(first.Workspace())); err != nil {
			t.Errorf("revoke test workspace: %v", err)
		}
	})
	drive := strings.TrimSuffix(first.CommandWorkspace(), `\`)
	if second.CommandWorkspace() != first.CommandWorkspace() {
		t.Fatalf("two scopes on one workspace got %q and %q; they should share a letter", first.CommandWorkspace(), second.CommandWorkspace())
	}
	resolved, _ := filepath.EvalSymlinks(workspace)
	if target, ok := driveTarget(drive); !ok || !strings.EqualFold(target, resolved) {
		t.Fatalf("%s maps to %q (%v), want %q", drive, target, ok, resolved)
	}

	if err := first.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if target, ok := driveTarget(drive); !ok || !strings.EqualFold(target, resolved) {
		t.Fatalf("closing one scope removed the drive the other still uses: %q %v", target, ok)
	}
	if err := second.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if target, ok := driveTarget(drive); ok && strings.EqualFold(target, resolved) {
		t.Fatalf("%s still maps to the workspace after every scope closed", drive)
	}
}

func TestAppContainerSeparatesWorkspaceIdentities(t *testing.T) {
	firstRoot, secondRoot := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(firstRoot, "sentinel.txt"), []byte("workspace-one-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, _ := containerScopes(t, firstRoot)
	second, _ := containerScopes(t, secondRoot)
	firstIdentity := first.backend.(*appContainerScope).identity
	secondIdentity := second.backend.(*appContainerScope).identity
	if firstIdentity.sid.Equals(secondIdentity.sid) {
		t.Fatal("distinct workspaces share a SID")
	}
	target := first.CommandWorkspace() + "sentinel.txt"
	control := runIn(t, first, Command{Shell: "Get-Content -LiteralPath " + psLiteral(target)})
	if !strings.Contains(control.Output, "workspace-one-secret") {
		t.Fatalf("positive control failed: %s", control.Output)
	}
	denied := runIn(t, second, Command{Shell: "Write-Output " + sandboxStarted + "; Get-Content -LiteralPath " + psLiteral(target)})
	assertSandboxStarted(t, denied)
	if strings.Contains(denied.Output, "workspace-one-secret") {
		t.Fatalf("cross-workspace read: %s", denied.Output)
	}
	write := runIn(t, second, Command{Shell: "Write-Output " + sandboxStarted + "; Set-Content -LiteralPath " + psLiteral(first.CommandWorkspace()+"escaped.txt") + " -Value forbidden"})
	assertSandboxStarted(t, write)
	if _, err := os.Stat(filepath.Join(firstRoot, "escaped.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cross-workspace write: %v", err)
	}
	// Closing and reopening the same workspace reuses only its own grants.
	if err := first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	reopened, _ := containerScopes(t, firstRoot)
	if !reopened.backend.(*appContainerScope).identity.sid.Equals(firstIdentity.sid) {
		t.Fatal("workspace identity was not stable")
	}
	denied = runIn(t, second, Command{Shell: "Write-Output " + sandboxStarted + "; Get-Content -LiteralPath " + psLiteral(reopened.CommandWorkspace()+"sentinel.txt")})
	assertSandboxStarted(t, denied)
	if strings.Contains(denied.Output, "workspace-one-secret") {
		t.Fatal("reopening widened another workspace")
	}
}

func TestWorkspaceIdentityNames(t *testing.T) {
	name := workspaceContainerName(`C:\project`)
	if name != workspaceContainerName(`C:\project\.`) {
		t.Fatal("equivalent canonical paths differ")
	}
	if name == workspaceContainerName(`C:\another`) {
		t.Fatal("different paths collide")
	}
	if name == workspaceContainerName(`C:\PROJECT`) {
		t.Fatal("case-sensitive workspace names collide")
	}
	if !validWorkspaceContainerName(name) || validWorkspaceContainerName("../"+name) || validWorkspaceContainerName(appContainerName) {
		t.Fatal("invalid identity name validation")
	}
}

func TestAppContainerCannotReadOutsideTheWorkspace(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("FASTLLM-SECRET-7f3a"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	sandboxed, local := containerScopes(t, t.TempDir())
	read := Command{Shell: psLiteral(sandboxStarted) + "; Get-Content -LiteralPath " + psLiteral(secret)}

	if control := runIn(t, local, read); !strings.Contains(control.Stdout, "FASTLLM-SECRET-7f3a") {
		t.Fatalf("control: local backend could not read the secret, so this test proves nothing:\n%s", control.Output)
	}
	result := runIn(t, sandboxed, read)
	assertSandboxStarted(t, result)
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
		return Command{Shell: psLiteral(sandboxStarted) + "; Set-Content -LiteralPath " + psLiteral(filepath.Join(outside, name)) + " -Value escaped"}
	}

	runIn(t, local, write("control.txt"))
	if _, err := os.Stat(filepath.Join(outside, "control.txt")); err != nil {
		t.Fatalf("control: local backend could not write outside, so this test proves nothing: %v", err)
	}
	assertSandboxStarted(t, runIn(t, sandboxed, write("escape.txt")))
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
	connect := Command{Shell: psLiteral(sandboxStarted) + "; $ErrorActionPreference = 'Stop'; $c = New-Object Net.Sockets.TcpClient; if (-not $c.ConnectAsync('127.0.0.1', " + port + ").Wait(5000)) { throw 'connect timed out' }; 'connected'; $c.Close()"}
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
	assertSandboxStarted(t, result)
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

func TestAppContainerDescendantsInheritRestrictions(t *testing.T) {
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("DESCENDANT-SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	sandboxed, local := containerScopes(t, t.TempDir())
	child := func(target string) Command {
		script := psLiteral(sandboxStarted) + "; Get-Content -LiteralPath " + psLiteral(secret) + "; Set-Content -LiteralPath " + psLiteral(target) + " -Value escaped"
		args := joinCommandLine([]string{"-NoProfile", "-NonInteractive", "-Command", script})
		shell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		return Command{Shell: "$info = New-Object Diagnostics.ProcessStartInfo; $info.FileName = " + psLiteral(shell) + "; $info.Arguments = " + psLiteral(args) + "; $info.UseShellExecute = $false; $info.RedirectStandardOutput = $true; $info.RedirectStandardError = $true; $p = [Diagnostics.Process]::Start($info); $p.StandardOutput.ReadToEnd(); $p.StandardError.ReadToEnd(); $p.WaitForExit(); exit $p.ExitCode"}
	}
	controlPath := filepath.Join(outside, "control.txt")
	control := runIn(t, local, child(controlPath))
	assertSandboxStarted(t, control)
	if !strings.Contains(control.Stdout, "DESCENDANT-SECRET") {
		t.Fatalf("local descendant could not read control: %s", control.Output)
	}
	if _, err := os.Stat(controlPath); err != nil {
		t.Fatalf("local descendant could not write control: %v", err)
	}
	escape := filepath.Join(outside, "escape.txt")
	result := runIn(t, sandboxed, child(escape))
	assertSandboxStarted(t, result)
	if strings.Contains(result.Output, "DESCENDANT-SECRET") {
		t.Fatal("descendant read outside granted workspace")
	}
	if _, err := os.Stat(escape); !os.IsNotExist(err) {
		t.Fatalf("descendant wrote outside granted workspace: %v", err)
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

func TestRevokeRemovesTreeAndDirectoryGrants(t *testing.T) {
	shared := t.TempDir()
	sandboxed, _ := containerScopes(t, t.TempDir())
	identity := sandboxed.backend.(*appContainerScope).identity
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

	if granted, err := grantSelf(shared, identity.sid, fileReadAttributes); err != nil || !granted {
		t.Fatalf("grantSelf = %v, %v", granted, err)
	}
	if has, _ := hasSelfGrant(shared, identity.sid, fileReadAttributes); !has {
		t.Fatal("grantSelf left no grant")
	}
	if err := revokeSelf(shared, identity.sid); err != nil {
		t.Fatalf("revokeSelf: %v", err)
	}
	if direct, _ := directGrant(shared, identity.sid); direct {
		t.Fatal("revokeSelf left the grant in place")
	}
}

func TestSetupRejectsANonContainerIdentity(t *testing.T) {
	// The Users group: an elevated helper handed it must grant nothing.
	for _, sid := range []string{"S-1-5-32-545", "not-a-sid"} {
		if err := RevokeIsolationSetup(sid); err == nil {
			t.Errorf("RevokeIsolationSetup(%q) succeeded", sid)
		}
	}
}

func TestGrantRecordKeepsKindsAndDeduplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grants.txt")
	// Earlier versions wrote bare tree paths, and "tree\t"-prefixed ones.
	if err := os.WriteFile(path, []byte("C:\\legacy\ntree\tC:\\prefixed\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	record := grantRecord{path: path}
	for _, add := range []grantEntry{{treeGrant, `C:\ws`}, {treeGrant, `c:\WS`}, {selfGrant, `C:\ws`}, {selfGrant, `C:\Users\me`}} {
		if err := record.add(add.kind, add.path); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	entries, err := record.entries()
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	want := []grantEntry{{treeGrant, `C:\legacy`}, {treeGrant, `C:\prefixed`}, {treeGrant, `C:\ws`}, {selfGrant, `C:\ws`}, {selfGrant, `C:\Users\me`}}
	if len(entries) != len(want) {
		t.Fatalf("entries = %v, want %v", entries, want)
	}
	for i := range want {
		if entries[i] != want[i] {
			t.Errorf("entry %d = %v, want %v", i, entries[i], want[i])
		}
	}
}
