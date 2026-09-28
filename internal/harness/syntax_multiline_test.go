package harness

import (
	"strings"
	"testing"
)

func TestLexLinesCarriesBlockCommentAcrossLines(t *testing.T) {
	lines := []string{"x := 1 /* start", "still comment", "end */ y := 2", "z := 3"}
	got := LexLines(lines, "go")

	if len(got[1]) != 1 || got[1][0].Type != TokenComment {
		t.Errorf("line inside block comment = %+v, want one comment token", got[1])
	}
	if got[2][0].Type != TokenComment || got[2][0].Value != "end */" {
		t.Errorf("closing line starts with %+v, want the comment tail", got[2][0])
	}
	var sawKeywordOrIdent bool
	for _, tok := range got[2][1:] {
		if tok.Type == TokenComment {
			t.Errorf("code after */ lexed as comment: %+v", tok)
		}
		if strings.TrimSpace(tok.Value) == "y" {
			sawKeywordOrIdent = true
		}
	}
	if !sawKeywordOrIdent {
		t.Errorf("code after */ missing: %+v", got[2])
	}
	for _, tok := range got[3] {
		if tok.Type == TokenComment {
			t.Errorf("line after the comment closed still lexed as comment: %+v", got[3])
		}
	}
}

func TestLexLinesCarriesRawStringAndTripleQuotes(t *testing.T) {
	goLines := LexLines([]string{"s := `first", "middle", "last`", "n := 1"}, "go")
	if goLines[1][0].Type != TokenString || goLines[2][0].Type != TokenString {
		t.Errorf("raw string body not lexed as string: %+v / %+v", goLines[1], goLines[2])
	}
	if goLines[3][0].Type == TokenString {
		t.Errorf("line after raw string closed still a string: %+v", goLines[3])
	}

	py := LexLines([]string{`doc = """open`, "inside", `close"""`, "x = 1"}, "python")
	if py[1][0].Type != TokenString {
		t.Errorf("docstring body not lexed as string: %+v", py[1])
	}
	if py[3][0].Type == TokenString {
		t.Errorf("line after docstring still a string: %+v", py[3])
	}
}

func TestHighlightCodeLineBgRestoresBackgroundAfterEachToken(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	const bg = "\033[48;2;1;2;3m"
	out := HighlightCodeLineBg(`func main() { return "x" }`, "go", bg)
	resets := strings.Count(out, ansiReset)
	if resets == 0 {
		t.Fatalf("expected coloured tokens, got %q", out)
	}
	if got := strings.Count(out, ansiReset+bg); got != resets {
		t.Errorf("%d of %d resets are followed by the background", got, resets)
	}
	if StripANSI(out) != `func main() { return "x" }` {
		t.Errorf("text changed: %q", StripANSI(out))
	}
}
