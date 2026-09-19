package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RuleFile represents a discovered project rule file.
type RuleFile struct {
	Path     string `json:"path"`
	Filename string `json:"filename"`
	Content  string `json:"content"`
}

var candidateRuleFiles = []string{
	"AGENTS.md",
	"CLAUDE.md",
	filepath.Join(".fastllm", "rules"),
	filepath.Join(".fastllm", "rules.md"),
	".cursorrules",
	".windsurfrules",
}

// DiscoverWorkspaceRules searches the working directory and its parent directories
// for known workspace instruction files (AGENTS.md, CLAUDE.md, .fastllm/rules, etc.).
func DiscoverWorkspaceRules(workingDir string) []RuleFile {
	var rules []RuleFile
	absDir, err := filepath.Abs(workingDir)
	if err != nil {
		return rules
	}

	visited := make(map[string]bool)
	current := absDir

	for {
		for _, relPath := range candidateRuleFiles {
			fullPath := filepath.Join(current, relPath)
			if visited[fullPath] {
				continue
			}
			visited[fullPath] = true

			info, err := os.Stat(fullPath)
			if err == nil && !info.IsDir() {
				data, err := os.ReadFile(fullPath)
				if err == nil && len(strings.TrimSpace(string(data))) > 0 {
					rel, _ := filepath.Rel(absDir, fullPath)
					if rel == "" {
						rel = filepath.Base(fullPath)
					}
					rules = append(rules, RuleFile{
						Path:     fullPath,
						Filename: rel,
						Content:  string(data),
					})
				}
			}
		}

		// Stop if we hit a git root or filesystem root
		if isRepoRoot(current) {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}

	return rules
}

// FormatRulesForPrompt formats discovered rules into a system prompt section.
func FormatRulesForPrompt(rules []RuleFile) string {
	if len(rules) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n\n# Project Rules and Guidelines\n")
	sb.WriteString("The following project-specific rules were discovered in the workspace. Follow them strictly:\n\n")

	for _, rule := range rules {
		sb.WriteString(fmt.Sprintf("## Rules from %s:\n%s\n\n", rule.Filename, strings.TrimSpace(rule.Content)))
	}

	return sb.String()
}

func isRepoRoot(dir string) bool {
	gitDir := filepath.Join(dir, ".git")
	info, err := os.Stat(gitDir)
	return err == nil && (info.IsDir() || !info.IsDir())
}
