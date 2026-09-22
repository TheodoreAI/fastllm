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
