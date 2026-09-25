package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"fastllm/internal/llm"
)

// The retention benchmark measures what compaction keeps. A long coding session
// is built with uniquely named facts planted at known ages, compacted exactly as
// Runner.Run does for a 16k local model, and each fact is looked for in the
// result. Minimums start at what the strategy retained when this benchmark was
// written and are raised as it improves, so a change that loses facts fails.

const retentionRounds = 30

// retentionAge buckets a round: the tail compaction keeps verbatim is "recent".
func retentionAge(round int) string {
	switch {
	case round < 10:
		return "early"
	case round < 20:
		return "middle"
	case round < retentionRounds-3:
		return "late"
	default:
		return "recent"
	}
}

type plantedFact struct {
	category, age, marker string
}

// retentionSession builds the session and returns the facts planted in it.
func retentionSession() ([]llm.Message, []plantedFact) {
	var facts []plantedFact
	plant := func(category, age, marker string) string {
		facts = append(facts, plantedFact{category, age, marker})
		return marker
	}

	// An earlier compaction's summary, longer than one is carried in full, with
	// facts at its start and at its end.
	carried := "[State Checkpoint]\nEarlier checkpoint body.\n- " + plant("carried summary", "start", "CARRIED-START-7q") + "\n" +
		strings.Repeat("- routine earlier step that matters little\n", 120) +
		"- " + plant("carried summary", "end", "CARRIED-END-3k") + "\n"

	messages := []llm.Message{
		{Role: "system", Content: strings.Repeat("You are a coding agent. ", 700)},
		{Role: "user", Content: "Build the nutrition tracker. " + plant("task", "first", "TASK-NUTRITION-1x")},
		{Role: "user", Content: conversationSummaryOpen + "\n" + carried + conversationSummaryClose + "\n\nContinue."},
	}
	for round := 0; round < retentionRounds; round++ {
		age := retentionAge(round)
		file := plant("file changed", age, fmt.Sprintf("src/module_%02d.go", round))
		if round%3 == 0 {
			messages = append(messages, llm.Message{Role: "user", Content: "Next: " + plant("request", age, fmt.Sprintf("REQUEST-%02d-ask", round)) + " please handle the next part."})
		}
		write := llm.ToolCall{ID: fmt.Sprintf("w%02d", round), Type: "function"}
		write.Function.Name = "write_file"
		args, _ := json.Marshal(map[string]string{"path": file, "content": strings.Repeat("x", 300)})
		write.Function.Arguments = string(args)
		output := fmt.Sprintf("wrote %s\n%s", file, strings.Repeat("build log line\n", 130))
		if round%4 == 1 {
			output += "Error: " + plant("error", age, fmt.Sprintf("ERROR-%02d-nilmap", round)) + " in handler\n"
		}
		messages = append(messages,
			llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{write}},
			llm.Message{Role: "tool", ToolCallID: write.ID, Content: output},
			llm.Message{Role: "assistant", Content: fmt.Sprintf("Done with part %d. We decided to %s because the old parser drops units.", round,
				plant("decision", age, fmt.Sprintf("DECISION-%02d-units", round)))})
	}
	messages = append(messages, llm.Message{Role: "user", Content: "Finally, " + plant("request", "recent", "REQUEST-LATEST-go") + "."})
	return messages, facts
}

// retentionBudget is Runner.Run's budget for a 16k model, net of the 5,839
// characters of tool schemas measured on it.
func retentionBudget() CompactionConfig {
	cfg := DefaultCompactionConfig()
	cfg.MaxTotalChars = contextBudgetChars(16384) - 5839
	return cfg
}

// compactAsRunDoes is the pipeline Runner.Run applies before each request.
func compactAsRunDoes(messages []llm.Message, budget CompactionConfig) []llm.Message {
	compacted, _ := OnlineCompactMessages(messages, budget)
	compacted, _ = shrinkToolResults(compacted, budget.MaxTotalChars)
	return compacted
}

func transcriptText(messages []llm.Message) string {
	var b strings.Builder
	for _, message := range messages {
		b.WriteString(message.Content + "\n")
		for _, call := range message.ToolCalls {
			b.WriteString(call.Function.Name + " " + call.Function.Arguments + "\n")
		}
	}
	return b.String()
}

// retentionFloor is the share of facts, in percent, each category and age must
// keep. Raise a floor when a change improves it; never lower one to pass.
var retentionFloor = map[string]int{
	// Baseline in parentheses where it was lower: first-N lists, a head-only
	// carried summary, no assistant prose, and a fixed six-message tail.
	"carried summary/start": 100,
	"carried summary/end":   100, // (0)
	"decision/early":        0,   // the checkpoint's notes are newest-first; early ones go first
	"decision/middle":       30,  // (0)
	"decision/late":         100, // (0)
	"decision/recent":       100,
	"error/early":           100,
	"error/middle":          100, // (50)
	"error/late":            100, // (0)
	"error/recent":          100,
	"file changed/early":    100, // (70)
	"file changed/middle":   100, // (50)
	"file changed/late":     100, // (0)
	"file changed/recent":   100, // (66)
	"request/early":         100,
	"request/middle":        100, // (0)
	"request/late":          100, // (0)
	"request/recent":        100,
	"task/first":            100,
}

