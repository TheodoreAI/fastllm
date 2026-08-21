package chat

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"

	"fastllm/internal/llm"
	"fastllm/internal/store"
)

// LiveEvent is one update pushed to every subscriber of Handler.Live —
// see ExternalChatCompletions (the /v1/chat/completions proxy an
// external tool, like a terminal-based AI CLI, can be pointed at) and
// LiveStream (the SSE endpoint the frontend subscribes to so the Chat
// panel can render that traffic as it happens, not just after a reload).
type LiveEvent struct {
	ConversationID int64  `json:"conversation_id"`
	Type           string `json:"type"` // "user_message" | "token" | "done"
	Text           string `json:"text,omitempty"`
}

// LiveBroadcaster fans a LiveEvent out to every currently-subscribed SSE
// connection. There's normally at most one subscriber (the one open Chat
// panel), but nothing here assumes that — multiple browser tabs/windows
// would each get their own channel and see the same events.
type LiveBroadcaster struct {
	mu   sync.Mutex
	subs map[chan LiveEvent]struct{}
}

func NewLiveBroadcaster() *LiveBroadcaster {
	return &LiveBroadcaster{subs: make(map[chan LiveEvent]struct{})}
}

// Subscribe registers a new listener and returns its channel plus an
// unsubscribe func the caller must run (deferred) when done listening.
// Buffered so a slow-reading subscriber doesn't stall Publish; Publish
// drops the event for that subscriber (rather than blocking) if the
// buffer is ever actually full.
func (b *LiveBroadcaster) Subscribe() (chan LiveEvent, func()) {
	ch := make(chan LiveEvent, 64)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		close(ch)
		b.mu.Unlock()
	}
}

func (b *LiveBroadcaster) Publish(evt LiveEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- evt:
		default:
		}
	}
}

// LiveStream is the SSE endpoint the Chat panel subscribes to on mount so
// it can render an externally-driven conversation (see
// ExternalChatCompletions) live, the same way it renders its own
// composer-driven streaming, instead of having to poll.
func (h *Handler) LiveStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	ch, unsubscribe := h.Live.Subscribe()
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			payload, _ := json.Marshal(evt)
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
	}
}

const liveTerminalTitle = "Live Terminal"

// liveConversationID returns the id of the single, always-reused
// conversation external chat-completion traffic (see
// ExternalChatCompletions/AnthropicMessages) gets appended to. Cached
// in-memory after the first lookup per process run, but that lookup
// checks the DB for an existing "Live Terminal" conversation before
// creating a new one — without this, every process restart (a `wails
// dev` reload, an app relaunch) would spawn a fresh duplicate instead of
// picking the previous session's back up.
func (h *Handler) liveConversationID() (int64, error) {
	h.liveConvMu.Lock()
	defer h.liveConvMu.Unlock()
	if h.liveConvID != 0 {
		return h.liveConvID, nil
	}
	existing, err := store.ListConversations(h.DB, defaultWorkspace)
	if err != nil {
		return 0, err
	}
	for _, c := range existing {
		if c.Title == liveTerminalTitle {
			h.liveConvID = c.ID
			return c.ID, nil
		}
	}
	id, err := store.CreateConversation(h.DB, defaultWorkspace, liveTerminalTitle)
	if err != nil {
		return 0, err
	}
	h.liveConvID = id
	return id, nil
}

type externalChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type externalChatRequest struct {
	Model    string                `json:"model"`
	Messages []externalChatMessage `json:"messages"`
	Stream   bool                  `json:"stream"`
}

