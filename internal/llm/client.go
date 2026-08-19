// Package llm provides a thin client for OpenAI-compatible chat/embedding
// APIs. It works unmodified against OpenAI, Ollama (localhost:11434/v1),
// LM Studio, and any other OpenAI-compatible server.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Message is fastllm's provider-agnostic chat message. Content stays a
// plain string for every existing call site (system prompts, tool
// results, loaded history) — Images is the only addition, and is empty
// for the overwhelming majority of messages. MarshalJSON below is what
// actually turns a message carrying images into the OpenAI-compatible
// wire format's content-block array; every other field here is untouched
// by that.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	Images     []Image    `json:"-"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// Image is one pasted/attached image, carried as a data URI end to end —
// the same representation the browser's FileReader.readAsDataURL already
// produces client-side, stored as-is in SQLite, and split back into
// media type + base64 payload only at the point each provider's wire
// format needs them (see anthropic.go/gemini.go's toAnthropicRequest/
// toGeminiRequest, and MarshalJSON below for the OpenAI-compatible path).
type Image struct {
	DataURI string `json:"data_uri"`
}

// splitDataURI pulls the media type and base64 payload out of a
// "data:<mediaType>;base64,<data>" string, for providers (Anthropic,
// Gemini) whose image content blocks want those two parts separately
// rather than a single URL string the way OpenAI's image_url block does.
// Returns ok=false for anything that isn't a well-formed base64 data URI.
func splitDataURI(dataURI string) (mediaType, data string, ok bool) {
	const prefix = "data:"
	if !strings.HasPrefix(dataURI, prefix) {
		return "", "", false
	}
	rest := dataURI[len(prefix):]
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return "", "", false
	}
	meta, payload := rest[:comma], rest[comma+1:]
	meta, isBase64 := strings.CutSuffix(meta, ";base64")
	if !isBase64 || meta == "" || payload == "" {
		return "", "", false
	}
	return meta, payload, true
}

// MarshalJSON emits the OpenAI-compatible wire format: a plain content
// string when there are no images (identical to this type's previous,
// pre-image-support JSON shape — every non-image call site is
// unaffected), or an array of {"type":"text"|"image_url",...} content
// blocks when there are, per OpenAI's (and Ollama's, and NVIDIA Build's)
// multimodal content-block convention. This only needs to exist once,
// here, rather than in every caller that marshals a chatRequest, since
// Go's encoding/json calls a type's own MarshalJSON automatically
// wherever that type appears — including nested in []Message.
func (m Message) MarshalJSON() ([]byte, error) {
	type alias Message // avoids infinite recursion into this MarshalJSON
	if len(m.Images) == 0 {
		return json.Marshal(struct {
			alias
			Content string `json:"content"`
		}{alias: alias(m), Content: m.Content})
	}
	blocks := make([]any, 0, len(m.Images)+1)
	if m.Content != "" {
		blocks = append(blocks, map[string]string{"type": "text", "text": m.Content})
	}
	for _, img := range m.Images {
		blocks = append(blocks, map[string]any{
			"type":      "image_url",
			"image_url": map[string]string{"url": img.DataURI},
		})
	}
	return json.Marshal(struct {
		alias
		Content []any `json:"content"`
	}{alias: alias(m), Content: blocks})
}

// Tool describes one function the model may call, in OpenAI's tool schema.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

// ToolCall is one function-call request from the model, as returned in a
// non-streaming completion's message.tool_calls.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type Client struct {
	BaseURL    string
	APIKey     string
	ChatModel  string
	EmbedModel string
	HTTPClient *http.Client
	// SendThink controls whether StreamChat/Chat include the "think"
	// field on outgoing requests — Ollama's own extension for picking a
	// reasoning effort level, not part of the OpenAI chat-completions
	// spec. Real OpenAI-wire-compatible cloud APIs (OpenAI itself, NVIDIA
	// Build) have no defined meaning for an unrecognized "think" field
	// and some (confirmed: NVIDIA Build, at least for some models) 400
	// the whole request rather than silently ignoring it — so this
	// defaults false and is only set true for the local Ollama client
	// (see New's caller in appserver.go), not the cloud ones New in
	// router.go's SetCloudProviders constructs.
	SendThink bool
}

