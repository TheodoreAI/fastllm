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

// budgetCharsPerTokenTenths converts the token window into the character
// budget compaction measures against, in tenths of a character per token.
// Measured on a local 9B model (Ornith 1.5): prose ran 4.35 characters per
// token, Go source 3.65, and JSON tool schemas 3.37. An agent transcript is
// mostly code and JSON, so converting at 3.3 keeps it inside the window even
// at the dense end; the old 4.0 let a transcript reach the window with the
// estimate still claiming room.
const budgetCharsPerTokenTenths = 33

// replyReserveTokens is the part of the window kept free for the model's reply,
// including any reasoning it does first: an eighth of the window, between 1k
// and 8k tokens. A flat percentage cannot do this: on a 16k local model the old
// 25% headroom was about 4k tokens, and the tool schemas alone took 1.7k of it.
func replyReserveTokens(windowTokens int) int {
	reserve := windowTokens / 8
	if reserve < 1024 {
		reserve = 1024
	}
	if reserve > 8192 {
		reserve = 8192
	}
	return reserve
}

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

// contextBudgetChars converts a token window into the character budget for
// the transcript, the system prompt included, after reserving room for the
// reply. Tool schemas are sent with every request too; they depend on the
// request, so requestBudget subtracts them where the tools are known.
func contextBudgetChars(windowTokens int) int {
	if windowTokens <= 0 {
		windowTokens = DefaultContextWindowTokens
	}
	chars := (windowTokens - replyReserveTokens(windowTokens)) * budgetCharsPerTokenTenths / 10
	// Never drop below a floor that can still hold a system prompt plus a few
	// turns; a tiny window otherwise yields a budget that compacts every turn.
	if chars < 8000 {
		chars = 8000
	}
	return chars
}
