package harness

import (
	"strings"
	"testing"

	"fastllm/internal/llm"
)

func TestFormatCharCountIsReadable(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want string
	}{
		{512, "512 chars"},
		{67_200, "67k chars"},
		{1_500_000, "1.5M chars"},
	} {
		if got := formatCharCount(tc.in); got != tc.want {
			t.Errorf("formatCharCount(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The advisory only makes sense below the threshold. Above it, compaction runs
// and the percentage would read over 100% — the confusing "112%" case.
func TestContextAdvisoryOnlyAppliesBelowTheThreshold(t *testing.T) {
	cfg := DefaultCompactionConfig()
	advisoryFloor := cfg.MaxTotalChars * 3 / 4

	if advisoryFloor >= cfg.MaxTotalChars {
		t.Fatal("the advisory band must sit below the compaction threshold")
	}
	// A context inside the band does not compact, so only the advisory can fire.
	quiet := []llm.Message{{Role: "system", Content: strings.Repeat("x", advisoryFloor)}}
	if _, compacted := OnlineCompactMessages(quiet, cfg); compacted {
		t.Error("a context inside the advisory band should not compact")
	}
	pct := advisoryFloor * 100 / cfg.MaxTotalChars
	if pct < 70 || pct > 99 {
		t.Errorf("advisory percentage should read 70-99%%, got %d%%", pct)
	}
}

func TestOverBudgetContextReportsCompactionNotAPercentage(t *testing.T) {
	cfg := CompactionConfig{MaxTotalChars: 1000, KeepRecentMessages: 2, MaxToolOutputChars: 100}
	messages := []llm.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "task"}}
	for i := 0; i < 8; i++ {
		var call llm.ToolCall
		call.Function.Name = "run_command"
		call.Function.Arguments = `{"command":"go test ./..."}`
		messages = append(messages, llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call}})
		messages = append(messages, llm.Message{Role: "tool", Content: strings.Repeat("FAIL x\n", 60)})
	}
	messages = append(messages, llm.Message{Role: "user", Content: "go on"}, llm.Message{Role: "assistant", Content: "ok"})

	before := messageCharacterCount(messages)
	after, compacted := OnlineCompactMessages(messages, cfg)
	if !compacted {
		t.Fatal("an over-budget context must compact")
	}
	if before*100/cfg.MaxTotalChars <= 100 {
		t.Fatal("this fixture should be over 100% of budget, which is the confusing case")
	}
	if messageCharacterCount(after) >= before {
		t.Error("compaction should reduce the context it reports on")
	}
}