func New(baseURL, apiKey, chatModel, embedModel string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		APIKey:     apiKey,
		ChatModel:  chatModel,
		EmbedModel: embedModel,
		HTTPClient: &http.Client{Timeout: 5 * time.Minute},
	}
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
	Tools    []Tool    `json:"tools,omitempty"`
	Think    string    `json:"think,omitempty"`
}

// Model describes one chat-capable model available on the backend.
type Model struct {
	Name              string   `json:"name"`
	Capabilities      []string `json:"capabilities,omitempty"`
	SupportsFileTools bool     `json:"supports_file_tools"`
	SupportsVision    bool     `json:"supports_vision"`
	// Provider is "anthropic"/"openai"/"gemini" for a cloud model, or ""
	// for a local Ollama model — the frontend model picker groups options
	// by this field instead of re-deriving it from Name's "provider:"
	// prefix with its own hardcoded copy of AnthropicPrefix/OpenAIPrefix/
	// GeminiPrefix, which would otherwise be a second place those prefixes
	// have to be kept in sync by hand.
	Provider string `json:"provider,omitempty"`
}

// OpenAIModels lists the models offered in the model picker when an
// OpenAI API key is configured. OpenAI's real hosted API is already
// wire-compatible with this file's Client (it's what "OpenAI-compatible"
// in the package doc comment refers to), so — unlike Anthropic/Gemini —
// no separate client type was needed; Router just points a second Client
// at api.openai.com. See AnthropicModels's doc comment for why this is a
// fixed list rather than a live query.
var OpenAIModels = []string{
	"gpt-5.1",
	"gpt-5.1-mini",
	"gpt-5.1-nano",
	"o3",
}

// NvidiaModels lists the models offered in the picker when an NVIDIA
// Build API key is configured. This is deliberately a hand-verified
// allowlist, not a live query against NVIDIA's own GET /v1/models: that
// endpoint lists NVIDIA Build's entire public catalog (~102 models as of
// the sweep below) regardless of whether the configured account is
// actually entitled to call each one — in a real sweep against a live
// account, ~78 of those 102 404'd with "Function ... Not found for
// account" the moment a real chat completion was attempted, so showing
// the live list in the picker just means most entries fail on click.
// Only models confirmed, via an actual chat completion (not just a
// listing or a docs page), to respond successfully are listed here.
//
// This list has already gone stale twice from trusting docs alone
// instead of a live call — mistralai/mistral-large-2-instruct and
// qwen/qwen2.5-coder-32b-instruct both went dead, and even their
// hand-verified "current successor" replacements (mistralai/mistral-
// large-3-675b-instruct-2512, qwen/qwen3-coder-480b-a35b-instruct) were
// gone within about a day — NVIDIA's catalog and this account's
// entitlements both churn faster than any docs snapshot stays accurate.
// Do not add a model here on the strength of a docs page; only add one
// after it returns a real, successful chat completion for an actual
// account. If an entry here starts failing, re-run the same kind of
// sweep (query GET /v1/models for the current catalog, then Chat each
// entry) before editing this list by hand again.
//
// Last verified 2026-08-18 against a real account: every model below
// returned a successful chat completion; no Qwen coder model was
// entitled at all for that account (all 404'd), which is why there's no
// coding-specialist entry here despite one existing in NVIDIA's catalog.
// meta/llama-3.2-11b-vision-instruct is the only one of these confirmed
// in that same sweep to accept image input — see SupportsVisionForModel
// and nvidiaVisionModels.
//
// Trimmed 2026-08-19 at the user's request from the full 11-model
// verified set down to one representative per distinct family/use-case,
// to cut down redundant near-duplicates in the picker (not a failure —
// every dropped model still worked): llama-3.1-8b-instruct and
// llama-3.1-70b-instruct were dropped as redundant with the newer
// llama-3.3-70b-instruct; nemotron-3-super-120b-a12b and
// llama-3.3-nemotron-super-49b-v1 were dropped as two more Nemotron sizes
// in between the nano/ultra ends already kept below. If any of these were
// wanted back, they're known-good as of the sweep above.
var NvidiaModels = []string{
	"meta/llama-3.3-70b-instruct",
	"meta/llama-3.2-11b-vision-instruct",
	"mistralai/mistral-nemotron",
	"nvidia/nemotron-3-nano-30b-a3b",
	"nvidia/nemotron-3-ultra-550b-a55b",
	"openai/gpt-oss-120b",
	"z-ai/glm-5.2",
}

