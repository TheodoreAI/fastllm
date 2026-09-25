package harness

import (
	"testing"

	"fastllm/internal/config"
)

func TestResolveContextWindowPrefersEndpointSetting(t *testing.T) {
	settings := &config.Settings{Models: []config.ModelEndpoint{
		// A self-hosted server started with a reduced --max-model-len must be able
		// to override a table entry that would otherwise overestimate its window.
		{ID: "gpt-4o", ContextWindow: 8192},
	}}
	if got := ResolveContextWindow(settings, "gpt-4o"); got != 8192 {
		t.Fatalf("explicit context_window = %d, want 8192", got)
	}
}

func TestResolveContextWindowFallsBackToTableThenDefault(t *testing.T) {
	settings := &config.Settings{Models: []config.ModelEndpoint{{ID: "gpt-4o"}}}
	if got := ResolveContextWindow(settings, "gpt-4o"); got != 128000 {
		t.Fatalf("table lookup = %d, want 128000", got)
	}
	if got := ResolveContextWindow(settings, "muse-glimmer"); got != DefaultContextWindowTokens {
		t.Fatalf("unknown model = %d, want the default %d", got, DefaultContextWindowTokens)
	}
	if got := ResolveContextWindow(nil, "claude-3-5-sonnet"); got != 200000 {
		t.Fatalf("nil settings = %d, want 200000", got)
	}
}

// Map iteration order is random, so a model matching several keys must resolve
// deterministically by longest key rather than by whichever key came up first.
func TestLookupKnownContextWindowPrefersLongestMatch(t *testing.T) {
	cases := map[string]int{
		"gpt-4o-mini":        128000,
		"gpt-4o-2024-08-06":  128000,
		"gpt-4":              8192,
		"GPT-4O":             128000,
		"claude-3-5-sonnet":  200000,
		"llama3.1:8b":        131072,
		"llama3":             8192,
		"qwen2.5-coder:32b":  32768,
		"nothing-matches-me": 0,
		"":                   0,
	}
	for id, want := range cases {
		for i := 0; i < 8; i++ {
			if got := lookupKnownContextWindow(id); got != want {
				t.Fatalf("lookup(%q) = %d, want %d", id, got, want)
			}
		}
	}
}

func TestContextBudgetReservesHeadroom(t *testing.T) {
	// The window less a reply reserve, converted at 3.3 characters per token.
	if got, want := contextBudgetChars(128000), (128000-8192)*33/10; got != want {
		t.Fatalf("budget for 128k window = %d, want %d", got, want)
	}
	// Measured on Ornith 1.5 (16k): the 12 tool schemas were 5839 JSON
	// characters and 1732 tokens, and dense content ran 3.3 to 3.4 characters
	// per token. A transcript filling the budget left after those schemas, at
	// the dense end, plus the schemas and the reply reserve, must fit.
	const window, schemaChars, schemaTokens = 16384, 5839, 1732
	transcriptTokens := float64(contextBudgetChars(window)-schemaChars) / 3.3
	if used := transcriptTokens + schemaTokens + float64(replyReserveTokens(window)); used > window {
		t.Fatalf("a full 16k request needs %.0f tokens, more than the %d window", used, window)
	}
	// A tiny or unset window must not produce a budget that compacts every turn.
	if got := contextBudgetChars(0); got < 8000 {
		t.Fatalf("zero window = %d, want at least the 8000 floor", got)
	}
	if got := contextBudgetChars(100); got != 8000 {
		t.Fatalf("tiny window = %d, want the 8000 floor", got)
	}
}

// A 200k model must not be compacted as aggressively as an 8k one -- the whole
// point of sizing the budget from the real window.
func TestCompactionConfigForModelScalesWithWindow(t *testing.T) {
	settings := &config.Settings{Models: []config.ModelEndpoint{
		{ID: "big", ContextWindow: 200000},
		{ID: "small", ContextWindow: 8192},
	}}
	big := CompactionConfigForModel(settings, "big")
	small := CompactionConfigForModel(settings, "small")
	if big.MaxTotalChars <= small.MaxTotalChars {
		t.Fatalf("200k budget %d did not exceed 8k budget %d", big.MaxTotalChars, small.MaxTotalChars)
	}
	// Unrelated knobs must survive the resize.
	if big.KeepRecentMessages != DefaultCompactionConfig().KeepRecentMessages {
		t.Fatalf("KeepRecentMessages = %d, want the default", big.KeepRecentMessages)
	}
	if big.MaxToolOutputChars != DefaultCompactionConfig().MaxToolOutputChars {
		t.Fatalf("MaxToolOutputChars = %d, want the default", big.MaxToolOutputChars)
	}
}

// The old fixed 60k budget compacted a large-window model far too early; the
// window-derived budget must be meaningfully larger for such a model.
func TestWindowBudgetExceedsLegacyFixedBudget(t *testing.T) {
	got := CompactionConfigForModel(nil, "claude-3-5-sonnet").MaxTotalChars
	if legacy := 60000; got <= legacy {
		t.Fatalf("200k-model budget = %d, want more than the legacy %d", got, legacy)
	}
}
