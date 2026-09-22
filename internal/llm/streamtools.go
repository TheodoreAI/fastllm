package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

// streamToolCallDelta is one tool-call fragment from an OpenAI-wire streaming
// chunk. Providers split a single call across many chunks, and the shape of that
// split is not uniform:
//
//   - vLLM (gemma-4-31b) sends the id and function name only in the FIRST
//     fragment, then a dozen more fragments carrying the same Index with null
//     name/id and a slice of the arguments JSON. Observed: one 1280-character
//     argument object split across 12 fragments.
//   - Ollama (qwen3.5:9b) sends the whole call, including a 3114-character
//     argument payload, in a single fragment.
//
// Index is therefore the only field present on every fragment, and is what ties
// them together.
type streamToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// toolCallAccumulator reassembles streamed tool-call fragments into whole calls.
//
// It latches id/name/type from whichever fragment first carries them and appends
// every arguments fragment in arrival order, because later fragments null those
// identifying fields rather than repeating them. Overwriting on each fragment
// would leave the finished call with an empty name.
type toolCallAccumulator struct {
	byIndex map[int]*ToolCall
	order   []int
}

func newToolCallAccumulator() *toolCallAccumulator {
	return &toolCallAccumulator{byIndex: make(map[int]*ToolCall)}
}

// Add folds one fragment into the call it belongs to.
func (a *toolCallAccumulator) Add(delta streamToolCallDelta) {
	call, seen := a.byIndex[delta.Index]
	if !seen {
		call = &ToolCall{}
		a.byIndex[delta.Index] = call
		a.order = append(a.order, delta.Index)
	}
	// Latch, never overwrite with the empty values later fragments carry.
	if delta.ID != "" {
		call.ID = delta.ID
	}
	if delta.Type != "" {
		call.Type = delta.Type
	}
	if delta.Function.Name != "" {
		call.Function.Name = delta.Function.Name
	}
	call.Function.Arguments += delta.Function.Arguments
}

// Calls returns the assembled calls ordered by the provider's own index, so a
// turn requesting several tools preserves the order the model asked for them in
// rather than Go's map iteration order.
func (a *toolCallAccumulator) Calls() []ToolCall {
	if len(a.order) == 0 {
		return nil
	}
	indices := append([]int(nil), a.order...)
	sort.Ints(indices)
	calls := make([]ToolCall, 0, len(indices))
	for _, index := range indices {
		call := a.byIndex[index]
		// A fragment stream that produced neither a name nor arguments is not a
		// usable call; emitting it would send the agent loop a nameless tool.
		if call.Function.Name == "" && call.Function.Arguments == "" {
			continue
		}
		if call.Type == "" {
			call.Type = "function"
		}
		calls = append(calls, *call)
	}
	if len(calls) == 0 {
		return nil
	}
	return calls
}

// Len reports how many distinct calls have been seen so far.
func (a *toolCallAccumulator) Len() int { return len(a.order) }

// StreamChatWithTools runs a tool-aware streaming completion.
//
// onToken receives assistant text as it arrives. It is called only for text the
// provider sends as content, never for tool-call arguments, so a caller can
// render it directly.
//
// The returned ChatResult is shaped exactly like ChatWithUsage's, so the agent
// loop consumes a streamed turn and a non-streamed one identically.
//
// Callers must not display onToken text as final before this returns: a model
// that emits a tool call as plain text (observed on qwen2.5-coder via Ollama)
// streams that JSON as content, and it is only recognized as a call by
// parseFallbackToolCall once the stream is complete. When that happens the
// result's Content is cleared, and the caller is told to discard what it showed
// via the returned discardText flag.
func (c *Client) StreamChatWithTools(
	ctx context.Context,
	model string,
	messages []Message,
	tools []Tool,
	thinkLevel string,
	onToken func(string),
	onReasoning func(string),
) (result ChatResult, discardText bool, err error) {
	if model == "" {
		model = c.ChatModel
	}
	cr := chatRequest{
		Model:         model,
		Messages:      messages,
		Stream:        true,
		StreamOptions: &chatStreamOptions{IncludeUsage: true},
		Tools:         tools,
		Temperature:   c.Temperature,
		TopP:          c.TopP,
		MaxTokens:     c.MaxTokens,
	}
	if c.SendThink {
		cr.Think = thinkLevel
	}
	body, err := json.Marshal(cr)
	if err != nil {
		return ChatResult{}, false, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return ChatResult{}, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return ChatResult{}, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ChatResult{}, false, readChatError(resp)
	}
	rateLimit := parseRateLimitHeaders(resp.Header)

	var (
		text      strings.Builder
		reasoning strings.Builder
		usage     *Usage
		calls     = newToolCallAccumulator()
	)

	// Channel-framed models interleave private reasoning with the answer in one
	// stream. Buffering through the filter keeps that reasoning off the screen,
	// so the visible text matches what the non-streaming path would have shown.
	emitText := func(token string) {
		text.WriteString(token)
		if onToken != nil {
			onToken(token)
		}
	}
	emitReasoning := func(token string) {
		reasoning.WriteString(token)
		if onReasoning != nil {
			onReasoning(token)
		}
	}
	channelFilter := newChannelStreamFilter(emitText, emitReasoning)
	useChannelFilter := c.ChannelFraming

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk chatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // skip malformed/keepalive lines
		}
		if len(chunk.Choices) > 0 {
			choice := chunk.Choices[0]
			if choice.Delta.Content != "" {
				if useChannelFilter {
					channelFilter.Feed(choice.Delta.Content)
				} else {
					emitText(choice.Delta.Content)
				}
			}
			if choice.Delta.Reasoning != "" {
				emitReasoning(choice.Delta.Reasoning)
			}
			for _, delta := range choice.Delta.ToolCalls {
				calls.Add(delta)
			}
		}
		if chunk.Usage != nil {
			usage = &Usage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
				TotalTokens:      chunk.Usage.TotalTokens,
				RateLimit:        rateLimit,
			}
		}
	}
	if useChannelFilter {
		channelFilter.Flush()
	}
	if err := scanner.Err(); err != nil {
		return ChatResult{}, false, err
	}

	reply := Message{Role: "assistant", Content: text.String(), ToolCalls: calls.Calls()}
	if !useChannelFilter && HasChannelMarkers(reply.Content) {
		cleaned, _ := SplitChannelContent(reply.Content)
		if cleaned != reply.Content {
			reply.Content = cleaned
			discardText = true
		}
	}
	// Same salvage as the non-streaming path: a model that wrote its tool call
	// as prose has streamed that JSON to the caller as visible text, so the
	// caller is told to take it back off the screen.
	if len(reply.ToolCalls) == 0 && len(tools) > 0 {
		if call, ok := parseFallbackToolCall(reply.Content, tools); ok {
			reply.ToolCalls = []ToolCall{call}
			reply.Content = ""
			discardText = true
		}
	}

	result = ChatResult{Message: reply}
	if usage != nil && (usage.PromptTokens > 0 || usage.CompletionTokens > 0) {
		result.Usage = *usage
		result.HasUsage = true
	}
	return result, discardText, nil
}
