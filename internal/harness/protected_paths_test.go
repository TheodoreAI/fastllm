package harness

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"fastllm/internal/files"
	"fastllm/internal/gitrepo"
)

// gitWorkspace makes a git repository with one commit, as a user's project.
func gitWorkspace(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func editModeContext(r *Runner, root string) toolExecutionContext {
	req := RunRequest{PermissionMode: PermissionEdit, WorkingDir: root}
	return toolExecutionContext{ctx: context.Background(), request: req, workingDir: root,
		fileReader: files.New(root, policyForRequest(req).write)}
}

// I4: edit mode never starts a process. Writing core.fsmonitor into
// .git/config would make the harness's own background `git status` run a
// command of the model's choosing, so the write itself must be refused and
// the harness's git must not honour the setting even if it is already there.
func TestEditModeCannotPlantGitFsmonitor(t *testing.T) {
	root := gitWorkspace(t)
	r := NewRunner(nil, root, "test")
	defer r.Close()

	marker := filepath.ToSlash(filepath.Join(root, "pwned.txt"))
	config := "[core]\n\trepositoryformatversion = 0\n\tbare = false\n\tfsmonitor = \"echo pwned > '" + marker + "'\"\n"
	args := `{"path":".git/config","content":` + jsonString(config) + `}`
	out := r.executeTool(editModeContext(r, root), "write_file", args).output

	if _, err := gitrepo.GetRepoStatus(context.Background(), root); err != nil {
		t.Fatalf("git status: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("edit mode ran a process through core.fsmonitor (write said: %s)", out)
	}
	if !strings.Contains(out, "denied") {
		t.Fatalf("writing .git/config should be denied, got: %s", out)
	}
}

func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Git config already present (written by the user, or by an earlier session
// before this protection existed) must not make the harness's own git calls
// run a command either.
func TestHarnessGitIgnoresCommandsInRepoConfig(t *testing.T) {
	root := gitWorkspace(t)
	marker := filepath.ToSlash(filepath.Join(root, "ran.txt"))
	// Stage first: this unhardened setup call would itself trigger the hook.
	add := exec.Command("git", "add", "README.md")
	add.Dir = root
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v %s", err, out)
	}
	for _, kv := range [][2]string{
		{"core.fsmonitor", "echo fsmonitor > '" + marker + "'"},
		{"diff.external", "sh -c 'echo diff > \"" + marker + "\"'"},
	} {
		cmd := exec.Command("git", "config", kv[0], kv[1])
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git config: %v %s", err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := gitrepo.GetRepoStatus(ctx, root); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.Diff(ctx, root, "README.md", false); err != nil {
		t.Fatal(err)
	}
	NewCheckpointManager(root).IsGitRepo()
	if data, err := os.ReadFile(marker); err == nil {
		t.Fatalf("a harness git call ran a command from repo config: %q", data)
	}
}

func TestClassifyWritePath(t *testing.T) {
	root := gitWorkspace(t)
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	cases := map[string]pathProtection{
		".git/config":                       protectGit,
		".GIT/hooks/pre-commit":             protectGit,
		"./.git/../.git/config":             protectGit,
		"vendor/lib/.git/config":            protectGit,
		".fastllm/config.json":              protectConfig,
		"AGENTS.md":                         protectConfig,
		"docs/claude.md":                    protectConfig,
		".cursorrules":                      protectConfig,
		".claude/skills/deploy/SKILL.md":    protectConfig,
		".agents/skills/x/SKILL.md":         protectConfig,
		"README.md":                         protectNone,
		".gitignore":                        protectNone,
		".github/workflows/ci.yml":          protectNone,
		"docs/skills.md":                    protectNone,
		filepath.Join(root, ".git", "HEAD"): protectGit, // absolute
	}
	for path, want := range cases {
		if got := classifyWritePath(root, path); got != want {
			t.Errorf("%s: got %d, want %d", path, got, want)
		}
	}

	// A symlink pointing into .git is judged by where it leads.
	if err := os.Symlink(filepath.Join(root, ".git"), filepath.Join(root, "innocent")); err == nil {
		if got := classifyWritePath(root, "innocent/config"); got != protectGit {
			t.Errorf("symlink into .git: got %d", got)
		}
	}
	// Windows 8.3 short names: GIT~1 is .git where short names are enabled.
	if _, err := os.Stat(filepath.Join(root, "GIT~1")); err == nil {
		if got := classifyWritePath(root, "GIT~1/config"); got != protectGit {
			t.Errorf("short name GIT~1: got %d", got)
		}
	}
}

func TestProtectedWritesAcrossModes(t *testing.T) {
	root := gitWorkspace(t)
	for _, mode := range everyMode {
		req := RunRequest{PermissionMode: mode, WorkingDir: root, AllowCommands: true}
		if d := authorize(req, "write_file", `{"path":".git/hooks/pre-commit","content":"x"}`); d != Deny {
			t.Errorf("%s: .git write decided %s", mode, d)
		}
		if d := authorize(req, "edit_file", `{"path":"AGENTS.md","search":"a","replace":"b"}`); d == Allow {
			t.Errorf("%s: a rule-file edit was allowed without asking", mode)
		}
	}

	// Full mode with nobody to ask: configuration writes are refused.
	headless := RunRequest{PermissionMode: PermissionFull, WorkingDir: root}
	if ok, why := admit(headless, "write_file", `{"path":".fastllm/config.json","content":"{}"}`); ok || !strings.Contains(why, "cannot ask") {
		t.Fatalf("headless full wrote fastllm config: %v %s", ok, why)
	}

	// Full mode with a user: it asks, under its own label, and shows why.
	var askedAs, askedSummary string
	interactive := RunRequest{PermissionMode: PermissionFull, WorkingDir: root,
		Authorize: func(c ConsentRequest) bool { askedAs, askedSummary = c.Tool, c.Summary; return true }}
	if ok, _ := admit(interactive, "write_file", `{"path":"CLAUDE.md","content":"x"}`); !ok {
		t.Fatal("an approved configuration write should proceed")
	}
	if askedAs != "write_file (fastllm configuration)" || !strings.Contains(askedSummary, "future sessions") {
		t.Fatalf("asked as %q: %q", askedAs, askedSummary)
	}
	// Ordinary writes in full mode still do not ask.
	askedAs = ""
	if ok, _ := admit(interactive, "write_file", `{"path":"main.go","content":"x"}`); !ok || askedAs != "" {
		t.Fatalf("an ordinary full-mode write asked as %q", askedAs)
	}
}

// A session grant for ordinary writes must not cover configuration writes.
func TestSessionGrantDoesNotCoverConfigWrites(t *testing.T) {
	root := gitWorkspace(t)
	pc := NewPermissionController(PermissionAgent, nil)
	pc.SetWorkspace(root)
	pc.Grant("write_file")
	var prompted []string
	req := RunRequest{PermissionMode: PermissionAgent, WorkingDir: root,
		Authorize: func(c ConsentRequest) bool {
			if pc.Covers(c) {
				return true
			}
			prompted = append(prompted, c.Tool)
			return false
		}}
	if ok, _ := admit(req, "write_file", `{"path":"notes.txt","content":"x"}`); !ok {
		t.Fatal("the session grant should cover an ordinary write")
	}
	if ok, _ := admit(req, "write_file", `{"path":".fastllm/rules.md","content":"x"}`); ok || len(prompted) != 1 {
		t.Fatalf("a configuration write rode on the session grant (prompted %v)", prompted)
	}
}

func TestFileLayerRefusesGitInternals(t *testing.T) {
	root := gitWorkspace(t)
	reader := files.New(root, true)
	for _, path := range []string{".git/config", ".Git/hooks/post-checkout", "sub/.git/x"} {
		if err := reader.Write(path, "x"); err == nil || !strings.Contains(err.Error(), ".git") {
			t.Errorf("file layer wrote %s: %v", path, err)
		}
	}
	if err := reader.Write("ok.txt", "x"); err != nil {
		t.Fatalf("ordinary write refused: %v", err)
	}
}
