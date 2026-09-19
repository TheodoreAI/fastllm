package llm

import (
	"strings"
	"testing"
)

func TestCanonicalModelAliasCollapsesOntoConfiguredID(t *testing.T) {
	tests := []struct {
		reported   string
		configured string
		expected   string
	}{
		// A server advertising several --served-model-name aliases for one model.
		{"vendor-models/Example-Model-30B", "example-model", "example-model"},
		{"example-model", "example-model", "example-model"},
		{"example-model-30b", "example-model", "example-model"},
		{"EXAMPLE_MODEL", "example-model", "example-model"},
		// A genuinely different model on the same endpoint keeps its own name.
		{"gemma-4-31b", "example-model", "gemma-4-31b"},
		// Without a configured id there is no way to know what is an alias.
		{"vendor-models/Example-Model-30B", "", "vendor-models/Example-Model-30B"},
	}

	for _, tt := range tests {
		if got := CanonicalModelAlias(tt.reported, tt.configured); got != tt.expected {
			t.Errorf("CanonicalModelAlias(%q, %q) = %q; want %q", tt.reported, tt.configured, got, tt.expected)
		}
	}
}

func TestHasChannelMarkersDetectsFormatFromContent(t *testing.T) {
	if !HasChannelMarkers("<|start|>assistant to=self<|message|>thinking") {
		t.Error("reasoning channel not detected")
	}
	if !HasChannelMarkers("to=user<|message|>hello") {
		t.Error("user channel not detected")
	}
	if HasChannelMarkers("a perfectly ordinary completion") {
		t.Error("plain content must not be treated as channel-routed")
	}
}

func TestSplitChannelContentDirectUser(t *testing.T) {
	// The exact user report:
	raw := "to=userI'm an open source large language model trained by Meta Superintelligence Labs. Nice to meet you!"
	content, reasoning := SplitChannelContent(raw)
	expectedContent := "I'm an open source large language model trained by Meta Superintelligence Labs. Nice to meet you!"
	if content != expectedContent {
		t.Errorf("SplitChannelContent content = %q; want %q", content, expectedContent)
	}
	if reasoning != "" {
		t.Errorf("SplitChannelContent reasoning = %q; want empty", reasoning)
	}
}

func TestSplitChannelContentWithReasoning(t *testing.T) {
	raw := " to=selfThinking through the problem...\nWe should return 4.\n\nProceed.assistant to=userThe answer is 4."
	content, reasoning := SplitChannelContent(raw)
	expectedContent := "The answer is 4."
	expectedReasoning := "Thinking through the problem...\nWe should return 4.\n\nProceed."
	if content != expectedContent {
		t.Errorf("SplitChannelContent content = %q; want %q", content, expectedContent)
	}
	if reasoning != expectedReasoning {
		t.Errorf("SplitChannelContent reasoning = %q; want %q", reasoning, expectedReasoning)
	}
}

func TestSplitChannelContentReasoningOnly(t *testing.T) {
	raw := " to=selfI am still calculating..."
	content, reasoning := SplitChannelContent(raw)
	if content != "" {
		t.Errorf("SplitChannelContent content = %q; want empty", content)
	}
	expectedReasoning := "I am still calculating..."
	if reasoning != expectedReasoning {
		t.Errorf("SplitChannelContent reasoning = %q; want %q", reasoning, expectedReasoning)
	}
}

func TestChannelStreamFilterDirect(t *testing.T) {
	var tokens []string
	var reasoning []string

	filter := newChannelStreamFilter(
		func(tok string) { tokens = append(tokens, tok) },
		func(r string) { reasoning = append(reasoning, r) },
	)

	chunks := []string{"to", "=user", "I'm", " an", " AI."}
	for _, ch := range chunks {
		filter.Feed(ch)
	}
	filter.Flush()

	gotContent := strings.Join(tokens, "")
	expectedContent := "I'm an AI."
	if gotContent != expectedContent {
		t.Errorf("stream content = %q; want %q", gotContent, expectedContent)
	}
	if len(reasoning) > 0 {
		t.Errorf("unexpected reasoning = %v", reasoning)
	}
}

func TestChannelStreamFilterWithReasoning(t *testing.T) {
	var tokens []string
	var reasoning []string

	filter := newChannelStreamFilter(
		func(tok string) { tokens = append(tokens, tok) },
		func(r string) { reasoning = append(reasoning, r) },
	)

	chunks := []string{
		" to", "=self", "Thinking...", " Step 1.", "\nassistant", " to", "=user", "Hello", " world",
	}
	for _, ch := range chunks {
		filter.Feed(ch)
	}
	filter.Flush()

	gotContent := strings.Join(tokens, "")
	expectedContent := "Hello world"
	if gotContent != expectedContent {
		t.Errorf("stream content = %q; want %q", gotContent, expectedContent)
	}

	gotReasoning := strings.Join(reasoning, "")
	expectedReasoning := "Thinking... Step 1."
	if gotReasoning != expectedReasoning {
		t.Errorf("stream reasoning = %q; want %q", gotReasoning, expectedReasoning)
	}
}
