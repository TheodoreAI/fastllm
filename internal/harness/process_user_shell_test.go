package harness

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"fastllm/internal/execution"
)

func bashFixture(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		path := `C:\Program Files\Git\bin\bash.exe`
		if _, err := os.Stat(path); err == nil {
			return path
		}
		t.Skip("Git Bash is not installed")
	}
	path, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("Bash is not installed")
	}
	return path
}

func TestUserShellRunsBashProfileAliasAndFunction(t *testing.T) {
	bash := bashFixture(t)
	home := t.TempDir()
	t.Setenv("HOME", filepath.ToSlash(home))
	t.Setenv("SHELL", bash)
	fixtures := map[string]string{
		".bash_profile":     "source \"$HOME/.bashrc\"\n",
		".bashrc":           "alias copilot-comments='bash \"$HOME/check-comments.sh\"'\nprofile_function() { printf 'FUNCTION:%s\\n' \"$1\"; }\nexport PATH=\"$HOME/bin:$PATH\"\n",
		"check-comments.sh": "printf 'COMMENTS:%s\\n' \"$1\"\n",
		"bin/profile-cli":   "#!/bin/sh\nprintf 'PROFILE_PATH\\n'\n",
	}
	for name, data := range fixtures {
		path := filepath.Join(home, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pm := NewProcessManager()
	pm.SetUserShell(bash)
	t.Cleanup(func() {
		if pm.owner != nil {
			pm.owner.Close(context.Background())
		}
	})
	for _, test := range []struct{ command, want string }{
		{`copilot-comments 'spaces ; $(literal)'`, "COMMENTS:spaces ; $(literal)"},
		{`profile_function 'two words'`, "FUNCTION:two words"},
		{`profile-cli`, "PROFILE_PATH"},
	} {
		p, bp, err := pm.StartTracked(test.command, home)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		result, err := p.Wait(ctx)
		cancel()
		if err != nil || result.Err() != nil || !strings.Contains(result.Output, test.want) {
			t.Fatalf("%s: %+v %v", test.command, result, err)
		}
		_, output, err := pm.Status(bp.ID)
		if err != nil || !strings.Contains(output, test.want) {
			t.Fatal("background log lost profile command output")
		}
	}
	p, _, err := pm.StartTracked("exit 7", home)
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Wait(context.Background())
	if err != nil || result.ExitCode != 7 {
		t.Fatalf("exit status lost: %+v %v", result, err)
	}
	// Stop the profile shell while a child is running; process ownership must
	// still cover the whole command rather than only its outer Bash process.
	long, bp, err := pm.StartTracked("printf READY; sleep 30", home)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		_, out, _ := pm.Status(bp.ID)
		if strings.Contains(out, "READY") {
			ready = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("profile shell did not start")
	}
	if err := pm.Kill(bp.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	killed, err := long.Wait(ctx)
	cancel()
	if err != nil || killed.Reason != "canceled" {
		t.Fatalf("profile shell cancellation failed: %+v %v", killed, err)
	}
	// The agent path uses its injected scope; no startup file executes there.
	manager := execution.NewManager()
	t.Cleanup(func() { manager.Close(context.Background()) })
	scope, err := manager.Open(context.Background(), execution.Options{Workspace: home, Policy: execution.LocalPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	agentPM := NewProcessManager(scope)
	agentPM.SetUserShell(bash)
	agent, _, err := agentPM.StartTracked("profile_function agent", home)
	if err != nil {
		t.Fatal(err)
	}
	agentResult, err := agent.Wait(context.Background())
	if err != nil || strings.Contains(agentResult.Output, "FUNCTION:agent") {
		t.Fatal("agent command loaded user profile")
	}
}
