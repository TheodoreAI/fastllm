package harness

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestExecuteFollowUpFusesSuccessfulMutation(t *testing.T) {
	runner := &Runner{}
	result := runner.executeFollowUp(context.Background(), t.TempDir(), "Successfully edited file.", &FollowUpCommand{Command: "echo fused", TimeoutSeconds: 5}, time.Second, true, false)
	if !strings.Contains(result, "Successfully edited file") || !strings.Contains(result, "Follow-up verification") || !strings.Contains(result, "fused") {
		t.Fatalf("unexpected fused result: %q", result)
	}
}

func TestExecuteFollowUpSkipsAfterMutationFailure(t *testing.T) {
	runner := &Runner{}
	result := runner.executeFollowUp(context.Background(), t.TempDir(), "Error: mutation failed", &FollowUpCommand{Command: "echo should-not-run"}, time.Second, true, false)
	if !strings.Contains(result, "skipped") || strings.Contains(result, "Exit code") {
		t.Fatalf("unexpected result: %q", result)
	}
}