// CloudflareModels lists the models offered in the picker when a
// Cloudflare Workers AI API token + account ID are configured. UNLIKE
// NvidiaModels and OpenAIModels above, this list is NOT yet hand-verified
// against a real account via an actual chat completion — it's seeded from
// Cloudflare's own docs (developers.cloudflare.com/workers-ai/models/) at
// setup time. Workers AI ships new models weekly and retires old ones
// without notice per Cloudflare's own docs, so treat every entry here as
// provisional until it's been confirmed with a real successful chat
// completion, the same way NvidiaModels' doc comment describes NVIDIA
// Build's catalog going stale twice from trusting docs alone. If an entry
// here 404s or errors, remove it; don't add a new one without testing it
// first.
var CloudflareModels = []string{
	"@cf/meta/llama-3.3-70b-instruct-fp8-fast",
	// llama-3.1-8b-instruct-fast and llama-3.2-3b-instruct removed
	// 2026-08-19 at the user's request (keeping the picker to just the
	// 70B Llama and gpt-5.6-luna below) — not a failure, just trimming
	// unwanted options.
	//
	// deepseek-v4-flash-0731 removed 2026-08-19: confirmed unusable on the
	// account's current plan tier (needs Workers Paid or prepaid AI
	// Gateway credits, per Cloudflare's own docs). Don't re-add unless a
	// real chat completion succeeds against an account actually entitled
	// to call it.
	//
	// gpt-5.6-luna only speaks Cloudflare's Responses API, not Chat
	// Completions — confirmed via two failed attempts against Chat
	// Completions (2026-08-19): "@cf/openai/gpt-5.6-luna" 400'd "No such
	// model" (code 5007), and the bare "openai/gpt-5.6-luna" form 400'd
	// "Invalid value at input" (code 7003) — the model was found but
	// rejected the {"messages": [...]} body shape. Routed through
	// CloudflareResponsesClient instead (see NeedsResponsesAPI in
	// cloudflare_responses.go and responsesOnlyModels there) using this
	// same bare "openai/gpt-5.6-luna" ID, which matches the ID Cloudflare's
	// own docs show for the Responses API.
	"openai/gpt-5.6-luna",
}

type tagsResponse struct {
	Models []struct {
		Name         string   `json:"name"`
		Capabilities []string `json:"capabilities"`
	} `json:"models"`
}

// ListModels queries Ollama's native /api/tags endpoint and returns every
// model that is not embedding-only. It relies on the Ollama-specific
// endpoint, so it only works when BaseURL points at an Ollama server
// (returns an error otherwise, which callers should treat as "unavailable").
func (c *Client) ListModels(ctx context.Context) ([]Model, error) {
	root := strings.TrimSuffix(c.BaseURL, "/v1")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, root+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("llm: list models failed: %s", resp.Status)
	}

	var tr tagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, err
	}

	out := make([]Model, 0, len(tr.Models))
	for _, m := range tr.Models {
		if containsString(m.Capabilities, "embedding") && !containsString(m.Capabilities, "completion") {
			continue // embedding-only model, not usable for chat
		}
		out = append(out, Model{Name: m.Name, Capabilities: m.Capabilities, SupportsFileTools: SupportsTools(m.Name), SupportsVision: SupportsVisionForModel(m.Name)})
	}
	return out, nil
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// toolCapableModelPrefixes lists the model families confirmed (by hand)
// to reliably populate Ollama's tool_calls field rather than writing tool
// invocations as plain-text JSON content. Ollama's own "tools" capability
// flag is not sufficient evidence: qwen2.5-coder:7b advertises "tools" in
// /api/tags but consistently ignores tool_calls in practice (see
// parseFallbackToolCall) — so file read/write access is only offered to
// models on this allowlist, matched by name prefix (e.g. "gemma4" matches
// "gemma4:12b").
//
// "qwen3.5" added 2026-08-19 after verifying qwen3.5:9b directly against
// /v1/chat/completions: a read_file request returned real
// finish_reason:"tool_calls" with correctly-shaped arguments, and a
// second request simulating the tool-result round-trip correctly
// consumed the result and gave a clean final answer (no redundant re-
// calls). yi-coder:9b was checked the same way and rejected outright by
// Ollama ("does not support tools") — it has no tools capability at all
// (confirmed via /api/tags: capabilities is ["completion"] only) and is
// deliberately NOT on this list.
var toolCapableModelPrefixes = []string{
	"gemma4",
	"gpt-oss",
	"qwen3.5",
}

