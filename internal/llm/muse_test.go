package llm

import (
	"strings"
	"testing"
)

func TestCanonicalOSUModelName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"meta-models/Muse-Glimmer-30B", "muse-glimmer"},
		{"muse-glimmer", "muse-glimmer"},
		{"muse-glimmer-30b", "muse-glimmer"},
		{"MUSE_GLIMMER", "muse-glimmer"},
		{"gemma-4-31b", "gemma-4-31b"},
	}

	for _, tt := range tests {
		got := CanonicalOSUModelName(tt.input)
		if got != tt.expected {
			t.Errorf("CanonicalOSUModelName(%q) = %q; want %q", tt.input, got, tt.expected)
		}
	}
}

func TestCleanMuseContentDirectUser(t *testing.T) {
	// The exact user report:
	raw := "to=userI'm an open source large language model trained by Meta Superintelligence Labs. Nice to meet you!"
	content, reasoning := CleanMuseContent(raw)
	expectedContent := "I'm an open source large language model trained by Meta Superintelligence Labs. Nice to meet you!"
	if content != expectedContent {
		t.Errorf("CleanMuseContent content = %q; want %q", content, expectedContent)
	}
	if reasoning != "" {
		t.Errorf("CleanMuseContent reasoning = %q; want empty", reasoning)
	}
}

func TestCleanMuseContentWithReasoning(t *testing.T) {
	raw := " to=selfThinking through the problem...\nWe should return 4.\n\nProceed.assistant to=userThe answer is 4."
	content, reasoning := CleanMuseContent(raw)
	expectedContent := "The answer is 4."
	expectedReasoning := "Thinking through the problem...\nWe should return 4.\n\nProceed."
	if content != expectedContent {
		t.Errorf("CleanMuseContent content = %q; want %q", content, expectedContent)
	}
	if reasoning != expectedReasoning {
		t.Errorf("CleanMuseContent reasoning = %q; want %q", reasoning, expectedReasoning)
	}
}

func TestCleanMuseContentReasoningOnly(t *testing.T) {
	raw := " to=selfI am still calculating..."
	content, reasoning := CleanMuseContent(raw)
	if content != "" {
		t.Errorf("CleanMuseContent content = %q; want empty", content)
	}
	expectedReasoning := "I am still calculating..."
	if reasoning != expectedReasoning {
		t.Errorf("CleanMuseContent reasoning = %q; want %q", reasoning, expectedReasoning)
	}
}

func TestMuseStreamFilterDirect(t *testing.T) {
	var tokens []string
	var reasoning []string

	filter := newMuseStreamFilter(
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

func TestMuseStreamFilterWithReasoning(t *testing.T) {
	var tokens []string
	var reasoning []string

	filter := newMuseStreamFilter(
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
