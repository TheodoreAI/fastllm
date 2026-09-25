package harness

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"fastllm/internal/llm"
)

func TestRequestBudgetSubtractsToolSchemas(t *testing.T) {
	cfg := CompactionConfig{MaxTotalChars: 50000}
	tools := toolsForRequest(RunRequest{PermissionMode: PermissionEdit, AllowCommands: true, CommandsConfigured: true}, toolAvailability{})
	schema, _ := json.Marshal(tools)
	if got := requestBudget(cfg, tools).MaxTotalChars; got != 50000-len(schema) {
		t.Fatalf("budget = %d, want %d less %d schema characters", got, 50000, len(schema))
	}
	if got := requestBudget(CompactionConfig{MaxTotalChars: 9000}, tools).MaxTotalChars; got != minTranscriptBudget {
		t.Fatalf("budget = %d, want the %d floor", got, minTranscriptBudget)
	}
}

func TestBoundToolResultKeepsBothEndsAndSaysWhatWasCut(t *testing.T) {
	content := "HEAD" + strings.Repeat("é", 50000) + "TAIL" // two-byte runes test the cut points
	got := boundToolResult(content, 6000)
	if len(got) > 6010 {
		t.Fatalf("bounded result is %d bytes, want about 6000", len(got))
	}
	if !strings.HasPrefix(got, "HEAD") || !strings.HasSuffix(got, "TAIL") {
		t.Fatal("bounding lost the start or end of the result")
	}
	if !strings.Contains(got, "characters omitted") || !strings.Contains(got, "Narrow the request") {
		t.Fatalf("no notice of the cut:\n%s", got[:200])
	}
	if !utf8.ValidString(got) {
		t.Fatal("bounding split a character")
	}
	if short := "small result"; boundToolResult(short, 6000) != short {
		t.Fatal("a result within the limit was changed")
	}
}

// The session that failed: a 16k model, a small conversation, then one search
// whose result was 209,997 characters. Ollama rejected the 80,956-token request.
func failedSessionTranscript() []llm.Message {
	messages := []llm.Message{{Role: "system", Content: strings.Repeat("s", 16904)}, {Role: "user", Content: "fix it"}}
	for i := 0; i < 8; i++ {
		call := llm.ToolCall{ID: "c" + string(rune('a'+i)), Type: "function"}
		call.Function.Name, call.Function.Arguments = "read_file", `{"path":"x"}`
		messages = append(messages,
			llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call}},
			llm.Message{Role: "tool", ToolCallID: call.ID, Content: strings.Repeat("r", 680)})
	}
	messages = append(messages, llm.Message{Role: "assistant", Content: strings.Repeat("a", 3400)}, llm.Message{Role: "user", Content: "fix the dream hallucination"})
	call := llm.ToolCall{ID: "search", Type: "function"}
	call.Function.Name, call.Function.Arguments = "search_files", `{"pattern":"frame"}`
	return append(messages,
		llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call}},
		llm.Message{Role: "tool", ToolCallID: "search", Content: strings.Repeat("m", 209997)})
}

func TestCompactionAloneCannotFitTheFailedSession(t *testing.T) {
	budget := requestBudget(CompactionConfigForModel(nil, "x"), nil)
	budget.MaxTotalChars = contextBudgetChars(16384) - 5839
	compacted, _ := OnlineCompactMessages(failedSessionTranscript(), budget)
	// This is the bug: compaction keeps the current turn whole.
	if messageCharacterCount(compacted) <= budget.MaxTotalChars {
		t.Skip("compaction now fits this transcript by itself; the shrink step may be redundant")
	}
	fitted, changed := shrinkToolResults(compacted, budget.MaxTotalChars)
	if !changed || messageCharacterCount(fitted) > budget.MaxTotalChars {
		t.Fatalf("after shrinking: %d characters, budget %d", messageCharacterCount(fitted), budget.MaxTotalChars)
	}
	last := fitted[len(fitted)-1]
	if last.Role != "tool" || last.ToolCallID != "search" || !strings.Contains(last.Content, "characters omitted") {
		t.Fatal("the search result must survive, shortened and marked, in place")
	}
	if fitted[0].Content != compacted[0].Content {
		t.Fatal("shrinking touched the system prompt")
	}
}

