package harness

import (
	"strings"
	"testing"
	"time"
)

func TestMetricsCalculation(t *testing.T) {
	tm := ComputeTurnMetrics(1, "claude-3-5-sonnet", 1000, 500, 2*time.Second, true)

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

	localTm := ComputeTurnMetrics(1, "example-model-30b", 1000, 500, 2*time.Second, false)
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

// Lookup used to be strings.Contains over a map, and Go randomizes map
// iteration: "gpt-4o-mini" contains both "gpt-4o-mini" and "gpt-4o", so the
// same binary priced the same request at $0.15 or $2.50 depending on which key
// came up first. Repeat enough to catch a reintroduced nondeterministic match.
func TestModelPricingIsDeterministicForOverlappingNames(t *testing.T) {
	overlapping := []string{"gpt-4o-mini", "gpt-5-mini", "gpt-5.5-pro", "o3-pro", "o3-mini"}
	for _, model := range overlapping {
		first, ok := CalculateCost(model, 1_000_000, 0)
		if !ok {
			t.Fatalf("%s has no price", model)
		}
		for i := 0; i < 200; i++ {
			again, _ := CalculateCost(model, 1_000_000, 0)
			if again != first {
				t.Fatalf("%s priced at $%.4f then $%.4f -- lookup is nondeterministic", model, first, again)
			}
		}
	}
}

// The specific variant must win over its shorter relative, not merely win
// consistently.
func TestModelPricingPrefersTheMoreSpecificVariant(t *testing.T) {
	cases := []struct {
		model string
		want  float64
	}{
		{"gpt-4o-mini", 0.15},
		{"gpt-4o", 2.50},
		{"gpt-5-mini", 0.25},
		{"gpt-5", 1.25},
		{"gpt-5.5-pro", 30.00},
		{"gpt-5.5", 5.00},
		{"o3-mini", 1.10},
		{"o3", 2.00},
	}
	for _, tc := range cases {
		got, ok := CalculateCost(tc.model, 1_000_000, 0)
		if !ok {
			t.Fatalf("%s has no price", tc.model)
		}
		if got != tc.want {
			t.Fatalf("%s = $%.4f per 1M prompt tokens; want $%.4f", tc.model, got, tc.want)
		}
	}
}

// A router prefix and a dated snapshot suffix are fastllm/provider decoration,
// not part of the model's identity for pricing.
func TestModelPricingNormalizesPrefixAndDateSuffix(t *testing.T) {
	for _, model := range []string{"gemini-3.7-flash", "gemini:gemini-3.7-flash", "GEMINI-3.7-FLASH"} {
		got, ok := CalculateCost(model, 1_000_000, 0)
		if !ok || got != 0.75 {
			t.Fatalf("%s = $%.4f (priced=%v); want $0.7500", model, got, ok)
		}
	}
	if got, ok := CalculateCost("claude-haiku-4-5-20251001", 1_000_000, 0); !ok || got != 1.00 {
		t.Fatalf("dated snapshot = $%.4f (priced=%v); want $1.0000", got, ok)
	}
}

// An unpriced model on a paid endpoint must not read as free.
func TestUnpricedPaidModelIsNotReportedAsFree(t *testing.T) {
	paid := ComputeTurnMetrics(1, "some-new-cloud-model", 1_000_000, 0, time.Second, true)
	if paid.CostKnown {
		t.Fatal("unknown model on a billable endpoint was reported as a known cost")
	}
	if strings.Contains(paid.FormatTurnSummary(), "$0.00") {
		t.Fatalf("unpriced paid model rendered as $0.00: %s", paid.FormatTurnSummary())
	}

	local := ComputeTurnMetrics(1, "muse-glimmer", 1_000_000, 0, time.Second, false)
	if !local.CostKnown || local.EstimatedCost != 0 {
		t.Fatalf("self-hosted model should be a known $0.00: %+v", local)
	}

	// One unpriced turn makes the session total an understatement, not a figure.
	var sm SessionMetrics
	sm.Add(local)
	if !sm.CostComplete {
		t.Fatal("all-local session should have a complete cost")
	}
	sm.Add(paid)
	if sm.CostComplete {
		t.Fatal("session containing an unpriced turn still claims a complete cost")
	}
}

func TestBillableEndpointDistinguishesLocalFromPublic(t *testing.T) {
	local := []string{
		"http://localhost:11434/v1", "http://127.0.0.1:8010/v1",
		"http://192.168.1.50:8000/v1", "http://10.0.0.4/v1", "http://[::1]:8080/v1", "",
	}
	for _, u := range local {
		if billableEndpoint(u) {
			t.Fatalf("%q treated as a paid endpoint", u)
		}
	}
	public := []string{
		"https://api.anthropic.com/v1", "https://api.openai.com/v1",
		"https://generativelanguage.googleapis.com/v1beta/openai",
	}
	for _, u := range public {
		if !billableEndpoint(u) {
			t.Fatalf("%q treated as free", u)
		}
	}
}
