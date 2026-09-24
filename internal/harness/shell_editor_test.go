package harness

import (
	"os"
	"runtime"
	"testing"
)

func TestClassifyShellCommandTUIEditors(t *testing.T) {
	cases := []struct {
		input      string
		wantBin    string
		wantTarget string
	}{
		{"nano main.go", "nano", "main.go"},
		{"vim config.yaml", "vim", "config.yaml"},
		{"nvim internal/harness/tui_tea.go", "nvim", "internal/harness/tui_tea.go"},
		{"micro README.md", "micro", "README.md"},
		{"hx src/main.rs", "hx", "src/main.rs"},
		{"less output.log", "less", "output.log"},
		{"sudo nano /etc/hosts", "nano", "/etc/hosts"},
	}

	for _, tc := range cases {
		spec := ClassifyShellCommand(tc.input, ".")
		if spec.Kind != CmdKindTUIEditor {
			t.Errorf("cmd %q: want CmdKindTUIEditor, got %v", tc.input, spec.Kind)
		}
		if spec.BinaryName != tc.wantBin {
			t.Errorf("cmd %q: want binary %q, got %q", tc.input, tc.wantBin, spec.BinaryName)
		}
		if spec.TargetFile != tc.wantTarget {
			t.Errorf("cmd %q: want target %q, got %q", tc.input, tc.wantTarget, spec.TargetFile)
		}
		if spec.Cmd == nil {
			t.Errorf("cmd %q: expected non-nil exec.Cmd", tc.input)
		}
	}
}

func TestClassifyShellCommandStandardCommands(t *testing.T) {
	standards := []string{
		"cat main.go",
		"head -n 20 config.yaml",
		"tail -f log.txt",
		"ls -la",
		"git status",
		"go test ./...",
		"echo 'hello world' > out.txt",
		"grep -rn 'func' .",
	}

	for _, cmd := range standards {
		spec := ClassifyShellCommand(cmd, ".")
		if spec.Kind != CmdKindStandard {
			t.Errorf("cmd %q: want CmdKindStandard, got %v", cmd, spec.Kind)
		}
	}
}

func TestClassifyShellCommandGUIEditors(t *testing.T) {
	// "code file.go" should be classified as GUI editor
	spec := ClassifyShellCommand("code file.go", ".")
	if spec.Kind != CmdKindGUIEditor {
		t.Errorf("code file.go: want CmdKindGUIEditor, got %v", spec.Kind)
	}
	if spec.BinaryName != "code" {
		t.Errorf("code file.go: want binary 'code', got %q", spec.BinaryName)
	}
	if spec.TargetFile != "file.go" {
		t.Errorf("code file.go: want target 'file.go', got %q", spec.TargetFile)
	}
}

func TestClassifyShellCommandNotepad(t *testing.T) {
	spec := ClassifyShellCommand("notepad notes.txt", ".")
	// On all platforms, notepad is handled either as native Windows notepad or mapped GUI/editor
	if spec.Kind == CmdKindStandard {
		t.Errorf("notepad notes.txt: should not be classified as standard headless command")
	}
	if spec.TargetFile != "notes.txt" {
		t.Errorf("notepad notes.txt: want target 'notes.txt', got %q", spec.TargetFile)
	}
	if spec.Cmd == nil {
		t.Errorf("notepad notes.txt: expected non-nil exec.Cmd")
	}
}