// SupportsToolsForModel reports whether model (bare local name, or a
// "provider:"-prefixed cloud model — see Router) can be offered file
// read/write tools at all. Cloud providers each need their own tool-call
// wire-format translation (see e.g. GeminiClient.Chat) — implemented for
// Gemini, OpenAI, and NVIDIA Build (OpenAI's and NVIDIA Build's hosted
// APIs both already speak Client's tool format natively) but not yet
// Anthropic, so an "anthropic:" model is excluded here even though Claude
// models are generally excellent at tool use — this is a "not
// implemented in fastllm yet" gate, not a judgment about the model.
// Unlike local models (see SupportsTools's allowlist below), a
// configured cloud provider needs no per-model allowlist: the
// uncertainty SupportsTools guards against is whether a given local
// model reliably emits real tool_calls at all, which doesn't apply to
// hosted providers fastllm has implemented tool support for. NVIDIA
// Build's own docs note tool-calling support varies per model on their
// platform (e.g. confirmed present on the Llama 3.2 Vision models,
// confirmed absent on DeepSeek-R1-Distill) — offering it at the whole-
// provider level here is optimistic for NvidiaModels as a set; verify
// each listed model actually returns real tool_calls before trusting
// this blindly, same caveat as NvidiaModels' own doc comment.
//
// Cloudflare is a partial exception to the "whole provider" rule above —
// tool support is allowlisted per model, not for the whole provider, via
// cloudflareToolCapableModels below.
func SupportsToolsForModel(model string) bool {
	if bare, provider, ok := stripProviderPrefix(model); ok {
		if provider == "cloudflare" {
			return cloudflareToolCapableModels[bare]
		}
		return provider == "gemini" || provider == "openai" || provider == "nvidia"
	}
	return SupportsTools(model)
}

// cloudflareToolCapableModels lists which CloudflareModels entries are
// confirmed, via a real tool-calling request against a live account, to
// reliably return proper structured tool_calls rather than writing the
// call out as plain text.
//
//   - "@cf/meta/llama-3.3-70b-instruct-fp8-fast": confirmed 2026-08-19 —
//     a real read_file request returned finish_reason:"tool_calls" with
//     correctly-shaped function.arguments on every round (4 rounds, one
//     redundant re-read of the same file each time — a model-behavior
//     quirk bounded by maxToolRounds, not a wiring problem — but every
//     round used real tool_calls, never the parseFallbackToolCall path).
//   - "openai/gpt-5.6-luna": tool-capable via the Responses API, not this
//     Chat-Completions path — see NeedsResponsesAPI/
//     CloudflareResponsesClient.Chat instead; not listed here.
//
// Every other CloudflareModels entry is unverified and stays excluded
// until checked the same way, same caution as NvidiaModels' own doc
// comment.
var cloudflareToolCapableModels = map[string]bool{
	"@cf/meta/llama-3.3-70b-instruct-fp8-fast": true,
}

