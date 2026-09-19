package harness

import (
	"strings"
	"testing"
	"time"
)

func TestProcessManager(t *testing.T) {
	pm := NewProcessManager()

	cmdStr := "echo hello-async"
	proc, err := pm.Start(cmdStr, ".")
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if proc.ID == "" || proc.PID == 0 {
		t.Fatalf("invalid process record: %+v", proc)
	}

	// Wait briefly for completion
	time.Sleep(300 * time.Millisecond)

	_, out, err := pm.Status(proc.ID)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if !strings.Contains(out, "hello-async") {
		t.Errorf("expected output to contain 'hello-async', got %q", out)
	}

	table := pm.FormatProcessTable()
	if !strings.Contains(table, proc.ID) {
		t.Errorf("FormatProcessTable should include proc ID %s, got:\n%s", proc.ID, table)
	}
}
