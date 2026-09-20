package harness

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"fastllm/internal/llm"
)

// TurnMetrics holds token, latency, and cost stats for a single model turn.
type TurnMetrics struct {
	Turn             int           `json:"turn"`
	PromptTokens     int           `json:"prompt_tokens"`
	CompletionTokens int           `json:"completion_tokens"`
	TotalTokens      int           `json:"total_tokens"`
	Duration         time.Duration `json:"duration"`
	TokensPerSecond  float64       `json:"tokens_per_second"`
	EstimatedCost    float64       `json:"estimated_cost"` // In USD
	// CostKnown is false when the endpoint bills for usage but the model has
	// no entry in the price table. Without it an unpriced paid model is
	// indistinguishable from a genuinely free local one -- both read $0.00.
	CostKnown bool `json:"cost_known"`
}

// SessionMetrics aggregates metrics across all turns in a session.
type SessionMetrics struct {
	TotalTurns            int           `json:"total_turns"`
	TotalPromptTokens     int           `json:"total_prompt_tokens"`
	TotalCompletionTokens int           `json:"total_completion_tokens"`
	TotalTokens           int           `json:"total_tokens"`
	TotalDuration         time.Duration `json:"total_duration"`
	TotalCost             float64       `json:"total_cost"`
	// CostComplete is false once any turn's price was unknown, so a session
	// total is never presented as exact when part of it could not be priced.
	CostComplete          bool             `json:"cost_complete"`
	ObservationEfficiency ObservationStats `json:"observation_efficiency"`
}

// ModelRate defines pricing per 1,000,000 tokens in USD.
type ModelRate struct {
	PromptPricePerMillion     float64
	CompletionPricePerMillion float64
}

