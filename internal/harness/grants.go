package harness

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// ConsentRequest is what the monitor asks the user to approve: the effect in
// full (I7) and the scope a "for this session" answer would grant.
type ConsentRequest struct {
	Tool    string // the label asked under, e.g. "write_file (fastllm configuration)"
	Summary string
	Scope   GrantScope
	// Via, when set, receives how the answer was reached (a grant ID,
	// "approved once", ...) for the audit log.
	Via *string
	// Always marks an ask that happens in every mode (configuration writes,
	// secret reads, web requests after one), not only in agent mode.
	Always bool
}

// note records how a consent was answered, if the asker wants to know.
func (c ConsentRequest) note(how string) {
	if c.Via != nil {
		*c.Via = how
	}
}

// A session grant covers the effect that was approved and things like it,
// never the whole tool: approving `go test ./...` must not approve
// `curl … | sh`, and approving an edit in internal/ must not approve one in
// .github/ (I9).
type ScopeKind int

const (
	scopeTool          ScopeKind = iota // every call of the tool
	scopeCommand                        // exactly this command
	scopeCommandPrefix                  // commands whose words begin with Pattern
	scopeFile                           // exactly this workspace file
	scopeTree                           // files under the Pattern directory
	scopeExact                          // exactly this request, e.g. one URL
)

// GrantScope describes one call (Subject) and what a session grant made from
// it would cover (Kind and Pattern).
type GrantScope struct {
	Tool    string    `json:"tool"`
	Kind    ScopeKind `json:"kind"`
	Pattern string    `json:"pattern,omitempty"`
	Subject string    `json:"-"`
}

// Describe names what the grant covers, for the prompt and /permissions.
func (s GrantScope) Describe() string {
	switch s.Kind {
	case scopeCommand:
		return "the command `" + s.Pattern + "`"
	case scopeCommandPrefix:
		return "commands starting with `" + s.Pattern + "`"
	case scopeFile:
		return s.Tool + " on " + s.Pattern
	case scopeTree:
		return s.Tool + " under " + s.Pattern + "/"
	case scopeExact:
		return "exactly this request"
	default:
		return "every " + s.Tool + " call"
	}
}

// Covers reports whether a grant with scope s permits the call described by
// req, which must be for the same tool label.
func (s GrantScope) Covers(req GrantScope) bool {
	if s.Tool != req.Tool {
		return false
	}
	switch s.Kind {
	case scopeTool:
		return true
	case scopeCommand:
		return req.Subject == s.Pattern
	case scopeCommandPrefix:
		return !hasShellSyntax(req.Subject) &&
			(req.Subject == s.Pattern || strings.HasPrefix(req.Subject, s.Pattern+" "))
	case scopeFile:
		return samePathText(req.Subject, s.Pattern)
	case scopeExact:
		return req.Subject == s.Pattern
	case scopeTree:
		return req.Subject != "" && !strings.HasPrefix(req.Subject, "../") &&
			(samePathText(req.Subject, s.Pattern) || hasPathPrefix(req.Subject, s.Pattern+"/"))
	}
	return false
}

// scopeFor derives the scope of one call asked for under label.
func scopeFor(req RunRequest, label, tool, args string) GrantScope {
	var parsed struct {
		Command string `json:"command"`
		Path    string `json:"path"`
	}
	_ = json.Unmarshal([]byte(args), &parsed)
	switch toolClasses[tool] {
	case classWrite:
		return pathScope(label, req.WorkingDir, parsed.Path)
	}
	if tool == "run_command" {
		return commandScope(label, parsed.Command)
	}
	return GrantScope{Tool: label, Kind: scopeTool}
}

// commandScope grants a command prefix only for the common "program
// subcommand" shape, and only when the command is plain words. Anything with
// shell syntax, or run through an interpreter or wrapper that would execute
// arbitrary code, is granted exactly as written.
func commandScope(label, command string) GrantScope {
	// Only spaces and tabs collapse. A newline separates two commands to a
	// shell, so it must survive for hasShellSyntax to see it.
	normalized := strings.Trim(blankRun.ReplaceAllString(command, " "), " \t")
	scope := GrantScope{Tool: label, Kind: scopeCommand, Pattern: normalized, Subject: normalized}
	words := strings.Fields(normalized)
	if len(words) < 2 || hasShellSyntax(normalized) || !subcommandWord.MatchString(words[1]) {
		return scope
	}
	program := strings.ToLower(strings.TrimSuffix(filepath.Base(words[0]), ".exe"))
	if codeRunners[program] {
		return scope
	}
	scope.Kind, scope.Pattern = scopeCommandPrefix, words[0]+" "+words[1]
	return scope
}

var (
	subcommandWord = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	blankRun       = regexp.MustCompile(`[ \t]+`)
)

// codeRunners take their next word as code, a script, or another command, so
// a prefix of theirs would approve arbitrary execution.
var codeRunners = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true, "cmd": true,
	"pwsh": true, "powershell": true, "python": true, "python3": true, "py": true,
	"node": true, "deno": true, "bun": true, "ruby": true, "perl": true, "php": true,
	"lua": true, "osascript": true, "sudo": true, "doas": true, "env": true,
	"xargs": true, "eval": true, "exec": true, "nohup": true, "time": true,
	"watch": true, "timeout": true, "nice": true, "ssh": true, "docker": true,
	"kubectl": true, "npx": true, "pnpx": true, "uvx": true,
}

// hasShellSyntax reports characters that chain, substitute, or redirect, so a
// command containing them is more than its first words suggest.
func hasShellSyntax(command string) bool {
	return strings.ContainsAny(command, ";&|`$<>(){}\n\r")
}

// pathScope grants the directory a write is in, and everything below it. A
// file at the workspace root is granted alone: granting its directory would
// grant the whole workspace.
func pathScope(label, workingDir, requested string) GrantScope {
	target := requested
	if !filepath.IsAbs(target) {
		target = filepath.Join(workingDir, target)
	}
	root := canonicalPath(workingDir)
	parts := relativeParts(root, canonicalPath(filepath.Clean(target)))
	rel := strings.Join(parts, "/")
	if rel == "" {
		// The workspace root itself. Judge it as ".", never as the raw
		// text, which a prefix check could match ("A/.." against "a/").
		return GrantScope{Tool: label, Kind: scopeFile, Pattern: ".", Subject: "."}
	}
	if !insideRoot(root, target) {
		// Outside the workspace the file layer refuses anyway; never widen.
		return GrantScope{Tool: label, Kind: scopeFile, Pattern: "../" + rel, Subject: "../" + rel}
	}
	scope := GrantScope{Tool: label, Kind: scopeFile, Pattern: rel, Subject: rel}
	if len(parts) > 1 {
		scope.Kind, scope.Pattern = scopeTree, strings.Join(parts[:len(parts)-1], "/")
	}
	return scope
}

func insideRoot(root, target string) bool {
	rel, err := filepath.Rel(root, canonicalPath(filepath.Clean(target)))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func samePathText(a, b string) bool {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func hasPathPrefix(path, prefix string) bool {
	if len(path) < len(prefix) {
		return false
	}
	return samePathText(path[:len(prefix)], prefix)
}
