package harness

import (
	"fmt"
	"strings"
	"time"
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
}

// SessionMetrics aggregates metrics across all turns in a session.
type SessionMetrics struct {
	TotalTurns            int              `json:"total_turns"`
	TotalPromptTokens     int              `json:"total_prompt_tokens"`
	TotalCompletionTokens int              `json:"total_completion_tokens"`
	TotalTokens           int              `json:"total_tokens"`
	TotalDuration         time.Duration    `json:"total_duration"`
	TotalCost             float64          `json:"total_cost"`
	ObservationEfficiency ObservationStats `json:"observation_efficiency"`
}

// ModelRate defines pricing per 1,000,000 tokens in USD.
type ModelRate struct {
	PromptPricePerMillion     float64
	CompletionPricePerMillion float64
}

var modelPricing = map[string]ModelRate{
	"claude-3-5-sonnet": {PromptPricePerMillion: 3.00, CompletionPricePerMillion: 15.00},
	"claude-3-5-haiku":  {PromptPricePerMillion: 0.80, CompletionPricePerMillion: 4.00},
	"claude-3-opus":     {PromptPricePerMillion: 15.00, CompletionPricePerMillion: 75.00},
	"gpt-4o":            {PromptPricePerMillion: 2.50, CompletionPricePerMillion: 10.00},
	"gpt-4o-mini":       {PromptPricePerMillion: 0.15, CompletionPricePerMillion: 0.60},
	"gemini-1.5-pro":    {PromptPricePerMillion: 1.25, CompletionPricePerMillion: 5.00},
	"gemini-1.5-flash":  {PromptPricePerMillion: 0.075, CompletionPricePerMillion: 0.30},
	"deepseek-chat":     {PromptPricePerMillion: 0.14, CompletionPricePerMillion: 0.28},
	"deepseek-reasoner": {PromptPricePerMillion: 0.14, CompletionPricePerMillion: 0.55},
}

// CalculateCost computes estimated cost in USD for the given token usage.
func CalculateCost(model string, promptTokens, completionTokens int) float64 {
	lower := strings.ToLower(model)

	// Check prefixes / known names
	var rate ModelRate
	matched := false
	for k, v := range modelPricing {
		if strings.Contains(lower, k) {
			rate = v
			matched = true
			break
		}
	}

	if !matched {
		// Local models (Ollama, vLLM, OSU cluster, Llama, Qwen, etc.) are free
		return 0.0
	}

	cost := (float64(promptTokens) / 1_000_000.0 * rate.PromptPricePerMillion) +
		(float64(completionTokens) / 1_000_000.0 * rate.CompletionPricePerMillion)
	return cost
}

// ComputeTurnMetrics calculates TurnMetrics from tokens and duration.
func ComputeTurnMetrics(turn int, model string, promptTokens, compTokens int, duration time.Duration) TurnMetrics {
	totalTokens := promptTokens + compTokens
	sec := duration.Seconds()
	tokPerSec := 0.0
	if sec > 0 && compTokens > 0 {
		tokPerSec = float64(compTokens) / sec
	}

	cost := CalculateCost(model, promptTokens, compTokens)

	return TurnMetrics{
		Turn:             turn,
		PromptTokens:     promptTokens,
		CompletionTokens: compTokens,
		TotalTokens:      totalTokens,
		Duration:         duration,
		TokensPerSecond:  tokPerSec,
		EstimatedCost:    cost,
	}
}

// FormatTurnSummary returns a readable one-line summary of turn metrics.
func (tm TurnMetrics) FormatTurnSummary() string {
	costStr := "$0.00"
	if tm.EstimatedCost > 0 {
		costStr = fmt.Sprintf("$%.4f", tm.EstimatedCost)
	}
	return fmt.Sprintf("in: %d tok | out: %d tok | %.1f tok/s | %.2fs | cost: %s",
		tm.PromptTokens, tm.CompletionTokens, tm.TokensPerSecond, tm.Duration.Seconds(), costStr)
}

// Add accumulates a turn into session totals.
func (sm *SessionMetrics) Add(tm TurnMetrics) {
	sm.TotalTurns++
	sm.TotalPromptTokens += tm.PromptTokens
	sm.TotalCompletionTokens += tm.CompletionTokens
	sm.TotalTokens += tm.TotalTokens
	sm.TotalDuration += tm.Duration
	sm.TotalCost += tm.EstimatedCost
}

// FormatSessionSummary returns a multi-line formatted summary of session metrics.
func (sm SessionMetrics) FormatSessionSummary() string {
	costStr := "$0.00"
	if sm.TotalCost > 0 {
		costStr = fmt.Sprintf("$%.4f", sm.TotalCost)
	}
	return fmt.Sprintf("Turns: %d | Total Tokens: %d (in: %d, out: %d) | Total Time: %.2fs | Total Cost: %s",
		sm.TotalTurns, sm.TotalTokens, sm.TotalPromptTokens, sm.TotalCompletionTokens, sm.TotalDuration.Seconds(), costStr)
}
