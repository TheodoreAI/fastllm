package harness

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeTree creates each file (slash path relative to root) with its content.
func writeTree(t *testing.T, root string, tree map[string]string) {
	t.Helper()
	for rel, content := range tree {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestParseIgnoreFollowsGitignoreSemantics(t *testing.T) {
	rules := parseIgnore(strings.Join([]string{
		"# comment",
		"*.log",
		"!keep.log",
		"out/",
		"/root-only.txt",
		"docs/*.md",
		"**/tmp",
		"",
	}, "\n"), "")

	cases := []struct {
		path  string
		isDir bool
		want  bool
	}{
		{"app.log", false, true},
		{"deep/nested/app.log", false, true},
		{"keep.log", false, false},
		{"out", true, true},
		{"src/out", true, true},
		{"out", false, false}, // dir-only rule, but a file named out
		{"root-only.txt", false, true},
		{"sub/root-only.txt", false, false},
		{"docs/a.md", false, true},
		{"docs/sub/a.md", false, false},
		{"a/b/tmp", true, true},
		{"main.go", false, false},
	}
	for _, c := range cases {
		if got := rules.matches(c.path, c.isDir); got != c.want {
			t.Errorf("matches(%q, dir=%v) = %v; want %v", c.path, c.isDir, got, c.want)
		}
	}
	if !rules.hidesFile("out/bin/tool") {
		t.Error("a file inside an ignored directory is not hidden")
	}

	// Rules from a nested .gitignore only apply below their directory.
	nested := parseIgnore("*.txt\n/local\n", "sub")
	if !nested.matches("sub/a/notes.txt", false) || nested.matches("notes.txt", false) {
		t.Error("nested unanchored rule escaped its directory")
	}
	if !nested.matches("sub/local", false) || nested.matches("sub/a/local", false) {
		t.Error("nested anchored rule matched the wrong depth")
	}
}

// Outside git, the walk skips the default folders and honours .gitignore
// files at every level plus .fastllmignore.
func TestWorkspaceFilesOutsideGit(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"src/main.py":               "print(1)",
		".venv/lib/site.py":         "x",
		"node_modules/pkg/index.js": "x",
		"__pycache__/main.pyc":      "x",
		"target/debug/app":          "x",
		".gitignore":                "*.log\n!keep.log\nsecret_dir/\n",
		"app.log":                   "x",
		"keep.log":                  "x",
		"secret_dir/a.txt":          "x",
		"sub/.gitignore":            "local.txt\n",
		"sub/local.txt":             "x",
		"sub/other.txt":             "x",
		".fastllmignore":            "fixtures/\n",
		"fixtures/big.json":         "{}",
	})

	got, err := workspaceFiles(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".fastllmignore", ".gitignore", "keep.log", "src/main.py", "sub/.gitignore", "sub/other.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("workspaceFiles = %v\nwant %v", got, want)
	}
}

// In a repository, git decides for tracked files, the default folders apply
// to untracked ones, and .fastllmignore hides tracked files too.
func TestWorkspaceFilesInGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"build/generate.go": "package build", // tracked source in a default-list folder
		"docs/guide.md":     "# guide",
		"src/new.go":        "package src",
		".venv/lib/x.py":    "x", // untracked and not in .gitignore
		".fastllmignore":    "docs/\n",
	})
	for _, args := range [][]string{{"init", "-q"}, {"add", "build/generate.go", "docs/guide.md"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	got, err := workspaceFiles(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".fastllmignore", "build/generate.go", "src/new.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("workspaceFiles = %v\nwant %v", got, want)
	}
}

func TestSearchFilesSkipsBinaryAndIgnored(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"src/app.py":          "needle = 1",
		"data/blob.bin":       "needle\x00\x01\x02",
		".venv/lib/needle.py": "needle = 2",
	})
	r := NewRunner(&mockLLM{}, root, "test-model")
	out := r.executeSearchFiles(context.Background(), root, "needle", "")
	if !strings.Contains(out, "src/app.py") {
		t.Fatalf("search missed the source file:\n%s", out)
	}
	if strings.Contains(out, "blob.bin") || strings.Contains(out, ".venv") {
		t.Fatalf("search returned a binary or ignored file:\n%s", out)
	}
	if list := r.executeListFiles(context.Background(), root, ""); strings.Contains(list, ".venv") {
		t.Fatalf("list_files showed .venv:\n%s", list)
	}
}
