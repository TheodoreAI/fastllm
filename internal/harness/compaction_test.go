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

// Manual /compact must collapse a transcript that is nowhere near the automatic
// threshold -- that is the whole point of invoking it by hand.
func TestForceCompactMessagesBelowThreshold(t *testing.T) {
	messages := []llm.Message{{Role: "system", Content: "system"}, {Role: "user", Content: "fix parser"}}
	for index := 0; index < 8; index++ {
		var call llm.ToolCall
		call.Function.Name = "run_command"
		call.Function.Arguments = `{"command":"go test ./..."}`
		messages = append(messages, llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call}})
		messages = append(messages, llm.Message{Role: "tool", Content: strings.Repeat("FAIL parser_test.go:42\n", 20)})
	}
	messages = append(messages, llm.Message{Role: "user", Content: "continue"}, llm.Message{Role: "assistant", Content: "working"})

	cfg := DefaultCompactionConfig()
	before := messageCharacterCount(messages)
	if before >= cfg.MaxTotalChars {
		t.Fatalf("fixture must sit under the automatic budget, got %d >= %d", before, cfg.MaxTotalChars)
	}
	if _, auto := OnlineCompactMessages(messages, cfg); auto {
		t.Fatal("automatic compaction should not fire below the budget")
	}

	compacted, ok := ForceCompactMessages(messages, cfg)
	if !ok {
		t.Fatal("forced compaction did not fire")
	}
	if got := messageCharacterCount(compacted); got >= before {
		t.Fatalf("forced compaction did not shrink context: %d -> %d", before, got)
	}
	if compacted[0].Role != "system" || compacted[1].Content != "fix parser" {
		t.Fatalf("forced compaction disturbed the preserved head: %+v", compacted[:2])
	}
}

// A short transcript has no completed older turns to collapse, so /compact must
// report that plainly instead of mangling the tail.
func TestForceCompactMessagesShortTranscript(t *testing.T) {
	messages := []llm.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}
	compacted, ok := ForceCompactMessages(messages, DefaultCompactionConfig())
	if ok {
		t.Fatalf("forced compaction should no-op on a short transcript, got %+v", compacted)
	}
	if len(compacted) != len(messages) {
		t.Fatalf("no-op path altered the transcript: %d -> %d", len(messages), len(compacted))
	}
}

// Empty transcripts must not drive MaxTotalChars to zero or negative.
func TestForceCompactMessagesEmpty(t *testing.T) {
	if _, ok := ForceCompactMessages(nil, DefaultCompactionConfig()); ok {
		t.Fatal("forced compaction fired on an empty transcript")
	}
}
