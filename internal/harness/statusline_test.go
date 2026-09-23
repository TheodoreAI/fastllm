package harness

import (
	"os"
	"strings"
	"testing"
)

func stripANSI(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		out.WriteByte(s[i])
	}
	return out.String()
}

func TestStatusLineShowsTheSessionAtAGlance(t *testing.T) {
	got := stripANSI(FormatStatusLine(StatusLine{
		Model:          "glm-5.3",
		PermissionMode: PermissionAgent,
		ContextChars:   24_000,
		ContextBudget:  60_000,
		Turns:          5,
		TotalTokens:    12_400,
		Cost:           0.42,
		WorkingDir:     "/projects/fastllm",
		Branch:         "main",
	}))
	for _, want := range []string{"glm-5.3", "agent", "ctx 24k/60k (40%)", "5 turns", "12k tok", "$0.42", "/projects/fastllm", "main"} {
		if !strings.Contains(got, want) {
			t.Errorf("status line missing %q:\n%s", want, got)
		}
	}
}

func TestStatusLineOmitsEmptyFields(t *testing.T) {
	// A fresh session has no turns, tokens or cost yet; showing "0 turns · 0 tok
	// · $0.00" is noise, not information.
	got := stripANSI(FormatStatusLine(StatusLine{Model: "llama3.1", PermissionMode: PermissionAgent}))
	for _, unwanted := range []string{"turns", "tok", "$"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("empty field %q rendered: %s", unwanted, got)
		}
	}
	if !strings.Contains(got, "llama3.1") {
		t.Errorf("model should still show: %s", got)
	}
}

func TestStatusLineShowsShellModeInsteadOfModel(t *testing.T) {
	got := stripANSI(FormatStatusLine(StatusLine{Model: "glm-5.3", ShellMode: true}))
	if !strings.Contains(got, "shell") || strings.Contains(got, "glm-5.3") {
		t.Errorf("shell mode should replace the model badge: %s", got)
	}
}

func TestCompactCountStaysShort(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want string
	}{{999, "999"}, {1_200, "1.2k"}, {24_000, "24k"}, {3_400_000, "3.4M"}} {
		if got := compactCount(tc.in); got != tc.want {
			t.Errorf("compactCount(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCurrentBranchReadsHeadWithoutSubprocess(t *testing.T) {
	dir := t.TempDir()
	writeGitHead(t, dir, "ref: refs/heads/feature/status-line\n")
	if got := currentBranch(dir); got != "feature/status-line" {
		t.Errorf("branch = %q", got)
	}
	writeGitHead(t, dir, "9f6c1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f\n")
	if got := currentBranch(dir); got != "9f6c1a2" {
		t.Errorf("detached HEAD should show a short sha, got %q", got)
	}
	if got := currentBranch(t.TempDir()); got != "" {
		t.Errorf("a non-repo should yield no branch, got %q", got)
	}
}

func writeGitHead(t *testing.T, dir, content string) {
	t.Helper()
	gitDir := dir + "/.git"
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gitDir+"/HEAD", []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
