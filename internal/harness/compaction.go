package harness

import (
	"fmt"

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

// DefaultCompactionConfig returns sensible default limits for agent context management.
func DefaultCompactionConfig() CompactionConfig {
	return CompactionConfig{
		MaxTotalChars:      60000,
		KeepRecentMessages: 6,
		MaxToolOutputChars: 800,
	}
}

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
