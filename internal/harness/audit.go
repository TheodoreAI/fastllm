package harness

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// Every decision the monitor makes is appended to an audit log, one JSON
// object per line: what the model asked for, whether it happened, and which
// layer decided (the mode table, a capability, a protected path, a session
// grant, or the user). A transcript records what the model said; this records
// what the harness let it do, including what it refused.
//
// The log lives under ~/.fastllm/audit, outside every workspace, so the file
// tools cannot reach it. It is append-only by convention, not cryptographically
// sealed: a shell the user has allowed could still edit it.

// AuditRecord is one decision.
type AuditRecord struct {
	Time       time.Time      `json:"time"`
	AgentDepth int            `json:"agent_depth,omitempty"`
	Mode       PermissionMode `json:"mode"`
	Tool       string         `json:"tool"`
	Summary    string         `json:"summary"`
	ArgsSHA256 string         `json:"args_sha256"`
	Allowed    bool           `json:"allowed"`
	// Layer names what decided: mode, capability, commands-off, unknown-tool,
	// protected-git, protected-config, no-approver, or user.
	Layer string `json:"layer"`
	// Via says how a user decision was made: a grant ID, "approved once",
	// "approved for session (grant-N)", or "denied".
	Via string `json:"via,omitempty"`
}

// AuditLog appends records to one file. A nil *AuditLog records nothing.
type AuditLog struct {
	mu   sync.Mutex
	path string
}

// auditDir is where logs are written: FASTLLM_AUDIT_DIR, else
// ~/.fastllm/audit.
var auditDir = func() (string, error) {
	if dir := os.Getenv("FASTLLM_AUDIT_DIR"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".fastllm", "audit"), nil
}

var unsafeAuditName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// OpenAuditLog returns the log named name (a session ID, say). The file is
// created on the first record.
func OpenAuditLog(name string) *AuditLog {
	dir, err := auditDir()
	if err != nil {
		return nil
	}
	name = unsafeAuditName.ReplaceAllString(name, "_")
	if name == "" || name == "_" {
		name = "run-" + time.Now().UTC().Format("20060102-150405")
	}
	return &AuditLog{path: filepath.Join(dir, name+".jsonl")}
}

// auditNameFor names a terminal session's log after the session, so the log
// and the transcript can be read side by side.
func auditNameFor(session *InteractiveSession) string {
	if session == nil {
		return "interactive-" + time.Now().UTC().Format("20060102")
	}
	return session.ID
}

// Path is the log's file, for display.
func (a *AuditLog) Path() string {
	if a == nil {
		return ""
	}
	return a.path
}

// Record appends one decision. Failures are swallowed: auditing must never
// change what a run does, and a missing record shows as a gap, not a crash.
func (a *AuditLog) Record(rec AuditRecord) {
	if a == nil {
		return
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(a.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
}

// Recent returns up to n of the latest records, oldest first.
func (a *AuditLog) Recent(n int) ([]AuditRecord, error) {
	if a == nil {
		return nil, nil
	}
	f, err := os.Open(a.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var all []AuditRecord
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var rec AuditRecord
		if json.Unmarshal(scanner.Bytes(), &rec) == nil {
			all = append(all, rec)
		}
	}
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all, scanner.Err()
}

func argsDigest(args string) string {
	sum := sha256.Sum256([]byte(args))
	return hex.EncodeToString(sum[:])
}

// formatAuditTail renders /audit [n]: the session's latest decisions.
func (m *teaModel) formatAuditTail(parts []string) string {
	n := 20
	if len(parts) > 1 {
		if parsed, err := strconv.Atoi(parts[1]); err == nil && parsed > 0 {
			n = parsed
		}
	}
	log := OpenAuditLog(auditNameFor(m.activeSession))
	records, err := log.Recent(n)
	if err != nil {
		return styleDiffDel.Render("Cannot read the audit log: " + err.Error())
	}
	lines := []string{"", FormatKV("log", abbreviateHome(log.Path()), 6), ""}
	if len(records) == 0 {
		lines = append(lines, ColorGray("No decisions recorded in this session yet."))
	}
	for _, rec := range records {
		mark := ColorGreen(SymCheck)
		if !rec.Allowed {
			mark = ColorRed(SymCross)
		}
		who := rec.Layer
		if rec.Via != "" {
			who += " · " + rec.Via
		}
		head := fmt.Sprintf("%s %s %-12s %s", ColorGray(rec.Time.Local().Format("15:04:05")), mark,
			rec.Tool, ColorGray(truncateText(who, 34)))
		lines = append(lines, head, "    "+ColorGray(truncateText(sanitizeUntrusted(rec.Summary), 76)))
	}
	lines = append(lines, "")
	return FormatCard("Permission Audit", lines, 86)
}
