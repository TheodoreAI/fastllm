package execution

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestUserShellCommandPreservesCommandAsOneArgument(t *testing.T) {
	name := "bash"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	text := `copilot-comments 'argument with spaces'; printf '%s' '$(literal)'`
	cmd, err := UserShellCommand(path, text)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Executable != path || len(cmd.Args) != 2 || cmd.Args[0] != "-lic" || cmd.Args[1] != text || cmd.Shell != "" {
		t.Fatalf("unsafe shell arguments: %+v", cmd)
	}
	if _, err := UserShellCommand(filepath.Join(t.TempDir(), "missing-shell"), text); err == nil {
		t.Fatal("missing shell silently fell back")
	}
}
