//go:build !windows

package execution

import (
	"context"
	"testing"
	"time"
)

func TestGracefulTerminationSendsSIGTERMFirst(t *testing.T) {
	_, s := modelScope(t, Options{})
	// A shell command that catches SIGTERM, prints confirmation, and exits cleanly.
	cmd := "trap 'echo term_received; exit 0' TERM; while true; do sleep 0.05; done"
	p, err := s.Start(context.Background(), Command{Shell: cmd})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Give the shell a moment to set up the trap handler.
	time.Sleep(100 * time.Millisecond)

	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	res, err := p.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}

	out, err := p.ReadOutput(context.Background(), 0)
	if err != nil {
		t.Fatalf("ReadOutput: %v", err)
	}
	var outputText string
	for _, chunk := range out.Chunks {
		outputText += chunk.Data
	}

	if !res.Exited {
		t.Fatal("process was not marked exited")
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0 (clean shutdown on SIGTERM)", res.ExitCode)
	}
	if res.Reason != "canceled" {
		t.Fatalf("reason = %q, want %q", res.Reason, "canceled")
	}
}

func TestGracefulTerminationEscalatesToSIGKILLWhenSIGTERMIgnored(t *testing.T) {
	_, s := modelScope(t, Options{})
	// A shell command that ignores SIGTERM: trap '' TERM
	cmd := "trap '' TERM; while true; do sleep 0.05; done"
	p, err := s.Start(context.Background(), Command{Shell: cmd})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Give the shell a moment to set up the ignore trap.
	time.Sleep(100 * time.Millisecond)

	startStop := time.Now()
	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	stopDuration := time.Since(startStop)

	// Since SIGTERM was ignored, it should have waited ~1 second before escalating to SIGKILL.
	if stopDuration < 800*time.Millisecond {
		t.Fatalf("Stop took %v, want at least ~1s for grace period before SIGKILL", stopDuration)
	}

	res, err := p.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}

	if !res.Exited {
		t.Fatal("process was not marked exited after SIGKILL escalation")
	}
}
