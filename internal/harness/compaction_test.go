package harness

import (
	"strings"
	"testing"

	"fastllm/internal/llm"
)

func TestCompactMessages(t *testing.T) {
	hugeToolOutput := strings.Repeat("a", 5000)

	msgs := []llm.Message{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "user task"},
		{Role: "assistant", Content: "checking file..."},
		{Role: "tool", Content: hugeToolOutput, ToolCallID: "call-1"},
		{Role: "assistant", Content: "checking second file..."},
		{Role: "tool", Content: hugeToolOutput, ToolCallID: "call-2"},
		{Role: "assistant", Content: "recent thinking"},
		{Role: "tool", Content: "recent small output", ToolCallID: "call-3"},
	}

	cfg := CompactionConfig{
		MaxTotalChars:      2000,
		KeepRecentMessages: 2,
		MaxToolOutputChars: 200,
	}

	compacted, pruned := CompactMessages(msgs, cfg)
	if !pruned {
		t.Fatal("expected pruned to be true")
	}

	// Tool call-1 should be truncated
	if !strings.Contains(compacted[3].Content, "Truncated") {
		t.Errorf("expected call-1 to be truncated, got len %d", len(compacted[3].Content))
	}

	// Recent message should not be truncated
	if compacted[7].Content != "recent small output" {
		t.Errorf("recent message was modified: %s", compacted[7].Content)
	}

	// System prompt untouched
	if compacted[0].Content != "system prompt" {
		t.Errorf("system prompt was modified: %s", compacted[0].Content)
	}
}
