package harness

import (
	"fmt"
	"strings"

	"fastllm/internal/llm"
)

// CompactionConfig configures when and how messages are compacted.
type CompactionConfig struct {
	// MaxTotalChars triggers compaction if total character length of messages exceeds this.
	// Defaults to 60,000 (roughly 15k tokens).
	MaxTotalChars int

	// KeepRecentMessages specifies how many recent messages to never touch.
	// Defaults to 6.
	KeepRecentMessages int

	// MaxToolOutputChars is the max length allowed for older tool outputs before truncation.
	// Defaults to 800 chars.
	MaxToolOutputChars int
}

// OnlineCompactMessages replaces completed older trajectories with a deterministic
// state checkpoint while retaining the original task and recent working set.
func OnlineCompactMessages(messages []llm.Message, cfg CompactionConfig) ([]llm.Message, bool) {
	if cfg.MaxTotalChars <= 0 {
		cfg = DefaultCompactionConfig()
	}
	if messageCharacterCount(messages) <= cfg.MaxTotalChars || len(messages) <= cfg.KeepRecentMessages+2 {
		return messages, false
	}
	tailStart := len(messages) - cfg.KeepRecentMessages
	for tailStart > 2 {
		message := messages[tailStart]
		if message.Role == "user" || (message.Role == "assistant" && len(message.ToolCalls) == 0) {
			break
		}
		tailStart--
	}
	if tailStart <= 2 {
		return CompactMessages(messages, cfg)
	}
	checkpoint := buildStateCheckpoint(messages[2:tailStart])
	if checkpoint == "" {
		return CompactMessages(messages, cfg)
	}
	result := make([]llm.Message, 0, 3+len(messages)-tailStart)
	result = append(result, messages[:2]...)
	result = append(result, llm.Message{Role: "system", Content: checkpoint})
	result = append(result, messages[tailStart:]...)
	if messageCharacterCount(result) >= messageCharacterCount(messages) {
		return CompactMessages(messages, cfg)
	}
	return result, true
}

func buildStateCheckpoint(messages []llm.Message) string {
	var actions, evidence, requests []string
	for _, message := range messages {
		switch message.Role {
		case "user":
			if text := compactLine(message.Content, 180); text != "" {
				requests = appendUnique(requests, text, 5)
			}
		case "assistant":
			for _, call := range message.ToolCalls {
				actions = appendUnique(actions, call.Function.Name+" "+compactLine(call.Function.Arguments, 160), 16)
			}
		case "tool":
			text := checkpointEvidence(message.Content)
			if text != "" {
				evidence = appendUnique(evidence, text, 16)
			}
		}
	}
	if len(actions) == 0 && len(evidence) == 0 && len(requests) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("[State Checkpoint]\nThis deterministic checkpoint replaces completed older trajectory messages.\n")
	writeCheckpointSection(&builder, "Requests", requests)
	writeCheckpointSection(&builder, "Actions", actions)
	writeCheckpointSection(&builder, "Evidence and receipts", evidence)
	return strings.TrimSpace(builder.String())
}

func checkpointEvidence(content string) string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.Contains(trimmed, "obs_") || strings.Contains(lower, "error") || strings.Contains(lower, "fail") || strings.Contains(lower, "success") || strings.Contains(lower, "exit code") || strings.Contains(lower, "sha-256") {
			return compactLine(trimmed, 220)
		}
	}
	return compactLine(content, 140)
}

func writeCheckpointSection(builder *strings.Builder, title string, values []string) {
	if len(values) == 0 {
		return
	}
	builder.WriteString("\n" + title + ":\n")
	for _, value := range values {
		builder.WriteString("- " + value + "\n")
	}
}

func appendUnique(values []string, value string, limit int) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	if len(values) >= limit {
		return values
	}
	return append(values, value)
}

func compactLine(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > limit {
		value = value[:limit-3] + "..."
	}
	return value
}

func messageCharacterCount(messages []llm.Message) int {
	total := 0
	for _, message := range messages {
		total += len(message.Content)
	}
	return total
}

