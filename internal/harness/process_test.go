package harness

import (
	"runtime"
	"strings"
	"sync"
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

func TestProcessManagerReturnsImmutableSnapshotsDuringExit(t *testing.T) {
	pm := NewProcessManager()
	command := "sleep 0.2"
	if runtime.GOOS == "windows" {
		command = "ping -n 2 127.0.0.1 >nul"
	}
	started, err := pm.Start(command, ".")
	if err != nil {
		t.Fatal(err)
	}
	if started.Exited {
		t.Fatal("new process snapshot is already exited")
	}

	var readers sync.WaitGroup
	for i := 0; i < 4; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				snapshot, _, statusErr := pm.Status(started.ID)
				if statusErr != nil || snapshot.Exited {
					return
				}
				time.Sleep(time.Millisecond)
			}
		}()
	}
	readers.Wait()
	if started.Exited {
		t.Fatal("previously returned snapshot was mutated after Start returned")
	}
	finished, _, err := pm.Status(started.ID)
	if err != nil || !finished.Exited {
		t.Fatalf("final status = %+v, err=%v", finished, err)
	}
}

func TestSanitizedEnvironmentScrubsSecrets(t *testing.T) {
	t.Setenv("TEST_FASTLLM_API_KEY", "super-secret-key-123")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-secret-xyz")
	t.Setenv("GITHUB_TOKEN", "ghp_secrettoken")
	t.Setenv("DB_PASSWORD", "dbpass")
	t.Setenv("SAFE_COMPILER_FLAG", "-O3")

	env := SanitizedEnvironment()
	envMap := make(map[string]string)
	for _, entry := range env {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	for _, secretKey := range []string{"TEST_FASTLLM_API_KEY", "AWS_SECRET_ACCESS_KEY", "GITHUB_TOKEN", "DB_PASSWORD"} {
		if _, found := envMap[secretKey]; found {
			t.Fatalf("sanitized environment leaked secret variable %q", secretKey)
		}
	}

	if val, found := envMap["SAFE_COMPILER_FLAG"]; !found || val != "-O3" {
		t.Fatalf("sanitized environment dropped harmless variable SAFE_COMPILER_FLAG: %v", val)
	}
}

func TestProcessManagerLogsAndKillAll(t *testing.T) {
	pm := NewProcessManager()
	defer pm.KillAll()

	cmd := "sleep 10"
	if runtime.GOOS == "windows" {
		cmd = "ping -n 11 127.0.0.1 >nul"
	}

	p1, err := pm.Start(cmd, ".")
	if err != nil {
		t.Fatalf("Start p1 failed: %v", err)
	}
	if p1.ID != "proc-1" {
		t.Errorf("expected p1 ID 'proc-1', got %q", p1.ID)
	}

	p2, err := pm.Start(cmd, ".")
	if err != nil {
		t.Fatalf("Start p2 failed: %v", err)
	}
	if p2.ID != "proc-2" {
		t.Errorf("expected p2 ID 'proc-2', got %q", p2.ID)
	}

	if pm.ActiveCount() != 2 {
		t.Errorf("expected ActiveCount 2, got %d", pm.ActiveCount())
	}

	// Test find by numeric index "1"
	bp, _, err := pm.Status("1")
	if err != nil || bp.ID != "proc-1" {
		t.Errorf("expected to find proc-1 by '1', got %+v, err: %v", bp, err)
	}

	// Test Logs method
	bpLogs, _, err := pm.Logs("proc-1", 10)
	if err != nil || bpLogs.ID != "proc-1" {
		t.Errorf("Logs failed: %v, bp: %+v", err, bpLogs)
	}

	// Test Kill single by ID
	if err := pm.Kill("1"); err != nil {
		t.Errorf("Kill '1' failed: %v", err)
	}

	// Test Kill all
	if err := pm.Kill("all"); err != nil {
		t.Errorf("Kill 'all' failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	if pm.ActiveCount() != 0 {
		t.Errorf("expected 0 active processes after KillAll, got %d", pm.ActiveCount())
	}
}

