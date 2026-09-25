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

// knownContextWindows maps a model identifier to the most input tokens a request
// may carry. Keys match case-insensitively as a whole segment of the model ID --
// bounded by the ends or by a non-alphanumeric character -- so "gpt-4o-2024-08-06"
// resolves through "gpt-4o" while "gpt-4o3" matches no "o3", and the longest
// matching key wins, so "gpt-4.1" beats "gpt-4".
//
// This is the fallback for a model nobody measured: /models detect asks the
// provider and saves the real value as context_window, and a request the model
// refuses as too large is compacted to the limit the provider states.
var knownContextWindows = map[string]int{
	// Anthropic, from the Models API reference (max_input_tokens), 2026-09.
	"claude-fable-5":    1000000,
	"claude-fable-5-1":  1000000,
	"claude-mythos":     1000000,
	"claude-opus-5":     1000000,
	"claude-opus-5-5":   1000000,
	"claude-opus-4-8":   1000000,
	"claude-opus-4-7":   1000000,
	"claude-opus-4-6":   1000000,
	"claude-sonnet-5":   1000000,
	"claude-sonnet-4-6": 1000000,
	"claude-haiku-4-5":  200000,
	"claude-3-5-sonnet": 200000,
	"claude-3-7":        200000,
	"claude-sonnet-4":   200000,
	"claude-opus-4":     200000,
	"claude":            200000,
	// OpenAI, from developers.openai.com/api/docs/models, 2026-09. Where a
	// model's context window includes its output, the entry is the input limit
	// (GPT-5: 272,000 of 400,000) or the window less the max output.
	"gpt-6":         922000, // 1.05M window, 128K max output
	"gpt-5.4":       922000, // 1.05M window, 128K max output
	"gpt-5":         272000,
	"gpt-4.1":       1000000, // 1,047,576 window, 32,768 max output
	"gpt-4o-mini":   128000,
	"gpt-4o":        128000,
	"gpt-4-turbo":   128000,
	"gpt-4":         8192,
	"gpt-3.5-turbo": 16385,
	"o1":            200000,
	"o3":            200000,
	"o4-mini":       200000,
	// Google, from ai.google.dev/gemini-api/docs/models, 2026-09: Gemini models
	// accept 1,048,576 input tokens. Variants differ; /models detect reads
	// inputTokenLimit for the exact one.
	"gemini": 1048576,
	// Open models: the published maximum. A local server usually serves less,
	// so a local endpoint should set context_window (see /models detect).
	"llama3.1":      131072,
	"llama3.2":      131072,
	"llama3":        8192,
	"qwen2.5-coder": 32768,
	"qwen2.5":       32768,
	"qwen3":         40960,
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
		if len(key) > len(bestKey) && containsSegment(id, key) {
			bestKey, bestWindow = key, window
		}
	}
	return bestWindow
}

// containsSegment reports whether key occurs in id with no letter or digit
// directly before or after it.
func containsSegment(id, key string) bool {
	for offset := 0; ; {
		i := strings.Index(id[offset:], key)
		if i < 0 {
			return false
		}
		start, end := offset+i, offset+i+len(key)
		if (start == 0 || !isAlphanumeric(id[start-1])) && (end == len(id) || !isAlphanumeric(id[end])) {
			return true
		}
		offset = start + 1
	}
}

func isAlphanumeric(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
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
