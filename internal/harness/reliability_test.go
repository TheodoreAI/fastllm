package harness

import (
	"context"
	"errors"
	"testing"

	"fastllm/internal/llm"
)

type flakyLLM struct{ calls int }

func (f *flakyLLM) Chat(context.Context, string, []llm.Message, []llm.Tool, string) (llm.Message, error) {
	f.calls++
	if f.calls < 3 {
		return llm.Message{}, errors.New("503 temporarily unavailable")
	}
	return llm.Message{Role: "assistant", Content: "recovered"}, nil
}

func TestChatWithRetryRecoversTransientFailure(t *testing.T) {
	client := &flakyLLM{}
	reply, err := chatWithRetry(context.Background(), client, "model", nil, nil, "", nil)
	if err != nil || reply.Message.Content != "recovered" || client.calls != 3 {
		t.Fatalf("reply=%+v calls=%d err=%v", reply, client.calls, err)
	}
}

func TestNormalizeToolArgumentsRecoversJSONFence(t *testing.T) {
	got, err := normalizeToolArguments("```json\n{\"path\":\"README.md\"}\n```")
	if err != nil || got != "{\"path\":\"README.md\"}" {
		t.Fatalf("got %q, err=%v", got, err)
	}
	if _, err := normalizeToolArguments("{broken"); err == nil {
		t.Fatal("expected malformed JSON error")
	}
}

func TestInteractiveCompletionSources(t *testing.T) {
	got := interactiveCompletions("/res", 4, t.TempDir(), nil, []string{"session-1"})
	if len(got) == 0 || got[0] != "/resume" {
		t.Fatalf("slash completions: %#v", got)
	}
	got = interactiveCompletions("/resume ses", len("/resume ses"), t.TempDir(), nil, []string{"session-1"})
	if len(got) != 1 || got[0] != "session-1" {
		t.Fatalf("session completions: %#v", got)
	}
}