func TestCompactionRetention(t *testing.T) {
	messages, facts := retentionSession()
	budget := retentionBudget()
	if messageCharacterCount(messages) <= budget.MaxTotalChars {
		t.Fatalf("fixture is inside the budget (%d <= %d); it measures nothing", messageCharacterCount(messages), budget.MaxTotalChars)
	}
	compacted := compactAsRunDoes(messages, budget)
	if got := messageCharacterCount(compacted); got > budget.MaxTotalChars {
		t.Errorf("compacted transcript is %d characters, over the %d budget", got, budget.MaxTotalChars)
	}
	// Compacting to just under the budget would compact again on the next turn.
	if got, target := messageCharacterCount(compacted), budget.MaxTotalChars*85/100; got > target {
		t.Errorf("compacted transcript is %d characters, leaving under 15%% of the %d budget for the next turns", got, budget.MaxTotalChars)
	}
	assertAlternates(t, compacted)
	again := compactAsRunDoes(messages, budget)
	if transcriptText(again) != transcriptText(compacted) {
		t.Error("compaction is not deterministic")
	}

	text := transcriptText(compacted)
	kept, total := map[string]int{}, map[string]int{}
	for _, fact := range facts {
		key := fact.category + "/" + fact.age
		total[key]++
		if strings.Contains(text, fact.marker) {
			kept[key]++
		}
	}
	keys := make([]string, 0, len(total))
	for key := range total {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var report strings.Builder
	fmt.Fprintf(&report, "retention after compacting %d to %d characters (budget %d):\n",
		messageCharacterCount(messages), messageCharacterCount(compacted), budget.MaxTotalChars)
	for _, key := range keys {
		percent := kept[key] * 100 / total[key]
		fmt.Fprintf(&report, "  %-24s %3d%%  (%d/%d)\n", key, percent, kept[key], total[key])
		if floor, ok := retentionFloor[key]; ok && percent < floor {
			t.Errorf("%s retention %d%% fell below its floor %d%%", key, percent, floor)
		}
	}
	t.Log(report.String())
}

// Real sessions have no planted facts, but they must still fit and compact
// deterministically. Runs only where session files exist.
func TestCompactionOnRealSessions(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	paths, _ := filepath.Glob(filepath.Join(home, ".fastllm", "sessions", "*.json"))
	if len(paths) == 0 {
		t.Skip("no local sessions")
	}
	budget := retentionBudget()
	checked := 0
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var session struct {
			Messages []llm.Message `json:"messages"`
		}
		if json.Unmarshal(data, &session) != nil || messageCharacterCount(session.Messages) <= budget.MaxTotalChars {
			continue
		}
		messages := append([]llm.Message{{Role: "system", Content: strings.Repeat("s", 17000)}}, session.Messages...)
		staged, _ := OnlineCompactMessages(messages, budget)
		compacted := compactAsRunDoes(messages, budget)
		if transcriptText(compactAsRunDoes(messages, budget)) != transcriptText(compacted) {
			t.Errorf("%s: compaction is not deterministic", filepath.Base(path))
		}
		if got := messageCharacterCount(compacted); got > budget.MaxTotalChars {
			t.Errorf("%s: compacted to %d characters, over the %d budget", filepath.Base(path), got, budget.MaxTotalChars)
		}
		t.Logf("%s: %d -> %d characters by compaction, %d after shrinking tool results (budget %d)", filepath.Base(path),
			messageCharacterCount(messages), messageCharacterCount(staged), messageCharacterCount(compacted), budget.MaxTotalChars)
		checked++
	}
	if checked == 0 {
		t.Skip("no local session exceeds the budget")
	}
}

// When the overflow is older tool output, clearing it is enough: every user and
// assistant message must survive byte for byte and no checkpoint is written.
func TestCompactionClearsOldToolOutputFirst(t *testing.T) {
	messages := []llm.Message{
		{Role: "system", Content: "You are a coding agent."},
		{Role: "user", Content: "Fix the build."},
	}
	for i := 0; i < 8; i++ {
		call := llm.ToolCall{ID: fmt.Sprintf("r%d", i), Type: "function"}
		call.Function.Name = "read_file"
		call.Function.Arguments = fmt.Sprintf(`{"path":"pkg/file%d.go"}`, i)
		messages = append(messages,
			llm.Message{Role: "assistant", Content: fmt.Sprintf("Reading file %d.", i), ToolCalls: []llm.ToolCall{call}},
			llm.Message{Role: "tool", ToolCallID: call.ID, Content: fmt.Sprintf("package pkg // file %d\n", i) + strings.Repeat("code line\n", 600)},
			llm.Message{Role: "user", Content: fmt.Sprintf("Now step %d.", i)})
	}
	cfg := DefaultCompactionConfig()
	cfg.MaxTotalChars = 30000
	compacted, changed := OnlineCompactMessages(messages, cfg)
	if !changed || messageCharacterCount(compacted) > cfg.MaxTotalChars {
		t.Fatalf("changed=%v, %d characters for a %d budget", changed, messageCharacterCount(compacted), cfg.MaxTotalChars)
	}
	if len(compacted) != len(messages) {
		t.Fatalf("%d messages became %d; clearing tool output must not remove messages", len(messages), len(compacted))
	}
	receipts := 0
	for i, message := range compacted {
		if message.Role != "tool" {
			if message.Content != messages[i].Content || strings.Contains(message.Content, conversationSummaryOpen) {
				t.Errorf("message %d (%s) changed: %q", i, message.Role, message.Content)
			}
			continue
		}
		if strings.HasPrefix(message.Content, toolReceiptPrefix) {
			receipts++
			if !strings.Contains(message.Content, fmt.Sprintf("read_file pkg/file%d.go", (i-3)/3)) {
				t.Errorf("receipt %d does not say what it was: %q", i, message.Content)
			}
		}
	}
	if receipts == 0 {
		t.Error("no tool output was cleared")
	}
	if last := compacted[len(compacted)-2]; strings.HasPrefix(last.Content, toolReceiptPrefix) {
		t.Error("the most recent tool result was cleared; the recent tail must stay verbatim")
	}
}
