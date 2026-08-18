package llm

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Cloud model names are prefixed so Router can tell at a glance which
// provider a model belongs to without keeping a separate lookup table
// that could drift out of sync with the model lists below — the prefix
// IS the routing key. Local Ollama models keep their bare names (e.g.
// "llama3.1"), unchanged from before cloud providers existed, so existing
// settings/history referencing a bare model name keep working.
const (
	AnthropicPrefix = "anthropic:"
	OpenAIPrefix    = "openai:"
	GeminiPrefix    = "gemini:"
)

// CloudProviderConfig is the subset of store.CloudProviderSettings the
// llm package needs — declared here instead of importing internal/store
// to avoid a store<->llm import cycle (store already has no reason to
// import llm, and shouldn't gain one just for this).
type CloudProviderConfig struct {
	AnthropicAPIKey string
	OpenAIAPIKey    string
	GeminiAPIKey    string
}

// cloudClients bundles the three optional cloud clients so Router can
// swap all of them out atomically under one lock (see
// Router.SetCloudProviders) instead of three separately-locked fields,
// which could otherwise let a concurrent StreamChat see one provider from
// the old settings and another from the new mid-update.
type cloudClients struct {
	anthropic *AnthropicClient
	openai    *Client // OpenAI's real API is wire-compatible with Client
	gemini    *GeminiClient
}

// Router dispatches chat requests to the local OpenAI-compatible backend
// or to whichever cloud provider a model's name prefix identifies,
// presenting the same StreamChat/Chat/ListModels surface as a bare
// *Client so internal/chat.Handler doesn't need to know which case it's
// in. Router is a long-lived pointer held by chat.Handler — cloud API
// keys are updated in place via SetCloudProviders (guarded by mu) rather
// than by handler.go swapping in a whole new *Router, so concurrent
// in-flight requests never race a plain field write/read.
type Router struct {
	Local *Client

	mu     sync.RWMutex
	clouds cloudClients
}

// NewRouter builds a Router around the given local client, with the given
// cloud provider config applied immediately (see SetCloudProviders).
func NewRouter(local *Client, cloud CloudProviderConfig) *Router {
	r := &Router{Local: local}
	r.SetCloudProviders(cloud)
	return r
}

// SetCloudProviders rebuilds the set of configured cloud clients from
// scratch and swaps them in atomically, constructing a cloud client for
// each provider that has a non-empty API key. A provider with no key
// stays nil, and ListModels leaves its models out of the combined list
// entirely (see that method). Called at startup (via NewRouter) and again
// every time Settings → Cloud providers is saved, so a newly-entered key
// is usable for the very next chat message without a server restart.
func (r *Router) SetCloudProviders(cloud CloudProviderConfig) {
	var next cloudClients
	if cloud.AnthropicAPIKey != "" {
		next.anthropic = NewAnthropicClient(cloud.AnthropicAPIKey)
	}
	if cloud.OpenAIAPIKey != "" {
		next.openai = New("https://api.openai.com/v1", cloud.OpenAIAPIKey, "", "")
	}
	if cloud.GeminiAPIKey != "" {
		next.gemini = NewGeminiClient(cloud.GeminiAPIKey)
	}
	r.mu.Lock()
	r.clouds = next
	r.mu.Unlock()
}

// stripProviderPrefix returns the bare model ID Anthropic/OpenAI/Gemini
// actually expect (their APIs know nothing about fastllm's "anthropic:"
// picker prefix) along with which provider it identified, or ("", "",
// false) for a local/unprefixed model name.
func stripProviderPrefix(model string) (bare, provider string, ok bool) {
	switch {
	case strings.HasPrefix(model, AnthropicPrefix):
		return strings.TrimPrefix(model, AnthropicPrefix), "anthropic", true
	case strings.HasPrefix(model, OpenAIPrefix):
		return strings.TrimPrefix(model, OpenAIPrefix), "openai", true
	case strings.HasPrefix(model, GeminiPrefix):
		return strings.TrimPrefix(model, GeminiPrefix), "gemini", true
	default:
		return "", "", false
	}
}

