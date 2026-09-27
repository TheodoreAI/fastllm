package harness

import (
	"context"
	"strings"
	"testing"

	"fastllm/internal/llm"
)

// scriptedResultsLLM answers each request with the next scripted result and
// records the messages it was sent.
type scriptedResultsLLM struct {
	results []llm.ChatResult
	sent    [][]llm.Message
}

func (s *scriptedResultsLLM) Chat(ctx context.Context, model string, messages []llm.Message, tools []llm.Tool, thinkLevel string) (llm.Message, error) {
	r, err := s.ChatWithUsage(ctx, model, messages, tools, thinkLevel)
	return r.Message, err
}

func (s *scriptedResultsLLM) ChatWithUsage(ctx context.Context, model string, messages []llm.Message, tools []llm.Tool, thinkLevel string) (llm.ChatResult, error) {
	s.sent = append(s.sent, append([]llm.Message(nil), messages...))
	i := min(len(s.sent)-1, len(s.results)-1)
	return s.results[i], nil
}

func cutOff(content string) llm.ChatResult {
	return llm.ChatResult{
		Message:   llm.Message{Role: "assistant", Content: content},
		Usage:     llm.Usage{PromptTokens: 100, CompletionTokens: 8192},
		HasUsage:  true,
		Truncated: true,
	}
}

func answer(content string) llm.ChatResult {
	return llm.ChatResult{Message: llm.Message{Role: "assistant", Content: content}}
}

func runScripted(t *testing.T, results ...llm.ChatResult) (*RunResult, *scriptedResultsLLM, []Event) {
	t.Helper()
	root := t.TempDir()
	client := &scriptedResultsLLM{results: results}
	r := NewRunner(client, root, "test-model")
	t.Cleanup(r.Close)
	var events []Event
	result, _ := r.Run(context.Background(), RunRequest{Task: "fix the feed", WorkingDir: root, Model: "test-model", MaxTurns: 10},
		func(ev Event) { events = append(events, ev) })
	return result, client, events
}

// The run from the report: a reply spent entirely on reasoning used to end
// the run as an empty success. Now the model is asked to continue.
func TestTruncatedEmptyReplyIsContinued(t *testing.T) {
	result, client, events := runScripted(t, cutOff(""), answer("Fixed the scroll hint."))
	if !result.Success || result.FinalResponse != "Fixed the scroll hint." {
		t.Fatalf("success=%v final=%q error=%q", result.Success, result.FinalResponse, result.Error)
	}
	if len(client.sent) != 2 {
		t.Fatalf("expected a continuation request, got %d requests", len(client.sent))
	}
	last := client.sent[1][len(client.sent[1])-1]
	if last.Role != "user" || last.Content != truncatedReplyPrompt {
		t.Fatalf("continuation request ended with %s: %q", last.Role, last.Content)
	}
	for _, m := range client.sent[1] {
		if m.Role == "assistant" && m.Content == "" && len(m.ToolCalls) == 0 {
			t.Fatal("an empty assistant message was sent back to the model")
		}
	}
	noticed := false
	for _, ev := range events {
		noticed = noticed || (ev.Type == EventNotice && strings.Contains(ev.Response, "output limit"))
	}
	if !noticed {
		t.Fatal("the cut-off was not reported to the user")
	}
}

// Text from a cut-off reply is kept and joined with its continuation.
func TestTruncatedAnswerIsJoinedWithContinuation(t *testing.T) {
	result, client, _ := runScripted(t, cutOff("The hint is pinned by "), answer("absolute positioning."))
	if result.FinalResponse != "The hint is pinned by absolute positioning." {
		t.Fatalf("final = %q", result.FinalResponse)
	}
	if got := client.sent[1][len(client.sent[1])-2]; got.Role != "assistant" || got.Content != "The hint is pinned by " {
		t.Fatalf("the partial answer was not replayed: %+v", got)
	}
}

// Two cut-offs in a row end the run with the reason, not an empty success.
func TestRepeatedTruncationStopsWithReason(t *testing.T) {
	result, client, _ := runScripted(t, cutOff(""), cutOff(""), answer("never reached"))
	if result.Success || !strings.Contains(result.Error, "output limit") || !strings.Contains(result.Error, "max_tokens") {
		t.Fatalf("success=%v error=%q", result.Success, result.Error)
	}
	if len(client.sent) != 2 {
		t.Fatalf("expected the run to stop after two cut-offs, made %d requests", len(client.sent))
	}
}
