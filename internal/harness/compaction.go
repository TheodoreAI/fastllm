package harness

import (
	"encoding/json"
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

// OnlineCompactMessages fits an over-budget transcript back into its budget in
// stages, giving up the cheapest material first. It never touches the head (the
// system prompt and the task) or the recent tail, which is kept word for word.
//
//  1. Older tool results become receipts: what was run, how large the output
//     was, and its key line. Tool output is most of a transcript's bulk and can
//     be produced again; every user and assistant message stays verbatim.
//  2. If that is not enough, the oldest part of the conversation becomes a
//     deterministic state checkpoint. The cut is placed as late as fits, so as
//     much conversation as possible stays verbatim after it.
//
// Both stages aim below the budget, not at it, so the next turns fit without
// compacting again: each compaction changes the prompt's prefix, which costs a
// local server its prompt cache.
func OnlineCompactMessages(messages []llm.Message, cfg CompactionConfig) ([]llm.Message, bool) {
	if cfg.MaxTotalChars <= 0 {
		cfg = DefaultCompactionConfig()
	}
	head := compactionHead(messages)
	total := messageCharacterCount(messages)
	if total <= cfg.MaxTotalChars || len(messages) <= cfg.KeepRecentMessages+head {
		return messages, false
	}
	tailStart := recentTailStart(messages, head, cfg)
	if tailStart <= head {
		return CompactMessages(messages, cfg)
	}
	free := cfg.MaxTotalChars - messageCharacterCount(messages[:head])
	target := cfg.MaxTotalChars - free*(100-compactionTargetPercent)/100
	maxCheckpoint := free * checkpointPercent / 100

	cleared := clearOlderToolOutput(messages, head, tailStart, target)
	if messageCharacterCount(cleared) <= cfg.MaxTotalChars {
		return cleared, true
	}

	// Every older tool result is a receipt now and the transcript is still over
	// budget. Try each place the checkpoint could end, earliest first, and keep
	// the first that reaches the target, else the first that fits at all.
	var fits []llm.Message
	for cut := head + 1; cut <= tailStart; cut++ {
		if !canCutBefore(messages[cut]) {
			continue
		}
		result := checkpointBefore(messages, cleared, head, cut, maxCheckpoint)
		if result == nil {
			continue
		}
		size := messageCharacterCount(result)
		if size <= target {
			return result, true
		}
		if fits == nil && size <= cfg.MaxTotalChars {
			fits = result
		}
	}
	if fits != nil {
		return fits, true
	}
	if result := checkpointBefore(messages, cleared, head, tailStart, maxCheckpoint); result != nil && messageCharacterCount(result) < total {
		return result, true
	}
	return CompactMessages(messages, cfg)
}

// Shares of the free budget -- what the head leaves -- in percent. Compaction
// aims to fill compactionTargetPercent of it, leaving the rest for the turns
// that follow; the verbatim tail may take up to recentTailPercent (with
// KeepRecentMessages as its minimum, in messages) and the checkpoint up to
// checkpointPercent. What remains keeps older conversation verbatim.
const (
	compactionTargetPercent = 70
	recentTailPercent       = 35
	checkpointPercent       = 25
)

// canCutBefore reports whether a transcript can be cut before message without
// separating a tool result from its call. An assistant message that makes tool
// calls is a valid cut: its results follow it. Allowing it matters, because a
// long agent run is one request followed by nothing but tool calls.
func canCutBefore(message llm.Message) bool {
	return message.Role != "tool"
}

// recentTailStart is where the verbatim tail begins: at least the last
// KeepRecentMessages messages, moved back so no tool result loses its call,
// then further back to earlier cut points while the tail fits
// recentTailPercent of what the head leaves of the budget.
func recentTailStart(messages []llm.Message, head int, cfg CompactionConfig) int {
	start := len(messages) - cfg.KeepRecentMessages
	if start < head {
		start = head
	}
	for start > head && !canCutBefore(messages[start]) {
		start--
	}
	limit := (cfg.MaxTotalChars - messageCharacterCount(messages[:head])) * recentTailPercent / 100
	size := messageCharacterCount(messages[start:])
	for earlier := start - 1; earlier > head; earlier-- {
		size += messageCharacterCount(messages[earlier : earlier+1])
		if size > limit {
			break
		}
		if canCutBefore(messages[earlier]) {
			start = earlier
		}
	}
	return start
}

// clearOlderToolOutput replaces tool results in messages[from:to] with receipts,
// oldest first, until the transcript is within target. Only the copies in the
// returned slice change.
func clearOlderToolOutput(messages []llm.Message, from, to, target int) []llm.Message {
	calls := make(map[string]llm.ToolCall)
	for _, message := range messages[:to] {
		for _, call := range message.ToolCalls {
			calls[call.ID] = call
		}
	}
	result := append([]llm.Message(nil), messages...)
	total := messageCharacterCount(result)
	for i := from; i < to && total > target; i++ {
		if result[i].Role != "tool" {
			continue
		}
		receipt := toolReceipt(result[i].Content, calls[result[i].ToolCallID])
		if len(receipt) >= len(result[i].Content) {
			continue
		}
		total -= len(result[i].Content) - len(receipt)
		result[i].Content = receipt
	}
	return result
}

const toolReceiptPrefix = "[Output of "

// toolReceipt stands in for a cleared tool result. It says what produced the
// output and gives its key line, so the model knows the work happened and can
// run the tool again rather than guess at what it said.
func toolReceipt(content string, call llm.ToolCall) string {
	if strings.HasPrefix(content, toolReceiptPrefix) {
		return content
	}
	what := "a tool call"
	if call.Function.Name != "" {
		what = toolCallLabel(call)
	}
	line, _ := checkpointEvidenceLine(content)
	return fmt.Sprintf("%s%s cleared to save context (%d characters). Key line: %s. Run the tool again to see it.]",
		toolReceiptPrefix, what, len(content), line)
}

// toolCallLabel names a tool call by what it acted on -- the path or the
// command -- rather than by its raw arguments, whose first characters are
// often a file's content.
func toolCallLabel(call llm.ToolCall) string {
	var args map[string]any
	if json.Unmarshal([]byte(call.Function.Arguments), &args) == nil {
		for _, key := range []string{"path", "command", "pattern", "query"} {
			if value, ok := args[key].(string); ok && value != "" {
				return call.Function.Name + " " + compactLine(value, 160)
			}
		}
	}
	return call.Function.Name + " " + compactLine(call.Function.Arguments, 160)
}

// checkpointBefore replaces original[head:cut] with a checkpoint summarizing it
// and keeps cleared[cut:], the same messages with older tool output cleared. The
// checkpoint keeps fewer entries until it is within maxChars. It returns nil
// when there is nothing to summarize.
func checkpointBefore(original, cleared []llm.Message, head, cut, maxChars int) []llm.Message {
	span := original[head:cut]
	task := ""
	if head > 0 && original[head-1].Role == "user" {
		// A summary an earlier compaction folded into the task is carried into
		// the new one and taken out of the task, so summaries never pile up there.
		var carried string
		if task, carried = splitTaskSummary(original[head-1].Content); carried != "" {
			span = append([]llm.Message{{Role: "user", Content: carried}}, span...)
		}
	}
	var checkpoint string
	for scale := 1; scale <= 8; scale *= 2 {
		checkpoint = scaledStateCheckpoint(span, scale)
		if len(checkpoint) <= maxChars {
			break
		}
	}
	if checkpoint == "" {
		return nil
	}
	summary := conversationSummaryOpen + "\n" + checkpoint + "\n" + conversationSummaryClose
	result := make([]llm.Message, 0, head+1+len(cleared)-cut)
	result = append(result, original[:head]...)
	if task != "" {
		result[head-1].Content = task
	}
	rest := append([]llm.Message(nil), cleared[cut:]...)
	// The summary is a user message, not a system one: Anthropic and Gemini have
	// a single system slot and hoist any later system message to the top, out of
	// order. It is folded into a neighbouring user message, never sent as one
	// of its own: two user messages in a row are rejected by strict-alternation
	// chat templates. When the cut falls before an assistant message, the
	// neighbour is the task, and the summary follows the task's own text.
	switch {
	case rest[0].Role == "user":
		rest[0].Content = summary + "\n\n" + rest[0].Content
	case head > 0 && result[head-1].Role == "user":
		result[head-1].Content = result[head-1].Content + "\n\n" + summary
	default:
		result = append(result, llm.Message{Role: "user", Content: summary})
	}
	return append(result, rest...)
}

// splitTaskSummary separates a task message from a summary folded in after its
// text by an earlier compaction.
func splitTaskSummary(content string) (task, summary string) {
	at := strings.LastIndex(content, "\n\n"+conversationSummaryOpen)
	if at < 0 || !strings.HasSuffix(content, conversationSummaryClose) {
		return content, ""
	}
	return content[:at], content[at+2:]
}

// withoutConversationSummary is a message's text with any compaction summary,
// leading or trailing, removed: what the user wrote, for display.
func withoutConversationSummary(content string) string {
	if _, rest := splitConversationSummary(content); rest != content {
		content = rest
	}
	task, _ := splitTaskSummary(content)
	return strings.TrimSpace(task)
}

const (
	conversationSummaryOpen  = "<conversation_summary>"
	conversationSummaryClose = "</conversation_summary>"
)

// isConversationSummary reports whether message begins with a compaction summary.
func isConversationSummary(message llm.Message) bool {
	return message.Role == "user" && strings.HasPrefix(message.Content, conversationSummaryOpen)
}

// splitConversationSummary separates a summary-bearing user message into the
// summary body and the user's own text that followed it.
func splitConversationSummary(content string) (summary, rest string) {
	if !strings.HasPrefix(content, conversationSummaryOpen) {
		return "", content
	}
	body := content[len(conversationSummaryOpen):]
	end := strings.Index(body, conversationSummaryClose)
	if end < 0 {
		return strings.TrimSpace(body), ""
	}
	return strings.TrimSpace(body[:end]), strings.TrimSpace(body[end+len(conversationSummaryClose):])
}

// compactionHead is how many leading messages compaction never touches: the
// system prompt, if present, and the first user message after it -- the task in
// a single run, the session's opening request in a replayed transcript.
func compactionHead(messages []llm.Message) int {
	head := 0
	for head < len(messages) && messages[head].Role == "system" {
		head++
	}
	if head < len(messages) && messages[head].Role == "user" {
		head++
	}
	return head
}

const checkpointHeader = "[State Checkpoint]\nThis deterministic checkpoint replaces completed older trajectory messages; the messages after it are verbatim.\n"

// maxCarriedSummaryChars bounds how much of an earlier summary a new one carries,
// so repeated compaction cannot grow the summary without limit.
const maxCarriedSummaryChars = 3000

// maxLatestRequestChars bounds the verbatim copy of the newest request.
const maxLatestRequestChars = 4000

// Checkpoint list sizes. Every list keeps its newest entries: in a long session
// the work just before the recent tail is the most likely to matter next.
const (
	checkpointRequests     = 12
	checkpointActions      = 16
	checkpointNotes        = 8
	checkpointFlagged      = 12 // errors, failures, exit codes, observation ids
	checkpointRoutine      = 4
	checkpointFilesChanged = 40
	checkpointFilesRead    = 20
)

func buildStateCheckpoint(messages []llm.Message) string {
	return scaledStateCheckpoint(messages, 1)
}

// scaledStateCheckpoint builds a checkpoint whose lists and carried text are cut
// to 1/scale of their full size, for budgets too small for the full checkpoint.
// The user's requests and the files changed are never cut this way: they are
// short, and the most costly to lose.
func scaledStateCheckpoint(messages []llm.Message, scale int) string {
	limit := func(n int) int {
		if n/scale < 1 {
			return 1
		}
		return n / scale
	}
	maxCarried, maxLatest := maxCarriedSummaryChars/scale, maxLatestRequestChars/scale
	var actions, flagged, routine, requests, notes, changed, read []string
	var earlier, latest string
	for _, message := range messages {
		switch message.Role {
		case "user":
			content := message.Content
			if summary, rest := splitConversationSummary(content); summary != "" {
				earlier = summary
				content = rest
			}
			if text := compactLine(content, 180); text != "" {
				requests = appendNewest(requests, text, checkpointRequests)
				latest = strings.TrimSpace(content)
			}
		case "assistant":
			if text := compactLine(message.Content, 200); text != "" {
				notes = appendNewest(notes, text, limit(checkpointNotes))
			}
			for _, call := range message.ToolCalls {
				actions = appendNewest(actions, toolCallLabel(call), limit(checkpointActions))
				switch path := toolCallPath(call); {
				case path == "":
				case call.Function.Name == "read_file":
					read = appendNewest(read, path, limit(checkpointFilesRead))
				case call.Function.Name == "write_file" || call.Function.Name == "edit_file" || call.Function.Name == "patch_file":
					changed = appendNewest(changed, path, checkpointFilesChanged)
				}
			}
		case "tool":
			if strings.HasPrefix(message.Content, toolReceiptPrefix) {
				// Already a receipt; its key line is the evidence.
				flagged = appendNewest(flagged, compactLine(message.Content, 260), limit(checkpointFlagged))
				continue
			}
			if text, important := checkpointEvidenceLine(message.Content); important {
				flagged = appendNewest(flagged, text, limit(checkpointFlagged))
			} else if text != "" {
				routine = appendNewest(routine, text, limit(checkpointRoutine))
			}
		}
	}
	if len(actions) == 0 && len(flagged) == 0 && len(routine) == 0 && len(requests) == 0 && len(notes) == 0 && earlier == "" {
		return ""
	}
	var builder strings.Builder
	builder.WriteString(checkpointHeader)
	if earlier != "" {
		earlier = strings.TrimSpace(strings.TrimPrefix(earlier, strings.TrimSpace(checkpointHeader)))
		// Keep both ends of a long earlier checkpoint: its start carries the
		// oldest context, its end the work closest to what follows.
		if len(earlier) > maxCarried {
			start := runeBoundary(earlier, maxCarried/3)
			end := runeBoundary(earlier, len(earlier)-(maxCarried-start))
			earlier = earlier[:start] + "\n...\n" + earlier[end:]
		}
		builder.WriteString("\nEarlier checkpoint:\n" + earlier + "\n")
	}
	writeCheckpointSection(&builder, "Requests", requests)
	// Request summaries are cut to 180 characters, which can lose detail in the
	// one that matters most: the newest.
	if latest != "" {
		if len(latest) > maxLatest {
			latest = latest[:runeBoundary(latest, maxLatest)] + "..."
		}
		builder.WriteString("\nLatest request (verbatim):\n" + latest + "\n")
	}
	writeCheckpointSection(&builder, "Assistant conclusions", notes)
	writeCheckpointSection(&builder, "Files changed (newest last)", changed)
	writeCheckpointSection(&builder, "Files read (newest last)", read)
	writeCheckpointSection(&builder, "Actions", actions)
	writeCheckpointSection(&builder, "Evidence and receipts", append(routine, flagged...))
	return strings.TrimSpace(builder.String())
}

// toolCallPath is the path a file tool call acted on, or "".
func toolCallPath(call llm.ToolCall) string {
	var args struct {
		Path string `json:"path"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
		return ""
	}
	return strings.TrimSpace(args.Path)
}

func checkpointEvidence(content string) string {
	text, _ := checkpointEvidenceLine(content)
	return text
}

// checkpointEvidenceLine picks the line of a tool result most worth keeping,
// and reports whether it is flagged (an error, failure, exit code, success,
// digest or observation id) rather than just the output's first characters.
func checkpointEvidenceLine(content string) (string, bool) {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.Contains(trimmed, "obs_") || strings.Contains(lower, "error") || strings.Contains(lower, "fail") || strings.Contains(lower, "success") || strings.Contains(lower, "exit code") || strings.Contains(lower, "sha-256") {
			return compactLine(trimmed, 220), true
		}
	}
	return compactLine(content, 140), false
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

// appendNewest appends value, moving it to the end if already present, and
// drops the oldest entries beyond limit.
func appendNewest(values []string, value string, limit int) []string {
	for i, existing := range values {
		if existing == value {
			values = append(values[:i:i], values[i+1:]...)
			break
		}
	}
	values = append(values, value)
	if len(values) > limit {
		values = values[len(values)-limit:]
	}
	return values
}

func compactLine(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > limit {
		value = value[:runeBoundary(value, limit-3)] + "..."
	}
	return value
}

func messageCharacterCount(messages []llm.Message) int {
	total := 0
	for _, message := range messages {
		total += len(message.Content)
		// Tool-call arguments are sent to the model too; a write_file call
		// carries a whole file in them. countApproxTokens counts them the same way.
		for _, call := range message.ToolCalls {
			total += len(call.Function.Name) + len(call.Function.Arguments)
		}
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

	head := compactionHead(messages)
	if messageCharacterCount(messages) <= cfg.MaxTotalChars || len(messages) <= cfg.KeepRecentMessages+head {
		return messages, false
	}

	result := make([]llm.Message, len(messages))
	copy(result, messages)

	cutoff := len(messages) - cfg.KeepRecentMessages
	prunedAny := false

	// Iterate older messages, skipping the preserved head
	for i := head; i < cutoff; i++ {
		m := &result[i]
		if m.Role == "tool" && len(m.Content) > cfg.MaxToolOutputChars {
			headLen := cfg.MaxToolOutputChars / 2
			tailLen := cfg.MaxToolOutputChars / 4
			headEnd := runeBoundary(m.Content, headLen)
			tailStart := runeBoundary(m.Content, len(m.Content)-tailLen)
			truncatedBytes := tailStart - headEnd

			head := m.Content[:headEnd]
			tail := m.Content[tailStart:]
			m.Content = fmt.Sprintf("%s\n\n... [Truncated %d characters of older tool output for context budget] ...\n\n%s", head, truncatedBytes, tail)
			prunedAny = true
		}
	}

	return result, prunedAny
}