func TestShrinkToolResultsLeavesAFittingTranscriptAlone(t *testing.T) {
	messages := []llm.Message{{Role: "tool", Content: strings.Repeat("x", 9000)}}
	if got, changed := shrinkToolResults(messages, 20000); changed || &got[0] != &messages[0] {
		t.Fatal("a transcript within budget was rewritten")
	}
}

// End to end: a workspace with a minified file, a 16k budget, and the model's
// broad search. The request after the search must fit.
func TestBroadSearchOverMinifiedFilesFitsA16kModel(t *testing.T) {
	workspace := t.TempDir()
	minified := strings.Repeat("@keyframes fade{0%{opacity:0}100%{opacity:1}}", 4000) // one 184 KB line
	if err := os.WriteFile(filepath.Join(workspace, "app.min.css"), []byte(minified), 0o644); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for i := 0; i < 200; i++ {
		lines = append(lines, "const frame"+strings.Repeat("x", 40)+" = 1")
	}
	if err := os.WriteFile(filepath.Join(workspace, "frames.js"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}

	var secondRequest []llm.Message
	mock := &mockLLM{turns: []func([]llm.Message) (llm.Message, error){
		func([]llm.Message) (llm.Message, error) {
			call := llm.ToolCall{ID: "search", Type: "function"}
			call.Function.Name, call.Function.Arguments = "search_files", `{"pattern":"frame"}`
			return llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call}}, nil
		},
		func(messages []llm.Message) (llm.Message, error) {
			secondRequest = append([]llm.Message(nil), messages...)
			return llm.Message{Role: "assistant", Content: "done"}, nil
		},
	}}
	runner := NewRunner(mock, workspace, "test-model")
	defer runner.Close()
	runner.contextWindow = 16384
	runner.ContextBudgetChars = contextBudgetChars(16384)
	if _, err := runner.Run(context.Background(), RunRequest{
		Task: "fix the dream hallucination", WorkingDir: workspace, Model: "test-model",
		AllowCommands: true, CommandsConfigured: true, PermissionMode: PermissionEdit,
	}, nil); err != nil {
		t.Fatal(err)
	}
	if secondRequest == nil {
		t.Fatal("the model was never called after the search")
	}
	search := secondRequest[len(secondRequest)-1]
	if search.Role != "tool" || !strings.Contains(search.Content, "app.min.css") {
		t.Fatalf("last message is not the search result: %+v", search.Role)
	}
	if strings.Contains(search.Content, strings.Repeat("@keyframes fade{0%{opacity:0}100%{opacity:1}}", 20)) {
		t.Fatal("the minified line was sent whole")
	}
	tools := toolsForRequest(RunRequest{PermissionMode: PermissionEdit, AllowCommands: true, CommandsConfigured: true}, toolAvailability{delegation: true})
	if limit := requestBudget(runner.compactionConfig(), tools).MaxTotalChars; messageCharacterCount(secondRequest) > limit {
		t.Fatalf("the request after the search is %d characters, over the %d budget", messageCharacterCount(secondRequest), limit)
	}
}

// The gauge starts from an estimate and switches to the figure a run reports;
// the two must agree, or the gauge jumps after the first turn.
func TestOverheadEstimateMatchesWhatARunSends(t *testing.T) {
	workspace := t.TempDir()
	mock := &mockLLM{turns: []func([]llm.Message) (llm.Message, error){
		func([]llm.Message) (llm.Message, error) { return llm.Message{Role: "assistant", Content: "done"}, nil },
	}}
	runner := NewRunner(mock, workspace, "test-model")
	defer runner.Close()
	req := RunRequest{WorkingDir: workspace, Model: "test-model", AllowCommands: true, CommandsConfigured: true, PermissionMode: PermissionEdit}
	estimate := runner.EstimateRequestOverheadChars(req)

	run := req
	run.Task = "say done"
	result, err := runner.Run(context.Background(), run, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.RequestOverheadChars == 0 || estimate != result.RequestOverheadChars {
		t.Fatalf("estimate %d, run reported %d", estimate, result.RequestOverheadChars)
	}
}