func (r *Router) StreamChat(ctx context.Context, model string, messages []Message, thinkLevel string, onToken func(string), onReasoning func(string)) error {
	bare, provider, ok := stripProviderPrefix(model)
	if !ok {
		return r.Local.StreamChat(ctx, model, messages, thinkLevel, onToken, onReasoning)
	}

	r.mu.RLock()
	clouds := r.clouds
	r.mu.RUnlock()

	switch provider {
	case "anthropic":
		if clouds.anthropic == nil {
			return fmt.Errorf("llm: Anthropic isn't configured — add an API key in Settings → Cloud providers")
		}
		return clouds.anthropic.StreamChat(ctx, bare, messages, thinkLevel, onToken, onReasoning)
	case "openai":
		if clouds.openai == nil {
			return fmt.Errorf("llm: OpenAI isn't configured — add an API key in Settings → Cloud providers")
		}
		return clouds.openai.StreamChat(ctx, bare, messages, thinkLevel, onToken, onReasoning)
	case "gemini":
		if clouds.gemini == nil {
			return fmt.Errorf("llm: Gemini isn't configured — add an API key in Settings → Cloud providers")
		}
		return clouds.gemini.StreamChat(ctx, bare, messages, thinkLevel, onToken, onReasoning)
	}
	return fmt.Errorf("llm: unknown provider for model %q", model)
}

// Chat is the non-streaming, tool-capable half of the interface (see
// StreamChat above) — internal/chat.Handler's runFileTools loop is the
// only caller, used as a pre-flight step before the user-facing answer
// streams. OpenAI's real hosted API already speaks the same tool-call
// wire format Client.Chat implements (see OpenAIModels's doc comment), so
// it's routed straight there with no translation needed, same as
// StreamChat does. Anthropic tool-calling isn't implemented yet — see
// llm.SupportsToolsForModel, which is what keeps internal/chat.Handler
// from ever reaching this case for an "anthropic:" model in practice;
// the error here is a backstop, not the primary gate.
func (r *Router) Chat(ctx context.Context, model string, messages []Message, tools []Tool, thinkLevel string) (Message, error) {
	bare, provider, ok := stripProviderPrefix(model)
	if !ok {
		return r.Local.Chat(ctx, model, messages, tools, thinkLevel)
	}

	r.mu.RLock()
	clouds := r.clouds
	r.mu.RUnlock()

	switch provider {
	case "openai":
		if clouds.openai == nil {
			return Message{}, fmt.Errorf("llm: OpenAI isn't configured — add an API key in Settings → Cloud providers")
		}
		return clouds.openai.Chat(ctx, bare, messages, tools, thinkLevel)
	case "gemini":
		if clouds.gemini == nil {
			return Message{}, fmt.Errorf("llm: Gemini isn't configured — add an API key in Settings → Cloud providers")
		}
		return clouds.gemini.Chat(ctx, bare, messages, tools, thinkLevel)
	case "anthropic":
		return Message{}, fmt.Errorf("llm: file read/write tools aren't supported for Anthropic models yet")
	}
	return Message{}, fmt.Errorf("llm: unknown provider for model %q", model)
}

// Embed always goes to the local backend — cloud chat providers are
// wired up for chat only; RAG embeddings keep using whatever embedding
// model/backend was already configured via LLM_EMBED_MODEL.
func (r *Router) Embed(ctx context.Context, text string) ([]float32, error) {
	return r.Local.Embed(ctx, text)
}

// ChatModel, EmbedModel, and BaseURL forward to the local client — these
// describe the local backend's own default configuration (shown as-is in
// Settings → LLM backend) and are unaffected by which cloud providers are
// configured.
func (r *Router) ChatModel() string  { return r.Local.ChatModel }
func (r *Router) EmbedModel() string { return r.Local.EmbedModel }
func (r *Router) BaseURL() string    { return r.Local.BaseURL }

// ListModels returns every model the combined picker should show: local
// Ollama models (if the backend is reachable) plus, for each cloud
// provider with a configured API key, its fixed model list under that
// provider's picker prefix. A provider with no key contributes nothing —
// there'd be nothing useful to click on until a key is entered, so it's
// left out rather than shown disabled (matches the settings-form
// no-op-when-unconfigured convention used elsewhere in this app).
func (r *Router) ListModels(ctx context.Context) ([]Model, error) {
	r.mu.RLock()
	clouds := r.clouds
	r.mu.RUnlock()

	var out []Model
	if local, err := r.Local.ListModels(ctx); err == nil {
		out = append(out, local...)
	}
	if clouds.anthropic != nil {
		for _, name := range AnthropicModels {
			full := AnthropicPrefix + name
			out = append(out, Model{Name: full, SupportsFileTools: SupportsToolsForModel(full), Provider: "anthropic"})
		}
	}
	if clouds.openai != nil {
		for _, name := range OpenAIModels {
			full := OpenAIPrefix + name
			out = append(out, Model{Name: full, SupportsFileTools: SupportsToolsForModel(full), Provider: "openai"})
		}
	}
	if clouds.gemini != nil {
		for _, name := range GeminiModels {
			full := GeminiPrefix + name
			out = append(out, Model{Name: full, SupportsFileTools: SupportsToolsForModel(full), Provider: "gemini"})
		}
	}
	return out, nil
}