// SupportsTools reports whether model is on the allowlist of models
// confirmed to reliably use tool_calls, so callers can decide whether to
// offer file read/write tools at all.
func SupportsTools(model string) bool {
	for _, prefix := range toolCapableModelPrefixes {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

// visionCapableModelPrefixes lists the local Ollama model families known
// to accept image input, matched by name prefix the same way
// toolCapableModelPrefixes gates tool support — Ollama's /api/tags
// "capabilities" field does actually include "vision" for these, but the
// prefix list is kept as a second, explicit source of truth for the same
// reason toolCapableModelPrefixes exists: a hand-maintained list is easy
// to reason about and doesn't silently change behavior if a future Ollama
// version starts reporting capabilities differently.
var visionCapableModelPrefixes = []string{
	"llava",
	"llama3.2-vision",
	"qwen2.5vl",
	"qwen3-vl",
	"minicpm-v",
	"moondream",
	"bakllava",
	"gemma3", // Gemma 3 (and later Gemma releases run locally) are multimodal
	"gemma4",
}

// nvidiaVisionModels lists which of NvidiaModels are confirmed to accept
// image input — unlike Anthropic/OpenAI/Gemini, NVIDIA Build's vision
// support isn't uniform across every model fastllm offers for that
// provider (most of NvidiaModels are text-only), so this needs a
// per-model list rather than a single provider-wide bool the way
// SupportsVisionForModel handles the other three cloud providers.
// meta/llama-3.2-11b-vision-instruct was confirmed live (a real chat
// completion succeeded, ~8s) in the 2026-08-18 sweep documented on
// NvidiaModels; the previously-listed 90b-vision variant timed out in
// that same sweep and isn't in the live catalog for this account, so
// it's been dropped here rather than left as an unverified claim.
var nvidiaVisionModels = map[string]bool{
	"meta/llama-3.2-11b-vision-instruct": true,
}

// SupportsVisionForModel reports whether model (bare local name, or a
// "provider:"-prefixed cloud model) can accept image input at all — the
// gate the chat composer uses to decide whether pasting/attaching an
// image is even offered for the currently selected model. Cloud: every
// current Anthropic/OpenAI/Gemini model family fastllm offers supports
// vision; NVIDIA Build is per-model (see nvidiaVisionModels) since most
// models on that platform are text-only. Local: matched against
// visionCapableModelPrefixes, mirroring SupportsToolsForModel/
// SupportsTools's local-model gating.
func SupportsVisionForModel(model string) bool {
	if bare, provider, ok := stripProviderPrefix(model); ok {
		switch provider {
		case "anthropic", "openai", "gemini":
			return true
		case "nvidia":
			return nvidiaVisionModels[bare]
		}
		return false
	}
	for _, prefix := range visionCapableModelPrefixes {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

// openAIErrorBody is the standard OpenAI-compatible error envelope
// ({"error": {"message": ..., ...}}) — used by OpenAI itself, and (per
// this file's package doc comment) by every other OpenAI-wire-compatible
// backend this Client talks to (Ollama, NVIDIA Build). A non-200
// response's body is the one place a caller actually finds out *why* a
// request was rejected (bad model ID, an unsupported/unrecognized
// parameter, a malformed request) rather than just that it was — see
// StreamChat/Chat below, which previously discarded the body entirely
// and surfaced only the bare HTTP status line.
type openAIErrorBody struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// readChatError extracts a useful message from a non-200 chat completion
// response, preferring the provider's own structured error message (see
// openAIErrorBody) over the bare HTTP status line, and falling back to
// the raw response body (capped) if it doesn't parse as that shape —
// some OpenAI-compatible backends return a plain-text or differently-
// shaped error body instead.
func readChatError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var eb openAIErrorBody
	if json.Unmarshal(body, &eb) == nil && eb.Error.Message != "" {
		return fmt.Errorf("llm: chat completion failed: %s", eb.Error.Message)
	}
	if trimmed := strings.TrimSpace(string(body)); trimmed != "" {
		return fmt.Errorf("llm: chat completion failed: %s: %s", resp.Status, trimmed)
	}
	return fmt.Errorf("llm: chat completion failed: %s", resp.Status)
}

type chatStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			Reasoning string `json:"reasoning"`
		} `json:"delta"`
	} `json:"choices"`
}

