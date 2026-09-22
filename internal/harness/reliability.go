package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"fastllm/internal/llm"
)

type usageLLMClient interface {
	ChatWithUsage(context.Context, string, []llm.Message, []llm.Tool, string) (llm.ChatResult, error)
}

// streamingLLMClient is implemented by clients that can stream assistant text
// while still returning tool calls. Checked at runtime like usageLLMClient, so a
// client without it (a test double, a provider not yet converted) silently keeps
// the non-streaming path.
type streamingLLMClient interface {
	StreamChatWithTools(ctx context.Context, model string, messages []llm.Message, tools []llm.Tool, thinkLevel string, onToken func(string), onReasoning func(string)) (llm.ChatResult, bool, error)
}

// streamSink receives assistant text as it arrives.
//
// Discard is called when text already handed to Emit turns out not to be part of
// the answer: a model that wrote its tool call as prose, or channel markers
// stripped at end of stream. The sink must then remove everything it showed for
// this attempt. Retry does the same, because a stream that dies partway has
// already emitted text that the next attempt will emit again.
type streamSink struct {
	Emit    func(string)
	Discard func()
}

func chatWithRetry(ctx context.Context, client LLMClient, model string, messages []llm.Message, tools []llm.Tool, thinkLevel string, notify func(int, time.Duration, error)) (llm.ChatResult, error) {
	return chatWithRetryStreaming(ctx, client, model, messages, tools, thinkLevel, notify, nil)
}

// chatWithRetryStreaming is chatWithRetry with an optional streaming sink. A nil
// sink, or a client that cannot stream, behaves exactly as before.
func chatWithRetryStreaming(ctx context.Context, client LLMClient, model string, messages []llm.Message, tools []llm.Tool, thinkLevel string, notify func(int, time.Duration, error), sink *streamSink) (llm.ChatResult, error) {
	streamClient, canStream := client.(streamingLLMClient)
	streaming := canStream && sink != nil && sink.Emit != nil
	usageClient, hasUsage := client.(usageLLMClient)

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		var result llm.ChatResult
		var err error
		switch {
		case streaming:
			var discard bool
			result, discard, err = streamClient.StreamChatWithTools(ctx, model, messages, tools, thinkLevel, sink.Emit, nil)
			// Anything shown must come back off the screen when the stream failed
			// (the retry re-emits it) or when the text was not the answer after all.
			if (err != nil || discard) && sink.Discard != nil {
				sink.Discard()
			}
		case hasUsage:
			result, err = usageClient.ChatWithUsage(ctx, model, messages, tools, thinkLevel)
		default:
			result.Message, err = client.Chat(ctx, model, messages, tools, thinkLevel)
		}
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !isTransientModelError(err) || attempt == 2 {
			break
		}
		delay := time.Duration(1<<attempt) * 500 * time.Millisecond
		if notify != nil {
			notify(attempt+2, delay, err)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return llm.ChatResult{}, ctx.Err()
		case <-timer.C:
		}
	}
	return llm.ChatResult{}, lastErr
}

func isTransientModelError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary()) {
		return true
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{"429", "500", "502", "503", "504", "eof", "connection reset", "connection refused", "temporarily unavailable", "timeout"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func decodeToolArguments(raw string, target any) error {
	raw, err := normalizeToolArguments(raw)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(raw), target); err != nil {
		return fmt.Errorf("malformed tool arguments: %w", err)
	}
	return nil
}

func normalizeToolArguments(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```json") {
		raw = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(raw, "```json"), "```"))
	}
	if strings.HasPrefix(raw, "```") {
		raw = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(raw, "```"), "```"))
	}
	if !json.Valid([]byte(raw)) {
		return "", fmt.Errorf("malformed tool arguments: invalid JSON")
	}
	return raw, nil
}