// DefaultCompactionConfig returns sensible default limits for agent context management.
func DefaultCompactionConfig() CompactionConfig {
	return CompactionConfig{
		MaxTotalChars:      60000,
		KeepRecentMessages: 6,
		MaxToolOutputChars: 800,
	}
}

// planBoundaryCompactionConfig lowers the compaction budget when a plan step has
// just completed. A boundary is a point where older trajectory is known to be safe
// to collapse, so compaction runs there instead of waiting for the full threshold.
func planBoundaryCompactionConfig(cfg CompactionConfig, contextChars int, planBoundary bool) CompactionConfig {
	if planBoundary && contextChars > cfg.MaxTotalChars/2 {
		cfg.MaxTotalChars = contextChars - 1
	}
	return cfg
}

// ForceCompactMessages compacts regardless of how far the transcript is from the
// budget, for an explicit user request rather than the automatic threshold. It
// drops the budget just under the current size so the normal path runs; the
// message-count and checkpoint guards inside still apply, so a short transcript
// with nothing safe to collapse reports false rather than corrupting the tail.
//
// The tool-output ceiling tightens too. When the recent-message window covers the
// whole trajectory there is no completed span to checkpoint, so OnlineCompactMessages
// falls back to truncating older tool output -- and at the default 800-char ceiling
// that reclaims nothing from the mid-size outputs a hand-invoked compaction is
// usually aimed at, making /compact silently report "nothing to compact".
func ForceCompactMessages(messages []llm.Message, cfg CompactionConfig) ([]llm.Message, bool) {
	if cfg.MaxTotalChars <= 0 {
		cfg = DefaultCompactionConfig()
	}
	if current := messageCharacterCount(messages); current-1 < cfg.MaxTotalChars {
		cfg.MaxTotalChars = current - 1
	}
	if cfg.MaxTotalChars < 1 {
		return messages, false
	}
	if cfg.MaxToolOutputChars > forcedMaxToolOutputChars {
		cfg.MaxToolOutputChars = forcedMaxToolOutputChars
	}
	return OnlineCompactMessages(messages, cfg)
}

// forcedMaxToolOutputChars is the older-tool-output ceiling for a manual compaction.
// Low enough to reclaim real space from routine command output, high enough that a
// truncated head and tail still identify what the call did.
const forcedMaxToolOutputChars = 240

// CompactMessages prunes older tool outputs if total message size exceeds budget,
// keeping system prompt, user prompt, and recent turns intact.
func CompactMessages(messages []llm.Message, cfg CompactionConfig) ([]llm.Message, bool) {
	if cfg.MaxTotalChars <= 0 {
		cfg.MaxTotalChars = 60000
	}
	if cfg.KeepRecentMessages <= 0 {
		cfg.KeepRecentMessages = 6
	}
	if cfg.MaxToolOutputChars <= 0 {
		cfg.MaxToolOutputChars = 800
	}

	totalChars := 0
	for _, m := range messages {
		totalChars += len(m.Content)
	}

	if totalChars <= cfg.MaxTotalChars || len(messages) <= cfg.KeepRecentMessages+2 {
		return messages, false
	}

	result := make([]llm.Message, len(messages))
	copy(result, messages)

	cutoff := len(messages) - cfg.KeepRecentMessages
	prunedAny := false

	// Iterate older messages, skipping index 0 (system) and 1 (user task)
	for i := 2; i < cutoff; i++ {
		m := &result[i]
		if m.Role == "tool" && len(m.Content) > cfg.MaxToolOutputChars {
			headLen := cfg.MaxToolOutputChars / 2
			tailLen := cfg.MaxToolOutputChars / 4
			truncatedBytes := len(m.Content) - headLen - tailLen

			head := m.Content[:headLen]
			tail := m.Content[len(m.Content)-tailLen:]
			m.Content = fmt.Sprintf("%s\n\n... [Truncated %d characters of older tool output for context budget] ...\n\n%s", head, truncatedBytes, tail)
			prunedAny = true
		}
	}

	return result, prunedAny
}