// ExternalChatCompletions is a minimal OpenAI-compatible
// /v1/chat/completions endpoint — point an external tool's API base URL
// (e.g. a terminal-based AI CLI's OPENAI_BASE_URL/--api-base) at fastllm
// instead of straight at the local model server, and every turn is both
// answered normally *and* saved + broadcast to the "Live Terminal"
// conversation (see liveConversationID/LiveBroadcaster) so the Chat panel
// can show it happening in real time. Only the last message with role
// "user" is treated as this turn's new input — a stateless chat-
// completion client resends its full history on every call, and
// everything earlier either was already saved by a previous call to this
// same endpoint, or is the external tool's own system prompt, which this
// endpoint doesn't store.
func (h *Handler) ExternalChatCompletions(w http.ResponseWriter, r *http.Request) {
	var req externalChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	var userText string
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			userText = req.Messages[i].Content
			break
		}
	}
	if strings.TrimSpace(userText) == "" {
		http.Error(w, "no user message found", http.StatusBadRequest)
		return
	}

	convID, err := h.liveConversationID()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := store.SaveMessage(h.DB, defaultWorkspace, convID, "user", userText, nil); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.Live.Publish(LiveEvent{ConversationID: convID, Type: "user_message", Text: userText})

	llmMessages := make([]llm.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		llmMessages = append(llmMessages, llm.Message{Role: m.Role, Content: m.Content})
	}

	ctx := r.Context()
	model := req.Model
	if model == "" {
		model = h.LLM.ChatModel()
	}

	if !req.Stream {
		var full strings.Builder
		err := h.LLM.StreamChat(ctx, model, llmMessages, "", func(token string) {
			full.WriteString(token)
			h.Live.Publish(LiveEvent{ConversationID: convID, Type: "token", Text: token})
		}, func(string) {}, func(llm.Usage) {})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := store.SaveAssistantMessage(h.DB, defaultWorkspace, convID, full.String(), nil); err != nil {
			log.Printf("save external assistant message: %v", err)
		}
		h.Live.Publish(LiveEvent{ConversationID: convID, Type: "done"})
		writeOpenAIResponse(w, model, full.String())
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	var full strings.Builder
	err = h.LLM.StreamChat(ctx, model, llmMessages, "", func(token string) {
		full.WriteString(token)
		h.Live.Publish(LiveEvent{ConversationID: convID, Type: "token", Text: token})
		writeOpenAIChunk(w, model, token)
		flusher.Flush()
	}, func(string) {}, func(llm.Usage) {})
	if err != nil {
		// Best-effort: an external CLI's mid-stream error still gets
		// whatever partial answer was generated saved, same as the
		// composer-driven /api/chat handler does for a client-stopped
		// stream (see Chat above).
		log.Printf("external chat stream: %v", err)
	}
	if _, err := store.SaveAssistantMessage(h.DB, defaultWorkspace, convID, full.String(), nil); err != nil {
		log.Printf("save external assistant message: %v", err)
	}
	h.Live.Publish(LiveEvent{ConversationID: convID, Type: "done"})
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func writeOpenAIChunk(w http.ResponseWriter, model, token string) {
	chunk := map[string]any{
		"object": "chat.completion.chunk",
		"model":  model,
		"choices": []map[string]any{
			{"index": 0, "delta": map[string]string{"content": token}},
		},
	}
	payload, _ := json.Marshal(chunk)
	fmt.Fprintf(w, "data: %s\n\n", payload)
}

func writeOpenAIResponse(w http.ResponseWriter, model, content string) {
	resp := map[string]any{
		"object": "chat.completion",
		"model":  model,
		"choices": []map[string]any{
			{"index": 0, "message": map[string]string{"role": "assistant", "content": content}, "finish_reason": "stop"},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// ExternalModels is a minimal OpenAI-compatible GET /v1/models — some CLI
// tools probe this before their first chat completion call to validate
// the configured base URL and/or populate a model picker of their own.
func (h *Handler) ExternalModels(w http.ResponseWriter, r *http.Request) {
	models, err := h.LLM.ListModels(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := make([]map[string]any, 0, len(models))
	for _, m := range models {
		data = append(data, map[string]any{"id": m.Name, "object": "model"})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

// anthropicContentBlock is only ever read for its "text" blocks here — a
// real Messages API request can carry images/tool_use/tool_result blocks
// too, but Claude Code (the one real-world client this exists for) sends
// plain text turns for a normal chat, and anything else is silently
// dropped rather than rejected, same tradeoff ExternalChatCompletions
// makes for the OpenAI shape.
type anthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// anthropicMessage's Content is a `json.RawMessage` because the wire
// format allows either a bare string or an array of content blocks for
// the same field — see anthropicMessageText below for how it's resolved.
type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	Messages  []anthropicMessage `json:"messages"`
	Stream    bool               `json:"stream"`
}

// anthropicMessageText resolves a message's Content into plain text,
// handling both wire shapes: a bare JSON string, or an array of content
// blocks (concatenating every "text" block's Text field — the only block
// type this minimal proxy understands).
func anthropicMessageText(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []anthropicContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var b strings.Builder
	for _, block := range blocks {
		if block.Type == "text" {
			b.WriteString(block.Text)
		}
	}
	return b.String()
}

// AnthropicMessages is a minimal Anthropic Messages API-compatible
// POST /v1/messages — unlike ExternalChatCompletions (the OpenAI-shaped
// proxy CLI tools reading OPENAI_BASE_URL use), this is what Claude Code
// itself needs: it only ever speaks the Anthropic wire format
// (ANTHROPIC_BASE_URL), never OpenAI's. Same idea otherwise: forwards to
// Router.StreamChat (so the model field can route to a cloud provider
// exactly the same way ExternalChatCompletions does, not just the local
// model), and saves + broadcasts to the same "Live Terminal" conversation
// both proxies share, so either bridge shows up in the same place.
func (h *Handler) AnthropicMessages(w http.ResponseWriter, r *http.Request) {
	var req anthropicRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	var userText string
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			userText = anthropicMessageText(req.Messages[i].Content)
			break
		}
	}
	if strings.TrimSpace(userText) == "" {
		http.Error(w, "no user message found", http.StatusBadRequest)
		return
	}

	convID, err := h.liveConversationID()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := store.SaveMessage(h.DB, defaultWorkspace, convID, "user", userText, nil); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.Live.Publish(LiveEvent{ConversationID: convID, Type: "user_message", Text: userText})

	llmMessages := make([]llm.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		llmMessages = append(llmMessages, llm.Message{Role: m.Role, Content: anthropicMessageText(m.Content)})
	}

	ctx := r.Context()
	model := req.Model
	if model == "" {
		model = h.LLM.ChatModel()
	}
	msgID := "msg_" + newWriteID()

	if !req.Stream {
		var full strings.Builder
		var usage llm.Usage
		err := h.LLM.StreamChat(ctx, model, llmMessages, "", func(token string) {
			full.WriteString(token)
			h.Live.Publish(LiveEvent{ConversationID: convID, Type: "token", Text: token})
		}, func(string) {}, func(u llm.Usage) { usage = u })
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := store.SaveAssistantMessage(h.DB, defaultWorkspace, convID, full.String(), nil); err != nil {
			log.Printf("save external assistant message: %v", err)
		}
		h.Live.Publish(LiveEvent{ConversationID: convID, Type: "done"})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":            msgID,
			"type":          "message",
			"role":          "assistant",
			"model":         model,
			"content":       []map[string]string{{"type": "text", "text": full.String()}},
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
			"usage":         map[string]int{"input_tokens": usage.PromptTokens, "output_tokens": usage.CompletionTokens},
		})
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	writeSSE := func(eventType string, payload any) {
		data, _ := json.Marshal(payload)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, data)
		flusher.Flush()
	}

	writeSSE("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": msgID, "type": "message", "role": "assistant", "content": []any{},
			"model": model, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]int{"input_tokens": 0, "output_tokens": 0},
		},
	})
	writeSSE("content_block_start", map[string]any{
		"type": "content_block_start", "index": 0,
		"content_block": map[string]string{"type": "text", "text": ""},
	})

	var full strings.Builder
	var usage llm.Usage
	err = h.LLM.StreamChat(ctx, model, llmMessages, "", func(token string) {
		full.WriteString(token)
		h.Live.Publish(LiveEvent{ConversationID: convID, Type: "token", Text: token})
		writeSSE("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]string{"type": "text_delta", "text": token},
		})
	}, func(string) {}, func(u llm.Usage) { usage = u })
	if err != nil {
		// Best-effort, same reasoning as ExternalChatCompletions: still
		// save and close out the stream with whatever partial answer was
		// generated rather than leaving the client's stream half-open.
		log.Printf("anthropic messages stream: %v", err)
	}
	if _, err := store.SaveAssistantMessage(h.DB, defaultWorkspace, convID, full.String(), nil); err != nil {
		log.Printf("save external assistant message: %v", err)
	}
	h.Live.Publish(LiveEvent{ConversationID: convID, Type: "done"})

	writeSSE("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	writeSSE("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": map[string]int{"output_tokens": usage.CompletionTokens},
	})
	writeSSE("message_stop", map[string]any{"type": "message_stop"})
}
