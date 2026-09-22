package harness

import (
	"context"
	"errors"
	"strings"
	"testing"

	"fastllm/internal/llm"
)

// fakeStreamClient streams a canned reply, optionally failing the first attempt.
type fakeStreamClient struct {
	chunks    []string
	failFirst bool
	discard   bool
	calls     int
	toolCalls []llm.ToolCall
}

func (f *fakeStreamClient) Chat(ctx context.Context, model string, messages []llm.Message, tools []llm.Tool, thinkLevel string) (llm.Message, error) {
	return llm.Message{Role: "assistant", Content: "non-streaming"}, nil
}

func (f *fakeStreamClient) StreamChatWithTools(ctx context.Context, model string, messages []llm.Message, tools []llm.Tool, thinkLevel string, onToken func(string), onReasoning func(string)) (llm.ChatResult, bool, error) {
	f.calls++
	for _, c := range f.chunks {
		onToken(c)
	}
	if f.failFirst && f.calls == 1 {
		return llm.ChatResult{}, false, errors.New("connection reset")
	}
	return llm.ChatResult{Message: llm.Message{
		Role:      "assistant",
		Content:   strings.Join(f.chunks, ""),
		ToolCalls: f.toolCalls,
	}}, f.discard, nil
}

// A stream that dies partway has already shown text; the retry re-emits it, so
// the sink must be told to discard before the second attempt.
func TestStreamingRetryDiscardsPartialText(t *testing.T) {
	client := &fakeStreamClient{chunks: []string{"partial ", "answer"}, failFirst: true}
	var shown strings.Builder
	var discards int
	sink := &streamSink{
		Emit:    func(s string) { shown.WriteString(s) },
		Discard: func() { discards++; shown.Reset() },
	}

	res, err := chatWithRetryStreaming(context.Background(), client, "m", nil, nil, "", nil, sink)
	if err != nil {
		t.Fatalf("retry did not recover: %v", err)
	}
	if client.calls != 2 {
		t.Fatalf("attempts = %d, want 2", client.calls)
	}
	if discards != 1 {
		t.Fatalf("discards = %d, want exactly 1 (after the failed attempt)", discards)
	}
	// Only the successful attempt's text may remain.
	if shown.String() != "partial answer" {
		t.Fatalf("visible text = %q, want only the retry's output", shown.String())
	}
	if res.Message.Content != "partial answer" {
		t.Fatalf("result content = %q", res.Message.Content)
	}
}

// A model that writes its tool call as prose streams that JSON as visible text;
// the discard flag must retract it.
func TestStreamingDiscardFlagRetractsProseToolCall(t *testing.T) {
	client := &fakeStreamClient{chunks: []string{`{"name":"write_file"}`}, discard: true}
	var shown strings.Builder
	var discards int
	sink := &streamSink{
		Emit:    func(s string) { shown.WriteString(s) },
		Discard: func() { discards++; shown.Reset() },
	}

	if _, err := chatWithRetryStreaming(context.Background(), client, "m", nil, nil, "", nil, sink); err != nil {
		t.Fatal(err)
	}
	if discards != 1 {
		t.Fatalf("discards = %d, want 1", discards)
	}
	if shown.String() != "" {
		t.Fatalf("prose tool call left on screen: %q", shown.String())
	}
}

// A nil sink, or a client that cannot stream, must take the old path unchanged.
func TestNilSinkFallsBackToNonStreaming(t *testing.T) {
	client := &fakeStreamClient{chunks: []string{"streamed"}}
	res, err := chatWithRetryStreaming(context.Background(), client, "m", nil, nil, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if client.calls != 0 {
		t.Fatalf("streaming was used despite a nil sink (calls=%d)", client.calls)
	}
	if res.Message.Content != "non-streaming" {
		t.Fatalf("content = %q, want the non-streaming path", res.Message.Content)
	}
}

// The harness always wraps its client in a *llm.Router (see cli.go), so the
// Router must satisfy streamingLLMClient. When it did not, the type assertion
// in chatWithRetryStreaming failed and streaming was silently disabled for
// every model, local ones included -- the answer still arrived, all at once.
func TestRouterSatisfiesStreamingClient(t *testing.T) {
	router := llm.NewRouter(&llm.Client{}, llm.CloudProviderConfig{})
	if _, ok := interface{}(router).(streamingLLMClient); !ok {
		t.Fatal("*llm.Router no longer satisfies streamingLLMClient; streaming is silently disabled")
	}
	// The concrete local client must satisfy it too, since the Router delegates.
	if _, ok := interface{}(&llm.Client{}).(streamingLLMClient); !ok {
		t.Fatal("*llm.Client no longer satisfies streamingLLMClient")
	}
}
