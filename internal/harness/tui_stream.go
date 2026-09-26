package harness

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// streamBuffer accumulates streamed assistant text for one turn and decides how
// much of it is safe to render.
//
// Rendering goes through FormatMarkdownWidth, which is line-based and tracks
// code-fence state. Handing it a half-written line means rendering a fence that
// has not closed yet, or a heading marker with no text, and the result rewrites
// itself as the rest arrives. So only whole lines are ever committed; the
// trailing partial line is held back until its newline shows up.
//
// The buffer also holds every committed line, because a turn's text can be
// retracted wholesale: a model that wrote its tool call as prose streamed that
// JSON as visible text, and a failed stream is retried from the beginning.
type streamBuffer struct {
	pending   strings.Builder // text not yet ending in a newline
	committed strings.Builder // whole lines already rendered
	active    bool
}

// Start begins a streamed turn, discarding anything left from a previous one.
func (b *streamBuffer) Start() {
	b.pending.Reset()
	b.committed.Reset()
	b.active = true
}

// Active reports whether a streamed turn is in progress.
func (b *streamBuffer) Active() bool { return b.active }

// Add folds in a fragment and returns whichever whole lines are now renderable,
// or "" when the fragment did not complete a line.
func (b *streamBuffer) Add(fragment string) string {
	if fragment == "" {
		return ""
	}
	b.active = true
	b.pending.WriteString(fragment)
	held := b.pending.String()
	cut := strings.LastIndexByte(held, '\n')
	if cut < 0 {
		return ""
	}
	ready := held[:cut+1]
	b.pending.Reset()
	b.pending.WriteString(held[cut+1:])
	b.committed.WriteString(ready)
	return ready
}

// Flush returns any held partial line at end of stream and closes the turn. The
// final line of an answer usually arrives without a trailing newline, so without
// this the last line would never be shown.
func (b *streamBuffer) Flush() string {
	remainder := b.pending.String()
	b.pending.Reset()
	if remainder != "" {
		b.committed.WriteString(remainder)
	}
	b.active = false
	return remainder
}

// Committed is everything rendered so far for this turn.
func (b *streamBuffer) Committed() string { return b.committed.String() }

// Discard drops the turn's text and reports what had been rendered, so the
// caller can remove exactly that much from the transcript.
func (b *streamBuffer) Discard() string {
	shown := b.committed.String()
	b.pending.Reset()
	b.committed.Reset()
	b.active = false
	return shown
}

// trimSuffixFromHistory removes a previously appended block from the end of the
// transcript. Used when streamed text is retracted: the text is the most recent
// thing written, so trimming the suffix restores the transcript exactly.
func trimSuffixFromHistory(history, shown string) string {
	if shown == "" {
		return history
	}
	if idx := strings.LastIndex(history, shown); idx >= 0 && idx+len(shown) == len(history) {
		return history[:idx]
	}
	return history
}

// streamAnswerHeader is the ASSISTANT label shown once before streamed text.
// formatAssistantAnswer cannot be reused because it renders a whole answer at
// once; a stream needs the label first and the body in pieces.
func streamAnswerHeader() string {
	return "\n" + lipgloss.NewStyle().Bold(true).Foreground(tuiColorGreen).Render(SymBullet+" ASSISTANT") + "\n"
}

// retractStreamedText removes this turn's streamed output from the transcript,
// including the ASSISTANT header, and rewinds the buffer.
func (m *teaModel) retractStreamedText() {
	shown := m.stream.Discard()
	if shown == "" && !m.streamHeaderShown {
		return
	}
	// The rendered form is what actually reached the transcript, so trim that
	// rather than the raw text the buffer holds.
	history := m.historyText.String()
	if shown != "" {
		history = trimSuffixFromHistory(history, FormatMarkdownWidth(shown, m.contentWidth())+"\n")
	}
	if m.streamHeaderShown {
		history = trimSuffixFromHistory(history, streamAnswerHeader())
		m.streamHeaderShown = false
	}
	m.historyText.Reset()
	m.historyText.WriteString(history)
	if m.ready {
		m.viewport.SetContent(history)
		m.viewport.GotoBottom()
	}
}