// modelPricing maps a canonical model id to its list price per 1,000,000
// tokens in USD. Verified 2026-09-20 against each provider's own pricing page;
// re-check before trusting a figure, since published prices move.
//
// Keys are matched exactly first, then by longest prefix -- see LookupModelRate.
// A substring match over this map would be both wrong and nondeterministic:
// "gpt-4o-mini" contains "gpt-4o", and Go randomizes map iteration order, so
// the same binary could price one request at $0.15 and the next at $2.50.
var modelPricing = map[string]ModelRate{
	// Anthropic
	"claude-fable-5":    {PromptPricePerMillion: 10.00, CompletionPricePerMillion: 50.00},
	"claude-mythos-5":   {PromptPricePerMillion: 10.00, CompletionPricePerMillion: 50.00},
	"claude-opus-5":     {PromptPricePerMillion: 5.00, CompletionPricePerMillion: 25.00},
	"claude-opus-4-8":   {PromptPricePerMillion: 5.00, CompletionPricePerMillion: 25.00},
	"claude-opus-4-7":   {PromptPricePerMillion: 5.00, CompletionPricePerMillion: 25.00},
	"claude-opus-4-6":   {PromptPricePerMillion: 5.00, CompletionPricePerMillion: 25.00},
	"claude-opus-4-5":   {PromptPricePerMillion: 5.00, CompletionPricePerMillion: 25.00},
	"claude-sonnet-5":   {PromptPricePerMillion: 3.00, CompletionPricePerMillion: 15.00},
	"claude-sonnet-4-6": {PromptPricePerMillion: 3.00, CompletionPricePerMillion: 15.00},
	"claude-sonnet-4-5": {PromptPricePerMillion: 3.00, CompletionPricePerMillion: 15.00},
	"claude-haiku-4-5":  {PromptPricePerMillion: 1.00, CompletionPricePerMillion: 5.00},
	"claude-3-5-sonnet": {PromptPricePerMillion: 3.00, CompletionPricePerMillion: 15.00},
	"claude-3-5-haiku":  {PromptPricePerMillion: 0.80, CompletionPricePerMillion: 4.00},
	"claude-3-opus":     {PromptPricePerMillion: 15.00, CompletionPricePerMillion: 75.00},

	// OpenAI
	"gpt-6-astra":   {PromptPricePerMillion: 10.00, CompletionPricePerMillion: 50.00},
	"gpt-5.6-sol":   {PromptPricePerMillion: 4.00, CompletionPricePerMillion: 20.00},
	"gpt-5.6-terra": {PromptPricePerMillion: 2.00, CompletionPricePerMillion: 12.00},
	"gpt-5.6-luna":  {PromptPricePerMillion: 0.20, CompletionPricePerMillion: 1.20},
	"gpt-5.5-pro":   {PromptPricePerMillion: 30.00, CompletionPricePerMillion: 180.00},
	"gpt-5.5":       {PromptPricePerMillion: 5.00, CompletionPricePerMillion: 30.00},
	"gpt-5.4-mini":  {PromptPricePerMillion: 0.75, CompletionPricePerMillion: 4.50},
	"gpt-5.4":       {PromptPricePerMillion: 2.50, CompletionPricePerMillion: 15.00},
	"gpt-5-mini":    {PromptPricePerMillion: 0.25, CompletionPricePerMillion: 2.00},
	"gpt-5":         {PromptPricePerMillion: 1.25, CompletionPricePerMillion: 10.00},
	"gpt-4o-mini":   {PromptPricePerMillion: 0.15, CompletionPricePerMillion: 0.60},
	"gpt-4o":        {PromptPricePerMillion: 2.50, CompletionPricePerMillion: 10.00},
	"o1-pro":        {PromptPricePerMillion: 150.00, CompletionPricePerMillion: 600.00},
	"o1":            {PromptPricePerMillion: 15.00, CompletionPricePerMillion: 60.00},
	"o3-pro":        {PromptPricePerMillion: 20.00, CompletionPricePerMillion: 80.00},
	"o3-mini":       {PromptPricePerMillion: 1.10, CompletionPricePerMillion: 4.40},
	"o3":            {PromptPricePerMillion: 2.00, CompletionPricePerMillion: 8.00},
	"o4-mini":       {PromptPricePerMillion: 1.10, CompletionPricePerMillion: 4.40},

	// Google. The 3.6/3.7/3.8 Flash rate is promotional through 2026-12-31 and
	// doubles to 1.50/7.50 on 2027-01-01.
	"gemini-3.8-flash":      {PromptPricePerMillion: 0.75, CompletionPricePerMillion: 3.75},
	"gemini-3.7-flash":      {PromptPricePerMillion: 0.75, CompletionPricePerMillion: 3.75},
	"gemini-3.6-flash":      {PromptPricePerMillion: 0.75, CompletionPricePerMillion: 3.75},
	"gemini-3.5-flash":      {PromptPricePerMillion: 1.50, CompletionPricePerMillion: 9.00},
	"gemini-3.1-flash-lite": {PromptPricePerMillion: 0.25, CompletionPricePerMillion: 1.50},
	"gemini-3.1-pro":        {PromptPricePerMillion: 2.00, CompletionPricePerMillion: 12.00},
	"gemini-2.5-pro":        {PromptPricePerMillion: 1.25, CompletionPricePerMillion: 10.00},
	"gemini-1.5-pro":        {PromptPricePerMillion: 1.25, CompletionPricePerMillion: 5.00},
	"gemini-1.5-flash":      {PromptPricePerMillion: 0.075, CompletionPricePerMillion: 0.30},

	// DeepSeek. Carried over from the previous table and NOT re-verified.
	"deepseek-chat":     {PromptPricePerMillion: 0.14, CompletionPricePerMillion: 0.28},
	"deepseek-reasoner": {PromptPricePerMillion: 0.14, CompletionPricePerMillion: 0.55},
}

// normalizeModelName reduces a model id to the form used as a pricing key:
// lowercased, without fastllm's router prefix ("gemini:gemini-3.7-flash") and
// without a trailing dated-snapshot suffix ("claude-haiku-4-5-20251001").
func normalizeModelName(model string) string {
	name := strings.ToLower(strings.TrimSpace(model))
	if idx := strings.LastIndex(name, ":"); idx >= 0 {
		name = name[idx+1:]
	}
	return datedSnapshotSuffix.ReplaceAllString(name, "")
}

var datedSnapshotSuffix = regexp.MustCompile(`-\d{8}$`)

// LookupModelRate finds a model's price. It matches exactly first, then falls
// back to the longest key that is a prefix of the name, so a more specific
// variant always beats a shorter relative: "gpt-4o-mini" resolves to its own
// entry rather than to "gpt-4o". Longest-prefix is deterministic, unlike
// ranging over the map, because two distinct keys of equal length cannot both
// prefix the same string.
func LookupModelRate(model string) (ModelRate, bool) {
	name := normalizeModelName(model)
	if rate, ok := modelPricing[name]; ok {
		return rate, true
	}
	best := ""
	for key := range modelPricing {
		if strings.HasPrefix(name, key) && len(key) > len(best) {
			best = key
		}
	}
	if best == "" {
		return ModelRate{}, false
	}
	return modelPricing[best], true
}

