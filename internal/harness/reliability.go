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

func chatWithRetry(ctx context.Context, client LLMClient, model string, messages []llm.Message, tools []llm.Tool, thinkLevel string, notify func(int, time.Duration, error)) (llm.Message, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		reply, err := client.Chat(ctx, model, messages, tools, thinkLevel)
		if err == nil {
			return reply, nil
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
			return llm.Message{}, ctx.Err()
		case <-timer.C:
		}
	}
	return llm.Message{}, lastErr
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
