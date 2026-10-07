//go:build !windows

package execution

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A pipe-only test misses zsh's attempt to acquire the UI's controlling tty.
func TestZshUnderControllingTerminal(t *testing.T) {
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("zsh unavailable")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("gh() { printf 'ISSUE_FIXTURE:%s:%s\\n' \"$1\" \"$2\"; }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	const script = `import os, pty, signal, sys
pid, fd = pty.fork()
if pid == 0:
    os.environ['FASTLLM_ZSH_PTY_HELPER'] = '1'
    os.environ['HOME'] = sys.argv[2]
    os.environ['ZDOTDIR'] = sys.argv[2]
    os.execv(sys.argv[1], [sys.argv[1], '-test.run=^TestZshPTYHelper$', '-test.v'])
def timeout(signum, frame):
    raise TimeoutError('zsh PTY helper timed out')
signal.signal(signal.SIGALRM, timeout)
signal.alarm(15)
try:
    while True:
        try:
            data = os.read(fd, 4096)
        except OSError:
            break
        if not data:
            break
        sys.stdout.buffer.write(data)
        sys.stdout.buffer.flush()
    _, status = os.waitpid(pid, 0)
    pid = 0
finally:
    signal.alarm(0)
    os.close(fd)
    if pid:
        os.killpg(pid, signal.SIGKILL)
        os.waitpid(pid, 0)
sys.exit(os.waitstatus_to_exitcode(status))
`
	out, err := exec.Command(python, "-c", script, binary, home).CombinedOutput()
	if err != nil {
		t.Fatalf("PTY shell: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "PASS") {
		t.Fatalf("helper did not pass: %s", out)
	}
}

func TestZshPTYHelper(t *testing.T) {
	if os.Getenv("FASTLLM_ZSH_PTY_HELPER") != "1" {
		t.Skip("PTY helper")
	}
	command, err := UserShellCommand("zsh", "gh issue list")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := RunLocal(ctx, os.Getenv("HOME"), LocalPolicy(), command)
	if err != nil {
		t.Fatal(err)
	}
	if err := result.Err(); err != nil {
		t.Fatalf("zsh failed: %v: %s", err, result.Output)
	}
	if !strings.Contains(result.Stdout, "ISSUE_FIXTURE:issue:list") {
		t.Fatalf("startup function did not run: %s", result.Output)
	}
}