// CalculateCost computes the estimated cost in USD for the given token usage,
// and reports whether the model's price is actually known. Callers must not
// treat a zero cost as free without checking that second value: an unpriced
// paid model and a free local one both compute to zero.
func CalculateCost(model string, promptTokens, completionTokens int) (float64, bool) {
	rate, ok := LookupModelRate(model)
	if !ok {
		return 0, false
	}
	cost := (float64(promptTokens) / 1_000_000.0 * rate.PromptPricePerMillion) +
		(float64(completionTokens) / 1_000_000.0 * rate.CompletionPricePerMillion)
	return cost, true
}

// ComputeTurnMetrics calculates TurnMetrics from tokens and duration.
func ComputeTurnMetrics(turn int, model string, promptTokens, compTokens int, duration time.Duration, billable bool) TurnMetrics {
	totalTokens := promptTokens + compTokens
	sec := duration.Seconds()
	tokPerSec := 0.0
	if sec > 0 && compTokens > 0 {
		tokPerSec = float64(compTokens) / sec
	}

	// A model served from this machine or a private tunnel costs nothing, so an
	// unpriced local model is genuinely free rather than merely unknown.
	cost, priced := CalculateCost(model, promptTokens, compTokens)
	costKnown := priced || !billable

	return TurnMetrics{
		Turn:             turn,
		PromptTokens:     promptTokens,
		CompletionTokens: compTokens,
		TotalTokens:      totalTokens,
		Duration:         duration,
		TokensPerSecond:  tokPerSec,
		EstimatedCost:    cost,
		CostKnown:        costKnown,
	}
}

// FormatTurnSummary returns a readable one-line summary of turn metrics.
func (tm TurnMetrics) FormatTurnSummary() string {
	costStr := FormatCost(tm.EstimatedCost, tm.CostKnown)
	return fmt.Sprintf("in: %d tok | out: %d tok | %.1f tok/s | %.2fs | cost: %s",
		tm.PromptTokens, tm.CompletionTokens, tm.TokensPerSecond, tm.Duration.Seconds(), costStr)
}

// Add accumulates a turn into session totals.
func (sm *SessionMetrics) Add(tm TurnMetrics) {
	// The first turn establishes the baseline; after that one unpriced turn is
	// enough to make the running total an understatement rather than a figure.
	if sm.TotalTurns == 0 {
		sm.CostComplete = tm.CostKnown
	} else if !tm.CostKnown {
		sm.CostComplete = false
	}
	sm.TotalTurns++
	sm.TotalPromptTokens += tm.PromptTokens
	sm.TotalCompletionTokens += tm.CompletionTokens
	sm.TotalTokens += tm.TotalTokens
	sm.TotalDuration += tm.Duration
	sm.TotalCost += tm.EstimatedCost
}

// FormatSessionSummary returns a multi-line formatted summary of session metrics.
func (sm SessionMetrics) FormatSessionSummary() string {
	costStr := FormatCost(sm.TotalCost, sm.CostComplete)
	return fmt.Sprintf("Turns: %d | Total Tokens: %d (in: %d, out: %d) | Total Time: %.2fs | Total Cost: %s",
		sm.TotalTurns, sm.TotalTokens, sm.TotalPromptTokens, sm.TotalCompletionTokens, sm.TotalDuration.Seconds(), costStr)
}

// usageReporter is implemented by LLM clients that can report the token counts
// the server itself returned for the last completion.
type usageReporter interface {
	LastUsage() (llm.Usage, bool)
}

// resolveTurnTokens prefers the server's own accounting over a local estimate.
//
// The estimate counts only what came back in the reply, which is wrong in two
// ways on an agent turn: a tool call carries almost no visible text, and models
// that stream a separate reasoning channel do their real work in tokens that
// never appear in the message at all. Both make tokens-per-second read far lower
// than the model is actually generating.
func resolveTurnTokens(client LLMClient, estPrompt, estCompletion int) (prompt, completion int, measured bool) {
	if reporter, ok := client.(usageReporter); ok {
		if usage, have := reporter.LastUsage(); have {
			p, c := usage.PromptTokens, usage.CompletionTokens
			if p == 0 {
				p = estPrompt
			}
			if c == 0 {
				c = estCompletion
			}
			return p, c, true
		}
	}
	return estPrompt, estCompletion, false
}

// FormatCost renders a cost figure, distinguishing "nothing to pay" from "we
// don't know what this costs". Showing an unpriced paid model as $0.00 is worse
// than showing nothing: it reads as a measurement.
func FormatCost(cost float64, known bool) string {
	if !known {
		return "unpriced"
	}
	if cost <= 0 {
		return "$0.00"
	}
	return fmt.Sprintf("$%.4f", cost)
}