// StreamChat sends messages to the chat completion endpoint and calls
// onToken for every incremental piece of answer text, and onReasoning (if
// non-nil) for every incremental piece of a thinking-capable model's
// reasoning trace, as they stream in. Ollama's OpenAI-compatible endpoint
// sends reasoning as a "reasoning" delta field alongside "content", ahead
// of and separate from the actual answer; models without thinking support
// simply never populate it. If model is empty, c.ChatModel is used.
func (c *Client) StreamChat(ctx context.Context, model string, messages []Message, thinkLevel string, onToken func(string), onReasoning func(string)) error {
	if model == "" {
		model = c.ChatModel
	}
	cr := chatRequest{Model: model, Messages: messages, Stream: true}
	if c.SendThink {
		cr.Think = thinkLevel
	}
	body, err := json.Marshal(cr)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return readChatError(resp)
	}

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
			if chunk.Choices[0].Delta.Content != "" {
				onToken(chunk.Choices[0].Delta.Content)
			}
			if chunk.Choices[0].Delta.Reasoning != "" && onReasoning != nil {
				onReasoning(chunk.Choices[0].Delta.Reasoning)
			}
		}
	}
	return scanner.Err()
}

type chatResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
}

// Chat sends a single non-streaming completion request with the given
// tools declared, and returns the model's reply message — which may
// contain ToolCalls instead of (or in addition to) Content if the model
// wants to invoke a tool. Used as a pre-flight step before StreamChat so
// tool calls (which arrive as accumulated JSON, awkward to stream) are
// resolved before the user-facing streamed answer begins. If model is
// empty, c.ChatModel is used.
func (c *Client) Chat(ctx context.Context, model string, messages []Message, tools []Tool, thinkLevel string) (Message, error) {
	if model == "" {
		model = c.ChatModel
	}
	creq := chatRequest{Model: model, Messages: messages, Stream: false, Tools: tools}
	if c.SendThink {
		creq.Think = thinkLevel
	}
	body, err := json.Marshal(creq)
	if err != nil {
		return Message{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Message{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return Message{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Message{}, readChatError(resp)
	}

	var cr chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return Message{}, err
	}
	if len(cr.Choices) == 0 {
		return Message{}, fmt.Errorf("llm: empty response")
	}

	reply := cr.Choices[0].Message
	if len(reply.ToolCalls) == 0 && len(tools) > 0 {
		if call, ok := parseFallbackToolCall(reply.Content, tools); ok {
			reply.ToolCalls = []ToolCall{call}
			reply.Content = ""
		}
	}
	return reply, nil
}

// parseFallbackToolCall recovers a tool call from models that don't
// support Ollama's tool_calls field and instead write the call out as
// plain message content — e.g. qwen2.5-coder:7b reliably does this
// despite advertising the "tools" capability. Deliberately strict to
// avoid misfiring on a model legitimately discussing JSON in an answer:
// the ENTIRE trimmed content (nothing before or after, no prose, no code
// fences) must parse as exactly {"name": "...", "arguments": {...}},
// and name must match one of the tools actually offered in this request.
func parseFallbackToolCall(content string, tools []Tool) (ToolCall, bool) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" || trimmed[0] != '{' {
		return ToolCall{}, false
	}

	var parsed struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	if err := dec.Decode(&parsed); err != nil || parsed.Name == "" || len(parsed.Arguments) == 0 {
		return ToolCall{}, false
	}
	// Reject trailing content after the JSON object — a model just
	// mentioning a tool-call-shaped example mid-explanation would still
	// fail this because real answers practically never end with nothing
	// but a bare JSON object and no closing remarks.
	if dec.More() {
		return ToolCall{}, false
	}

	known := false
	for _, t := range tools {
		if t.Function.Name == parsed.Name {
			known = true
			break
		}
	}
	if !known {
		return ToolCall{}, false
	}

	return ToolCall{
		ID:   "fallback_" + parsed.Name,
		Type: "function",
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: parsed.Name, Arguments: string(parsed.Arguments)},
	}, true
}

type embedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type embedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed returns the embedding vector for a single piece of text.
func (c *Client) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(embedRequest{Model: c.EmbedModel, Input: text})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("llm: embedding request failed: %s", resp.Status)
	}

	var er embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, err
	}
	if len(er.Data) == 0 {
		return nil, fmt.Errorf("llm: empty embedding response")
	}
	return er.Data[0].Embedding, nil
}