func TestResolveDefaultEditorWithEnv(t *testing.T) {
	origEditor := os.Getenv("EDITOR")
	defer os.Setenv("EDITOR", origEditor)

	// Test TUI editor from $EDITOR
	os.Setenv("EDITOR", "vim -u NONE")
	spec := ResolveDefaultEditor([]string{"test.txt"}, "/tmp")
	if spec.Kind != CmdKindTUIEditor {
		t.Errorf("want CmdKindTUIEditor, got %v", spec.Kind)
	}
	if spec.BinaryName != "vim" {
		t.Errorf("want binary 'vim', got %q", spec.BinaryName)
	}
	if spec.TargetFile != "test.txt" {
		t.Errorf("want target 'test.txt', got %q", spec.TargetFile)
	}

	// Test GUI editor from $EDITOR
	os.Setenv("EDITOR", "code --wait")
	spec = ResolveDefaultEditor([]string{"test.txt"}, "/tmp")
	if spec.Kind != CmdKindGUIEditor {
		t.Errorf("want CmdKindGUIEditor, got %v", spec.Kind)
	}
	if spec.BinaryName != "code" {
		t.Errorf("want binary 'code', got %q", spec.BinaryName)
	}
}

func TestResolveDefaultEditorWithoutEnv(t *testing.T) {
	origEditor := os.Getenv("EDITOR")
	origVisual := os.Getenv("VISUAL")
	defer func() {
		os.Setenv("EDITOR", origEditor)
		os.Setenv("VISUAL", origVisual)
	}()

	os.Unsetenv("EDITOR")
	os.Unsetenv("VISUAL")

	spec := ResolveDefaultEditor([]string{"test.txt"}, "/tmp")
	if runtime.GOOS == "windows" {
		if spec.Kind != CmdKindGUIEditor || spec.BinaryName != "notepad" {
			t.Errorf("windows default: want notepad GUI, got %v (%s)", spec.Kind, spec.BinaryName)
		}
	} else {
		if spec.Kind != CmdKindTUIEditor {
			t.Errorf("unix default: want TUI editor, got %v", spec.Kind)
		}
		if spec.BinaryName != "nano" && spec.BinaryName != "vim" && spec.BinaryName != "vi" {
			t.Errorf("unix default: unexpected binary %q", spec.BinaryName)
		}
	}
}

func TestClassifyShellCommandEditAlias(t *testing.T) {
	spec := ClassifyShellCommand("edit my_script.py", ".")
	if spec.TargetFile != "my_script.py" {
		t.Errorf("edit my_script.py: want target 'my_script.py', got %q", spec.TargetFile)
	}
	if spec.Kind == CmdKindStandard {
		t.Errorf("edit my_script.py: should resolve to an editor, not standard command")
	}
}

func TestClassifyShellCommandGH(t *testing.T) {
	tests := []struct {
		cmd  string
		want CommandKind
	}{
		{"gh auth login", CmdKindInteractive},
		{"gh auth refresh", CmdKindInteractive},
		{"gh auth switch", CmdKindInteractive},
		{"gh auth setup-git", CmdKindInteractive},
		{"gh auth status", CmdKindStandard},
		{"gh auth token", CmdKindStandard},
		{"gh copilot", CmdKindInteractive},
		{"gh browse", CmdKindInteractive},
		{"gh pr view 42 --web", CmdKindInteractive},
		{"gh pr list -w", CmdKindInteractive},
		{"gh pr create", CmdKindInteractive},
		{"gh pr create --title 'Fix' --body 'Done'", CmdKindStandard},
		{"gh pr create -t Fix -b Done", CmdKindStandard},
		{"gh pr create --fill", CmdKindStandard},
		{"gh pr create --fill-first", CmdKindStandard},
		{"gh pr checkout", CmdKindInteractive},
		{"gh pr checkout 123", CmdKindStandard},
		{"gh pr checkout feat-branch", CmdKindStandard},
		{"gh pr list", CmdKindStandard},
		{"gh pr status", CmdKindStandard},
		{"gh pr checks", CmdKindStandard},
		{"gh pr diff", CmdKindStandard},
		{"gh run list", CmdKindStandard},
		{"gh run view 123", CmdKindStandard},
		{"gh issue create", CmdKindInteractive},
		{"gh issue create -t bug -b broken", CmdKindStandard},
		{"gh issue list", CmdKindStandard},
		{"gh repo create", CmdKindInteractive},
		{"gh repo fork", CmdKindInteractive},
		{"gh repo fork --clone=true", CmdKindStandard},
		{"gh repo clone owner/repo", CmdKindStandard},
	}

	for _, tc := range tests {
		spec := ClassifyShellCommand(tc.cmd, ".")
		if spec.Kind != tc.want {
			t.Errorf("command %q: want kind %v, got %v", tc.cmd, tc.want, spec.Kind)
		}
		if tc.want == CmdKindInteractive && spec.Cmd == nil {
			t.Errorf("command %q: expected non-nil exec.Cmd for interactive kind", tc.cmd)
		}
	}
}

