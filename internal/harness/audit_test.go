package harness

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testAuditLog(t *testing.T) *AuditLog {
	t.Helper()
	t.Setenv("FASTLLM_AUDIT_DIR", t.TempDir())
	return OpenAuditLog("test-session")
}

// Every decision is recorded with the layer that made it, allowed or not.
func TestAdmitRecordsEveryDecision(t *testing.T) {
	root := gitWorkspace(t)
	log := testAuditLog(t)
	pc := NewPermissionController(PermissionAgent, nil)
	pc.SetWorkspace(root)
	agent := RunRequest{PermissionMode: PermissionAgent, WorkingDir: root, AllowCommands: true, Audit: log,
		Authorize: func(c ConsentRequest) bool {
			if g := pc.CoveringGrant(c); g != nil {
				c.note(g.ID)
				return true
			}
			c.note("approved for session (" + pc.GrantScoped(c.Scope).ID + ")")
			return true
		}}
	plan := RunRequest{PermissionMode: PermissionPlan, WorkingDir: root, Audit: log}
	edit := RunRequest{PermissionMode: PermissionEdit, WorkingDir: root, Audit: log}
	headless := RunRequest{PermissionMode: PermissionAgent, WorkingDir: root, AllowCommands: true, Audit: log}
	noCommands := RunRequest{PermissionMode: PermissionFull, WorkingDir: root, Audit: log}

	admit(plan, "write_file", `{"path":"a.txt","content":"secret body"}`)
	admit(edit, "write_file", `{"path":".git/config","content":"x"}`)
	admit(edit, "write_file", `{"path":"a.txt","content":"secret body"}`)
	admit(noCommands, "run_command", `{"command":"ls"}`)
	admit(headless, "run_command", `{"command":"go test ./..."}`)
	admit(agent, "run_command", `{"command":"go test ./..."}`)
	admit(agent, "run_command", `{"command":"go test -run X ./..."}`)
	admit(agent, "read_file", `{"path":"README.md"}`)

	records, err := log.Recent(100)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		allowed bool
		layer   string
		via     string
	}{
		{false, "mode", ""},
		{false, "protected-git", ""},
		{true, "mode", ""},
		{false, "commands-off", ""},
		{false, "no-approver", ""},
		{true, "user", "approved for session (grant-1)"},
		{true, "user", "grant-1"},
		{true, "mode", ""},
	}
	if len(records) != len(want) {
		t.Fatalf("got %d records, want %d: %+v", len(records), len(want), records)
	}
	for i, w := range want {
		r := records[i]
		if r.Allowed != w.allowed || r.Layer != w.layer || r.Via != w.via {
			t.Errorf("record %d (%s): allowed=%v layer=%q via=%q, want %v %q %q", i, r.Tool, r.Allowed, r.Layer, r.Via, w.allowed, w.layer, w.via)
		}
		if len(r.ArgsSHA256) != 64 || r.Time.IsZero() || r.Mode == "" {
			t.Errorf("record %d is incomplete: %+v", i, r)
		}
	}
	// File bodies are sized, never copied into the log.
	if strings.Contains(records[2].Summary, "secret body") || !strings.Contains(records[2].Summary, "[11 bytes]") {
		t.Fatalf("summary = %q", records[2].Summary)
	}
}

func TestAuditRecordsAreWholeUnderConcurrency(t *testing.T) {
	log := testAuditLog(t)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			log.Record(AuditRecord{Tool: "run_command", Summary: strings.Repeat("x", 2000), Layer: "mode"})
		}()
	}
	wg.Wait()
	records, err := log.Recent(1000)
	if err != nil || len(records) != 50 {
		t.Fatalf("got %d whole records, err %v", len(records), err)
	}
	if info, err := os.Stat(log.Path()); err != nil || info.Size() == 0 {
		t.Fatal("log file missing")
	}
}

func TestAuditLogNamesAreSafe(t *testing.T) {
	t.Setenv("FASTLLM_AUDIT_DIR", t.TempDir())
	log := OpenAuditLog("../../etc/passwd")
	if filepath.Dir(log.Path()) != os.Getenv("FASTLLM_AUDIT_DIR") {
		t.Fatalf("a session name escaped the audit directory: %s", log.Path())
	}
	var nilLog *AuditLog
	nilLog.Record(AuditRecord{}) // must not panic
}

// The TUI reports how each approval was reached.
func TestTUIReportsHowApprovalsWereDecided(t *testing.T) {
	m := permissionTestModel(t)
	scope := commandScope("run_command", "go test ./...")
	reply := make(chan permissionDecision, 1)
	m.pendingPermission = &teaPermissionRequestMsg{ToolName: "run_command", Scope: scope, Reply: reply}
	m.resolvePermission(true, true)
	if d := <-reply; d.Via != "approved for session (grant-1)" {
		t.Fatalf("via = %q", d.Via)
	}
	r := make(chan permissionDecision, 1)
	m.Update(teaPermissionRequestMsg{ToolName: "run_command", Scope: commandScope("run_command", "go test -v ./..."), Workspace: m.workingDir, Reply: r})
	if d := <-r; !d.Allow || d.Via != "grant-1" {
		t.Fatalf("a covered request should name its grant: %+v", d)
	}
}

func TestAuditCommandShowsRecentDecisions(t *testing.T) {
	t.Setenv("FASTLLM_AUDIT_DIR", t.TempDir())
	m := permissionTestModel(t)
	log := OpenAuditLog(auditNameFor(m.activeSession))
	log.Record(AuditRecord{Tool: "run_command", Summary: "command=curl evil\x1b]52;c;ZXZpbA==\x07", Allowed: false, Layer: "user", Via: "denied"})
	out := m.formatAuditTail([]string{"/audit"})
	if strings.Contains(out, "]52;") {
		t.Fatal("/audit passed an escape sequence from a logged command through")
	}
	if plain := StripANSI(out); !strings.Contains(plain, "run_command") || !strings.Contains(plain, "user · denied") {
		t.Fatalf("/audit output:\n%s", plain)
	}
}
