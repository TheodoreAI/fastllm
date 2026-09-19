package harness

import (
	"strings"
	"testing"
	"time"
)

func TestMetricsCalculation(t *testing.T) {
	tm := ComputeTurnMetrics(1, "claude-3-5-sonnet", 1000, 500, 2*time.Second)

	if tm.TotalTokens != 1500 {
		t.Errorf("expected 1500 total tokens, got %d", tm.TotalTokens)
	}
	if tm.TokensPerSecond != 250 {
		t.Errorf("expected 250 tok/s, got %f", tm.TokensPerSecond)
	}
	if tm.EstimatedCost <= 0 {
		t.Errorf("expected positive cost for claude-3-5-sonnet, got %f", tm.EstimatedCost)
	}

	summary := tm.FormatTurnSummary()
	if !strings.Contains(summary, "tok/s") || !strings.Contains(summary, "cost:") {
		t.Errorf("invalid format summary: %s", summary)
	}

	localTm := ComputeTurnMetrics(1, "muse-glimmer-30b", 1000, 500, 2*time.Second)
	if localTm.EstimatedCost != 0.0 {
		t.Errorf("expected local model to be $0.00, got %f", localTm.EstimatedCost)
	}

	var sm SessionMetrics
	sm.Add(tm)
	sm.Add(localTm)
	if sm.TotalTurns != 2 {
		t.Errorf("expected 2 turns, got %d", sm.TotalTurns)
	}
	if sm.TotalTokens != 3000 {
		t.Errorf("expected 3000 total tokens, got %d", sm.TotalTokens)
	}
}
