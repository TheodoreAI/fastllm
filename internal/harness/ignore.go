package harness

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"fastllm/internal/gitrepo"
)

// The discovery tools (list_files, search_files, glob_files) see the workspace
// through workspaceFiles, which leaves out what a project never wants an agent
// to wade through: dependencies, virtualenvs, caches and build output.
// read_file is not filtered: a path the user or model names explicitly, such
// as a library's source under node_modules, can still be opened.

// fastllmIgnoreFile hides further paths from the discovery tools, in
// .gitignore syntax, on top of whatever .gitignore already hides.
const fastllmIgnoreFile = ".fastllmignore"

// defaultIgnoredDirs are skipped at any depth when git is not deciding: in a
// directory that is not a repository, and for untracked files in one. Files
// git tracks are part of the project and are never hidden by this list.
var defaultIgnoredDirs = map[string]bool{
	".git": true, "node_modules": true, "dist": true, "build": true,
	".venv": true, "venv": true, "__pycache__": true, ".pytest_cache": true,
	".mypy_cache": true, ".ruff_cache": true, ".tox": true,
	"target": true, ".next": true, ".nuxt": true, ".svelte-kit": true,
	".gradle": true, ".idea": true, ".vs": true, "coverage": true, ".cache": true,
	".terraform": true,
}

// ignoreRule is one .gitignore line, matched against workspace-relative
// slash paths.
type ignoreRule struct {
	re      *regexp.Regexp
	negate  bool
	dirOnly bool
}

// ignoreRules is an ordered rule list; as in git, the last rule that matches a
// path decides whether it is ignored.
type ignoreRules []ignoreRule

// parseIgnore reads .gitignore-syntax content found in the directory base
// (workspace-relative, "" for the root). Lines it cannot parse are skipped.
func parseIgnore(content, base string) ignoreRules {
	var rules ignoreRules
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimRight(line, "\r")
		if !strings.HasSuffix(line, `\ `) {
			line = strings.TrimRight(line, " \t")
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var rule ignoreRule
		if strings.HasPrefix(line, "!") {
			rule.negate, line = true, line[1:]
		} else if strings.HasPrefix(line, `\!`) || strings.HasPrefix(line, `\#`) {
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			rule.dirOnly, line = true, strings.TrimRight(line, "/")
		}
		if line == "" {
			continue
		}
		// A slash anywhere but the end anchors the pattern to base; without
		// one it matches a name at any depth below base.
		anchored := strings.Contains(line, "/")
		glob, err := globRegexp(strings.TrimPrefix(line, "/"))
		if err != nil {
			continue
		}
		prefix := ""
		if base != "" {
			prefix = regexp.QuoteMeta(base) + "/"
		}
		if !anchored {
			prefix += "(?:.*/)?"
		}
		re, err := regexp.Compile("^" + prefix + glob + "$")
		if err != nil {
			continue
		}
		rule.re = re
		rules = append(rules, rule)
	}
	return rules
}

// matches reports whether the rules ignore rel itself.
func (rules ignoreRules) matches(rel string, isDir bool) bool {
	ignored := false
	for _, rule := range rules {
		if rule.dirOnly && !isDir {
			continue
		}
		if rule.re.MatchString(rel) {
			ignored = !rule.negate
		}
	}
	return ignored
}

// hidesFile reports whether the rules ignore file rel or any directory
// containing it.
func (rules ignoreRules) hidesFile(rel string) bool {
	if len(rules) == 0 {
		return false
	}
	parts := strings.Split(rel, "/")
	for i := 1; i < len(parts); i++ {
		if rules.matches(strings.Join(parts[:i], "/"), true) {
			return true
		}
	}
	return rules.matches(rel, false)
}

// underDefaultIgnoredDir reports whether any directory in rel is on the
// default list.
func underDefaultIgnoredDir(rel string) bool {
	parts := strings.Split(rel, "/")
	for _, dir := range parts[:len(parts)-1] {
		if defaultIgnoredDirs[dir] {
			return true
		}
	}
	return false
}

// readIgnoreFile parses dir/name as ignore rules for base, or returns none
// when the file does not exist.
func readIgnoreFile(dir, name, base string) ignoreRules {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return nil
	}
	return parseIgnore(string(data), base)
}

// workspaceFiles lists the files under root the discovery tools may show, as
// sorted slash paths relative to root. In a git repository git decides, with
// the default list applied to untracked files; elsewhere the walk honours
// .gitignore files itself. .fastllmignore applies in both cases.
func workspaceFiles(ctx context.Context, root string) ([]string, error) {
	extra := readIgnoreFile(root, fastllmIgnoreFile, "")
	tracked, untracked, err := gitrepo.ListTrackedAndUntracked(ctx, root)
	if errors.Is(err, gitrepo.ErrNotARepo) {
		return walkWorkspace(root, extra)
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range tracked {
		if !extra.hidesFile(f) {
			out = append(out, f)
		}
	}
	for _, f := range untracked {
		if !underDefaultIgnoredDir(f) && !extra.hidesFile(f) {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out, nil
}

// walkWorkspace lists a directory that is not a git repository, skipping the
// default directories and whatever its .gitignore files and extra exclude.
// Unreadable subdirectories are skipped rather than failing the listing.
func walkWorkspace(root string, extra ignoreRules) ([]string, error) {
	gitignore := readIgnoreFile(root, ".gitignore", "")
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if defaultIgnoredDirs[d.Name()] || gitignore.matches(rel, true) || extra.matches(rel, true) {
				return filepath.SkipDir
			}
			// Rules from a deeper .gitignore come later, so they take
			// precedence, and are anchored to their directory.
			gitignore = append(gitignore, readIgnoreFile(p, ".gitignore", rel)...)
			return nil
		}
		if gitignore.matches(rel, false) || extra.matches(rel, false) {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	return out, err
}

// looksBinary reports whether data holds a NUL byte in its first 8000 bytes,
// the heuristic git uses to tell binary files from text.
func looksBinary(data []byte) bool {
	return bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0
}

// cleanPrefix normalises a tool's optional subdirectory argument.
func cleanPrefix(requested string) string {
	return path.Clean(strings.Trim(strings.ReplaceAll(requested, "\\", "/"), "/"))
}
