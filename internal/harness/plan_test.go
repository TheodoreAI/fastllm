package harness

import (
	"strings"
	"testing"

	"fastllm/internal/llm"
)

func TestFormatPlanUpdateRendersEverySection(t *testing.T) {
	got := formatPlanUpdate(
		"ship the parser fix",
		[]string{"reproduce the failure", "isolate the bad token"},
		"write the regression test",
		[]string{"run the full suite"},
	)
	for _, want := range []string{
		"[Plan Updated]",
		"Goal: ship the parser fix",
		"Completed:\n- reproduce the failure\n- isolate the bad token",
		"Current:\n- write the regression test",
		"Remaining:\n- run the full suite",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}

func TestFormatPlanUpdateOmitsEmptySections(t *testing.T) {
	got := formatPlanUpdate("", nil, "first step", []string{"", "   "})
	for _, unwanted := range []string{"Goal:", "Completed:", "Remaining:"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("rendered empty section %q in:\n%s", unwanted, got)
		}
	}
	if !strings.Contains(got, "Current:\n- first step") {
		t.Fatalf("current step missing from:\n%s", got)
	}
}

func TestFormatPlanUpdateCollapsesMultilineSteps(t *testing.T) {
	got := formatPlanUpdate("goal", []string{"first line\nsecond line"}, "current", nil)
	if !strings.Contains(got, "- first line second line\n") {
		t.Fatalf("multiline step was not collapsed onto one bullet:\n%s", got)
	}
}

func TestPlanBoundaryLowersCompactionBudget(t *testing.T) {
	base := CompactionConfig{MaxTotalChars: 1000, KeepRecentMessages: 6, MaxToolOutputChars: 800}
	got := planBoundaryCompactionConfig(base, 800, true)
	if got.MaxTotalChars != 799 {
		t.Fatalf("expected the budget lowered below the context size, got %d", got.MaxTotalChars)
	}
	if got.KeepRecentMessages != base.KeepRecentMessages || got.MaxToolOutputChars != base.MaxToolOutputChars {
		t.Fatalf("unrelated config fields changed: %+v", got)
	}
}

func TestPlanBoundaryLeavesBudgetAloneWhenNotWarranted(t *testing.T) {
	base := CompactionConfig{MaxTotalChars: 1000, KeepRecentMessages: 6, MaxToolOutputChars: 800}
	if got := planBoundaryCompactionConfig(base, 800, false); got.MaxTotalChars != base.MaxTotalChars {
		t.Fatalf("without a boundary the budget must be untouched, got %d", got.MaxTotalChars)
	}
	if got := planBoundaryCompactionConfig(base, 400, true); got.MaxTotalChars != base.MaxTotalChars {
		t.Fatalf("context below half the budget must not force compaction, got %d", got.MaxTotalChars)
	}
}

func TestPlanBoundaryTriggersCompactionThatWouldOtherwiseWait(t *testing.T) {
	messages := []llm.Message{{Role: "system", Content: "system"}, {Role: "user", Content: "fix parser"}}
	for index := 0; index < 8; index++ {
		var call llm.ToolCall
		call.Function.Name = "run_command"
		call.Function.Arguments = `{"command":"go test ./..."}`
		messages = append(messages, llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call}})
		messages = append(messages, llm.Message{Role: "tool", Content: strings.Repeat("FAIL parser_test.go:42\n", 20)})
	}
	messages = append(messages, llm.Message{Role: "user", Content: "continue"}, llm.Message{Role: "assistant", Content: "working"})

	contextChars := messageCharacterCount(messages)
	base := CompactionConfig{MaxTotalChars: contextChars * 3 / 2, KeepRecentMessages: 2, MaxToolOutputChars: 100}

	if _, compacted := OnlineCompactMessages(messages, base); compacted {
		t.Fatal("context under the budget must not compact without a plan boundary")
	}

	boundary := planBoundaryCompactionConfig(base, contextChars, true)
	result, compacted := OnlineCompactMessages(messages, boundary)
	if !compacted {
		t.Fatal("a completed plan step should let compaction run early")
	}
	if !isConversationSummary(result[2]) || !strings.Contains(result[2].Content, "State Checkpoint") {
		t.Fatalf("expected a checkpoint at the boundary, got %+v", result[2])
	}
}
