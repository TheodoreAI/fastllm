package harness

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// pathProtection is how the monitor treats a write to a path, beyond what the
// mode table says about writes in general (I8).
type pathProtection int

const (
	protectNone pathProtection = iota
	// protectConfig covers files that shape every later session: fastllm's
	// own settings, the rule files injected into the prompt, and skills. A
	// write there always asks, even in edit and full, and a run that cannot
	// ask is refused.
	protectConfig
	// protectGit covers .git. Git runs commands named in its own files
	// (core.fsmonitor, hooks, diff and filter drivers) and the harness runs
	// git in the background, so a write there starts a process. No mode
	// allows it.
	protectGit
)

// writeProtection classifies the path argument of a write tool.
func writeProtection(req RunRequest, tool, args string) pathProtection {
	if toolClasses[tool] != classWrite {
		return protectNone
	}
	var parsed struct {
		Path string `json:"path"`
	}
	if json.Unmarshal([]byte(args), &parsed) != nil || strings.TrimSpace(parsed.Path) == "" {
		return protectNone
	}
	return classifyWritePath(req.WorkingDir, parsed.Path)
}

// classifyWritePath inspects the canonical path, so symlinks, "..", mixed
// case and Windows short names (GIT~1) cannot disguise a protected target.
func classifyWritePath(workingDir, requested string) pathProtection {
	target := requested
	if !filepath.IsAbs(target) {
		target = filepath.Join(workingDir, target)
	}
	parts := relativeParts(canonicalPath(workingDir), canonicalPath(filepath.Clean(target)))

	for _, part := range parts {
		if strings.EqualFold(part, ".git") {
			return protectGit
		}
	}
	for _, part := range parts {
		if strings.EqualFold(part, ".fastllm") {
			return protectConfig
		}
	}
	if len(parts) > 0 {
		base := parts[len(parts)-1]
		for _, rule := range candidateRuleFiles {
			if strings.EqualFold(base, filepath.Base(rule)) {
				return protectConfig
			}
		}
	}
	for _, source := range projectSkillSources {
		if containsSequence(parts, strings.Split(source.path, string(filepath.Separator))) {
			return protectConfig
		}
	}
	return protectNone
}

// canonicalPath resolves symlinks and short names on the longest existing
// ancestor of path and re-attaches the part that does not exist yet.
func canonicalPath(path string) string {
	if path == "" {
		return path
	}
	path = filepath.Clean(path)
	existing, rest := path, ""
	for {
		if _, err := os.Lstat(existing); err == nil || !errors.Is(err, fs.ErrNotExist) {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return path
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return path
	}
	return filepath.Join(resolved, rest)
}

// relativeParts splits target relative to root into components, or all of
// target's components when it is not under root.
func relativeParts(root, target string) []string {
	rel := target
	if root != "" {
		if r, err := filepath.Rel(root, target); err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			rel = r
		}
	}
	var parts []string
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}
	return parts
}

func containsSequence(parts, seq []string) bool {
	for i := 0; i+len(seq) <= len(parts); i++ {
		match := true
		for j := range seq {
			if !strings.EqualFold(parts[i+j], seq[j]) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
