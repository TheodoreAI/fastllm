package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverWorkspaceRules(t *testing.T) {
	tempDir := t.TempDir()

	agentsFile := filepath.Join(tempDir, "AGENTS.md")
	if err := os.WriteFile(agentsFile, []byte("# Agents Rule\nAlways write tests."), 0644); err != nil {
		t.Fatal(err)
	}

	fastllmDir := filepath.Join(tempDir, ".fastllm")
	if err := os.MkdirAll(fastllmDir, 0755); err != nil {
		t.Fatal(err)
	}
	rulesFile := filepath.Join(fastllmDir, "rules")
	if err := os.WriteFile(rulesFile, []byte("Never commit API keys."), 0644); err != nil {
		t.Fatal(err)
	}

	subDir := filepath.Join(tempDir, "src", "cmd")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Discover rules from subDir
	rules := DiscoverWorkspaceRules(subDir)
	if len(rules) < 2 {
		t.Fatalf("expected at least 2 rule files, got %d", len(rules))
	}

	prompt := FormatRulesForPrompt(rules)
	if !strings.Contains(prompt, "Always write tests") {
		t.Errorf("prompt missing AGENTS.md rule: %s", prompt)
	}
	if !strings.Contains(prompt, "Never commit API keys") {
		t.Errorf("prompt missing .fastllm/rules: %s", prompt)
	}
}