func TestClassifyShellCommandGit(t *testing.T) {
	tests := []struct {
		cmd  string
		want CommandKind
	}{
		{"git commit", CmdKindInteractive},
		{"git commit -m 'Initial commit'", CmdKindStandard},
		{"git commit --message 'Initial commit'", CmdKindStandard},
		{"git commit -m 'Fix' -e", CmdKindInteractive},
		{"git commit --no-edit", CmdKindStandard},
		{"git add -p", CmdKindInteractive},
		{"git add --patch", CmdKindInteractive},
		{"git add -i", CmdKindInteractive},
		{"git add .", CmdKindStandard},
		{"git rebase -i HEAD~2", CmdKindInteractive},
		{"git rebase main", CmdKindStandard},
		{"git checkout -p", CmdKindInteractive},
		{"git reset -p", CmdKindInteractive},
		{"git restore -p", CmdKindInteractive},
		{"git status", CmdKindStandard},
		{"git log --oneline -n 10", CmdKindStandard},
		{"git diff", CmdKindStandard},
	}

	for _, tc := range tests {
		spec := ClassifyShellCommand(tc.cmd, ".")
		if spec.Kind != tc.want {
			t.Errorf("command %q: want kind %v, got %v", tc.cmd, tc.want, spec.Kind)
		}
	}
}

func TestClassifyShellCommandREPLsAndTUIs(t *testing.T) {
	tests := []struct {
		cmd  string
		want CommandKind
	}{
		{"python", CmdKindInteractive},
		{"python -i", CmdKindInteractive},
		{"python main.py", CmdKindStandard},
		{"python -c 'print(1)'", CmdKindStandard},
		{"node", CmdKindInteractive},
		{"node server.js", CmdKindStandard},
		{"lazygit", CmdKindInteractive},
		{"htop", CmdKindInteractive},
		{"fzf", CmdKindInteractive},
		{"sudo htop", CmdKindInteractive},
		{"bash", CmdKindInteractive},
		{"bash script.sh", CmdKindStandard},
		{"zsh", CmdKindInteractive},
	}

	for _, tc := range tests {
		spec := ClassifyShellCommand(tc.cmd, ".")
		if spec.Kind != tc.want {
			t.Errorf("command %q: want kind %v, got %v", tc.cmd, tc.want, spec.Kind)
		}
	}
}

func TestClassifyShellCommandOverrides(t *testing.T) {
	// Colon prefix forces interactive
	spec := ClassifyShellCommand(":my-script.sh", ".")
	if spec.Kind != CmdKindInteractive {
		t.Errorf("want CmdKindInteractive for :my-script.sh, got %v", spec.Kind)
	}

	// 'run -i' prefix forces interactive
	spec2 := ClassifyShellCommand("run -i my-script.sh", ".")
	if spec2.Kind != CmdKindInteractive {
		t.Errorf("want CmdKindInteractive for 'run -i my-script.sh', got %v", spec2.Kind)
	}

	// 'term' prefix forces interactive
	spec3 := ClassifyShellCommand("term my-script.sh", ".")
	if spec3.Kind != CmdKindInteractive {
		t.Errorf("want CmdKindInteractive for 'term my-script.sh', got %v", spec3.Kind)
	}

	// 'run -b' forces standard (batch)
	spec4 := ClassifyShellCommand("run -b gh pr create", ".")
	if spec4.Kind != CmdKindStandard {
		t.Errorf("want CmdKindStandard for 'run -b gh pr create', got %v", spec4.Kind)
	}
}

