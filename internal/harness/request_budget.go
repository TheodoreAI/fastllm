package harness

import (
	"encoding/json"
	"fmt"
	"sort"
	"unicode/utf8"

	"fastllm/internal/llm"
)

// minTranscriptBudget is the smallest transcript budget a request may be left
// with after its tool schemas are subtracted; below it every turn compacts.
const minTranscriptBudget = 8000

// requestBudget narrows the transcript budget by the tool schemas sent with
// every request. They are counted in characters, the unit the budget uses, so
// no second ratio is needed.
func requestBudget(cfg CompactionConfig, tools []llm.Tool) CompactionConfig {
	if len(tools) == 0 {
		return cfg
	}
	cfg.MaxTotalChars -= toolSchemaChars(tools)
	if cfg.MaxTotalChars < minTranscriptBudget {
		cfg.MaxTotalChars = minTranscriptBudget
	}
	return cfg
}

// toolSchemaChars is the size of the tool schemas as sent, in characters.
func toolSchemaChars(tools []llm.Tool) int {
	if len(tools) == 0 {
		return 0
	}
	schema, err := json.Marshal(tools)
	if err != nil {
		return 0
	}
	return len(schema)
}

// minToolResultChars is the least a tool result is cut to, however small the
// budget: enough for an error message or a short listing to stay readable.
const minToolResultChars = 4000

// toolResultLimit is the most characters one tool result may add to the
// transcript: a quarter of the budget, so a single broad search or large read
// cannot crowd out the conversation it belongs to.
func toolResultLimit(cfg CompactionConfig) int {
	limit := cfg.MaxTotalChars / 4
	if limit < minToolResultChars {
		limit = minToolResultChars
	}
	return limit
}

// boundToolResult keeps the start and end of a result longer than limit and
// states how much was cut, so the model narrows its request instead of
// guessing at what is missing. Compaction never shortens the current turn, so
// this is the only point a single oversized result can be stopped.
func boundToolResult(content string, limit int) string {
	if limit <= 0 || len(content) <= limit {
		return content
	}
	notice := fmt.Sprintf("\n\n[%d characters omitted: this result is larger than the model's context allows. "+
		"Narrow the request (a smaller path, a more specific pattern, or a line range) to see the rest.]\n\n",
		len(content)-limit)
	keep := limit - len(notice)
	if keep < 0 {
		keep = 0
	}
	head := runeBoundary(content, keep*2/3)
	tail := runeBoundary(content, len(content)-(keep-head))
	return content[:head] + notice + content[tail:]
}

// runeBoundary moves i back to the start of the UTF-8 sequence it falls in,
// so a cut never splits a character.
func runeBoundary(s string, i int) int {
	if i <= 0 {
		return 0
	}
	if i >= len(s) {
		return len(s)
	}
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

// shrinkToolResults is the last resort after compaction, which summarizes
// earlier turns but leaves the current one whole. While the transcript is over
// budget it halves the largest tool result, the current turn's included, down
// to minToolResultChars; it reports whether anything changed. Only the copies
// in the returned slice change.
func shrinkToolResults(messages []llm.Message, budget int) ([]llm.Message, bool) {
	total := messageCharacterCount(messages)
	if budget <= 0 || total <= budget {
		return messages, false
	}
	var tools []int
	for i, message := range messages {
		if message.Role == "tool" && len(message.Content) > minToolResultChars {
			tools = append(tools, i)
		}
	}
	if len(tools) == 0 {
		return messages, false
	}
	result := append([]llm.Message(nil), messages...)
	changed := false
	for total > budget {
		sort.SliceStable(tools, func(a, b int) bool { return len(result[tools[a]].Content) > len(result[tools[b]].Content) })
		largest := tools[0]
		before := len(result[largest].Content)
		if before <= minToolResultChars {
			break
		}
		target := before / 2
		if over := total - budget; before-over > target {
			target = before - over
		}
		if target < minToolResultChars {
			target = minToolResultChars
		}
		result[largest].Content = boundToolResult(result[largest].Content, target)
		after := len(result[largest].Content)
		if after >= before {
			break
		}
		total -= before - after
		changed = true
	}
	return result, changed
}
