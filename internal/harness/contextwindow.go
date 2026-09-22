package harness

import (
	"strings"

	"fastllm/internal/config"
)

// DefaultContextWindowTokens is the assumed window for a model that neither
// declares one in config.json nor matches the table below. It is deliberately
// modest: under-estimating costs some extra compaction, while over-estimating
// overflows the window and fails the request outright.
const DefaultContextWindowTokens = 32768

// contextUtilization is the share of the window the transcript may occupy before
// compaction runs. The remainder absorbs what the transcript measurement does not
// see: the reply the model is about to generate (max_tokens is 4k-8k on a typical
// endpoint), tool schemas sent with every request, and the error in approximating
// tokens as chars/4 -- an approximation that understates code and JSON, the two
// things an agent transcript is mostly made of.
const contextUtilization = 75

// charsPerToken matches countApproxTokens. Compaction budgets are measured in
// characters, so the token window is converted once, here, rather than at each
// call site inventing its own ratio.
const charsPerToken = 4

// knownContextWindows maps a model identifier substring to its window in tokens.
// Keys are matched case-insensitively against the model ID as a substring, so
// "gpt-4o-2024-08-06" resolves through "gpt-4o". Longer keys are tried first, so
// a more specific entry wins over a shorter prefix of itself.
//
// This is a convenience for stock models, not a source of truth: an endpoint that
// serves something custom should set context_window explicitly.
var knownContextWindows = map[string]int{
	"claude-3-5-sonnet": 200000,
	"claude-3-7":        200000,
	"claude-sonnet-4":   200000,
	"claude-opus-4":     200000,
	"claude":            200000,
	"gpt-4o-mini":       128000,
	"gpt-4o":            128000,
	"gpt-4-turbo":       128000,
	"gpt-4":             8192,
	"gpt-3.5-turbo":     16385,
	"o1":                200000,
	"o3":                200000,
	"llama3.1":          131072,
	"llama3.2":          131072,
	"llama3":            8192,
	"qwen2.5-coder":     32768,
	"qwen2.5":           32768,
	"qwen3":             40960,
	// Confirmed against the served endpoint's max_model_len, not inferred from
	// the model name: GET /v1/models on the vLLM host reports 262144.
	"gemma-4":  262144,
	"gemma3":   131072,
	"gemma2":   8192,
	"deepseek": 65536,
	"mistral":  32768,
	"mixtral":  32768,
	"phi-4":    16384,
	"glm-5":    131072,
	"glm-4":    131072,
}

// ResolveContextWindow returns the context window in tokens for modelID, using
// the endpoint's explicit setting first, then the known-model table, then the
// default. Resolution never touches the network: endpoints here are frequently
// unreachable tunnels, and a startup probe would make the budget depend on
// whether a port happened to be forwarded.
func ResolveContextWindow(settings *config.Settings, modelID string) int {
	if settings != nil {
		for _, endpoint := range settings.Models {
			if endpoint.ID == modelID && endpoint.ContextWindow > 0 {
				return endpoint.ContextWindow
			}
		}
	}
	if window := lookupKnownContextWindow(modelID); window > 0 {
		return window
	}
	return DefaultContextWindowTokens
}

// lookupKnownContextWindow matches the longest table key contained in modelID, so
// that "gpt-4o" beats "gpt-4" for "gpt-4o-mini" regardless of Go's random map
// iteration order.
func lookupKnownContextWindow(modelID string) int {
	id := strings.ToLower(strings.TrimSpace(modelID))
	if id == "" {
		return 0
	}
	bestKey, bestWindow := "", 0
	for key, window := range knownContextWindows {
		if !strings.Contains(id, key) {
			continue
		}
		if len(key) > len(bestKey) {
			bestKey, bestWindow = key, window
		}
	}
	return bestWindow
}

// CompactionConfigForModel sizes the compaction budget from the model's real
// context window rather than a fixed character count, so a 200k-token model is
// not compacted as aggressively as an 8k one.
func CompactionConfigForModel(settings *config.Settings, modelID string) CompactionConfig {
	cfg := DefaultCompactionConfig()
	cfg.MaxTotalChars = contextBudgetChars(ResolveContextWindow(settings, modelID))
	return cfg
}

// contextBudgetChars converts a token window into the character budget compaction
// measures against, reserving headroom for the reply and tool schemas.
func contextBudgetChars(windowTokens int) int {
	if windowTokens <= 0 {
		windowTokens = DefaultContextWindowTokens
	}
	chars := windowTokens * charsPerToken * contextUtilization / 100
	// Never drop below a floor that can still hold a system prompt plus a few
	// turns; a tiny window otherwise yields a budget that compacts every turn.
	if chars < 8000 {
		chars = 8000
	}
	return chars
}
