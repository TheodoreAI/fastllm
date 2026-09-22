package harness

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
)

// Only whole lines may be rendered: FormatMarkdownWidth tracks code-fence state
// line by line, so a half-written line would render as something that rewrites
// itself once the rest arrives.
func TestStreamBufferCommitsWholeLinesOnly(t *testing.T) {
	var b streamBuffer
	b.Start()

	if got := b.Add("Here is "); got != "" {
		t.Fatalf("partial line was released: %q", got)
	}
	if got := b.Add("a line"); got != "" {
		t.Fatalf("still-partial line was released: %q", got)
	}
	if got := b.Add("\nand more"); got != "Here is a line\n" {
		t.Fatalf("completed line = %q", got)
	}
	// "and more" has no newline yet, so it stays held until Flush.
	if got := b.Flush(); got != "and more" {
		t.Fatalf("flush = %q, want the held final line", got)
	}
	if b.Active() {
		t.Fatal("buffer still active after flush")
	}
}

// A fragment carrying several newlines releases all complete lines at once.
func TestStreamBufferReleasesMultipleLines(t *testing.T) {
	var b streamBuffer
	b.Start()
	got := b.Add("one\ntwo\nthree")
	if got != "one\ntwo\n" {
		t.Fatalf("released = %q, want the two complete lines", got)
	}
	if got := b.Flush(); got != "three" {
		t.Fatalf("flush = %q", got)
	}
}

// A code fence must not be released until its line is complete, otherwise the
// renderer sees an unterminated fence.
func TestStreamBufferHoldsIncompleteFenceLine(t *testing.T) {
	var b streamBuffer
	b.Start()
	if got := b.Add("```"); got != "" {
		t.Fatalf("bare fence prefix released early: %q", got)
	}
	if got := b.Add("go"); got != "" {
		t.Fatalf("fence language released early: %q", got)
	}
	if got := b.Add("\n"); got != "```go\n" {
		t.Fatalf("completed fence line = %q", got)
	}
}

// Discard reports exactly what was committed, so the caller can retract that much.
func TestStreamBufferDiscardReturnsCommittedText(t *testing.T) {
	var b streamBuffer
	b.Start()
	b.Add("visible line\n")
	b.Add("held partial")

	shown := b.Discard()
	if shown != "visible line\n" {
		t.Fatalf("discard reported %q, want only the committed line", shown)
	}
	if b.Active() || b.Committed() != "" {
		t.Fatal("discard did not reset the buffer")
	}
}

// Start must clear a previous turn's leftovers so text cannot bleed across turns.
func TestStreamBufferStartResetsPreviousTurn(t *testing.T) {
	var b streamBuffer
	b.Start()
	b.Add("first turn\n")
	b.Start()
	if b.Committed() != "" {
		t.Fatalf("committed text survived restart: %q", b.Committed())
	}
	if got := b.Add("second\n"); got != "second\n" {
		t.Fatalf("second turn = %q", got)
	}
}

func TestTrimSuffixFromHistoryRemovesOnlyTrailingBlock(t *testing.T) {
	history := "earlier output\nstreamed answer\n"
	if got := trimSuffixFromHistory(history, "streamed answer\n"); got != "earlier output\n" {
		t.Fatalf("trim = %q", got)
	}
	// A block that is present but not at the end must be left alone: retraction
	// only ever removes the most recent thing written.
	history2 := "streamed answer\nlater output\n"
	if got := trimSuffixFromHistory(history2, "streamed answer\n"); got != history2 {
		t.Fatalf("non-trailing block was removed: %q", got)
	}
	if got := trimSuffixFromHistory(history, ""); got != history {
		t.Fatalf("empty trim changed history: %q", got)
	}
}

// End-to-end through the model: streamed deltas render once, and the duplicate
// full Response on EventTurnComplete must not be appended a second time.
func TestTokenDeltasRenderOnceNotTwice(t *testing.T) {
	m := newStreamTestModel()
	m.stream.Start()

	for _, frag := range []string{"Hello ", "there\n", "second line"} {
		updated, _ := m.Update(teaAgentEventMsg(Event{Type: EventTokenDelta, Response: frag}))
		m = updated.(*teaModel)
	}
	updated, _ := m.Update(teaAgentEventMsg(Event{
		Type:     EventTurnComplete,
		Response: "Hello there\nsecond line",
	}))
	m = updated.(*teaModel)

	plain := StripANSI(m.historyText.String())
	if n := strings.Count(plain, "second line"); n != 1 {
		t.Fatalf("streamed text rendered %d times, want exactly 1:\n%s", n, plain)
	}
	if n := strings.Count(plain, "Hello there"); n != 1 {
		t.Fatalf("first line rendered %d times, want exactly 1:\n%s", n, plain)
	}
	if m.lastResponse != "Hello there\nsecond line" {
		t.Fatalf("lastResponse = %q", m.lastResponse)
	}
}

// A discard (tool call written as prose, or a stream that died before retry)
// must leave no trace of the streamed text in the transcript.
func TestTokenDiscardRetractsStreamedText(t *testing.T) {
	m := newStreamTestModel()
	m.appendHistory("earlier output\n")
	before := m.historyText.String()
	m.stream.Start()

	for _, frag := range []string{`{"name":`, `"write_file"}`, "\n"} {
		updated, _ := m.Update(teaAgentEventMsg(Event{Type: EventTokenDelta, Response: frag}))
		m = updated.(*teaModel)
	}
	if !strings.Contains(m.historyText.String(), "write_file") {
		t.Fatal("fixture did not actually stream anything to retract")
	}

	updated, _ := m.Update(teaAgentEventMsg(Event{Type: EventTokenDiscard}))
	m = updated.(*teaModel)

	if got := m.historyText.String(); got != before {
		t.Fatalf("discard did not restore the transcript.\n got: %q\nwant: %q", got, before)
	}
	if m.streamHeaderShown {
		t.Fatal("assistant header survived the discard")
	}
}

// newStreamTestModel builds a model wired enough to render streamed events.
func newStreamTestModel() *teaModel {
	ta := textarea.New()
	return &teaModel{
		input:    ta,
		viewport: viewport.New(80, 10),
		ready:    true,
		width:    80,
	}
}
