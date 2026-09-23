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

	// Discovery walks upward, but the prompt lists the outermost file first so the
	// file nearest the working directory is read last and, where two files
	// disagree, reads as the more specific instruction.
	for i, j := 0, len(rules)-1; i < j; i, j = i+1, j-1 {
		rules[i], rules[j] = rules[j], rules[i]
	}
	return rules
}

// maxRuleFileChars caps each rules file in the prompt. A rules file is sent with
// every request, so an oversized one silently eats the budget compaction is
// trying to protect.
const maxRuleFileChars = 16000

// FormatRulesForPrompt renders each rules file in its own tagged block. The tag
// carries the source path and marks exactly where the file begins and ends, so
// headings inside a file cannot be mistaken for the prompt's own structure.
func FormatRulesForPrompt(rules []RuleFile) string {
	if len(rules) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("The following instruction files were found in the workspace. Follow them unless they conflict with the operating rules above or with the current mode; where they disagree with each other, the later (more specific) file wins.")
	for _, rule := range rules {
		content := strings.TrimSpace(rule.Content)
		if len(content) > maxRuleFileChars {
			content = content[:maxRuleFileChars] + fmt.Sprintf("\n[... truncated: the file is %d characters; read it for the rest]", len(rule.Content))
		}
		fmt.Fprintf(&sb, "\n\n<project_rules source=%q>\n%s\n</project_rules>", filepath.ToSlash(rule.Filename), content)
	}
	return sb.String()
}

// isRepoRoot reports whether dir contains .git, which is a directory in a normal
// checkout and a file in a worktree or submodule.
func isRepoRoot(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}
