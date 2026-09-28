package harness

import "strings"

// LexLine sees one line at a time, so a block comment or multi-line string
// that opens on one line would leave the lines after it lexed as code. The
// helpers here carry that one piece of state from line to line, which is
// all a whole-file view (the editor, the side-by-side diff) needs.

// lexState is what an unclosed construct left open at the end of a line:
// the delimiter that closes it and the token type of everything until then.
type lexState struct {
	closer  string
	tokType TokenType
}

// lexLineState lexes line starting in state st and returns the state the
// next line starts in.
func lexLineState(line, lang string, st lexState) ([]Token, lexState) {
	var tokens []Token
	rest := line
	if st.closer != "" {
		idx := strings.Index(rest, st.closer)
		if idx < 0 {
			if line == "" {
				return nil, st
			}
			return []Token{{Type: st.tokType, Value: line}}, st
		}
		end := idx + len(st.closer)
		tokens = append(tokens, Token{Type: st.tokType, Value: rest[:end]})
		rest = rest[end:]
	}
	restTokens := LexLine(rest, lang)
	return append(tokens, restTokens...), openedState(restTokens, strings.ToLower(lang))
}

// openedState reports the construct, if any, that the last token of a line
// opened without closing. Only the last token can: an unclosed comment or
// string runs to the end of the line.
func openedState(tokens []Token, lang string) lexState {
	if len(tokens) == 0 {
		return lexState{}
	}
	last := tokens[len(tokens)-1]
	switch lang {
	case "go", "javascript", "typescript", "rust", "c", "cpp":
		if last.Type == TokenComment && strings.HasPrefix(last.Value, "/*") && !strings.Contains(last.Value[2:], "*/") {
			return lexState{closer: "*/", tokType: TokenComment}
		}
		if lang != "rust" && lang != "c" && lang != "cpp" && last.Type == TokenString &&
			strings.HasPrefix(last.Value, "`") && (len(last.Value) == 1 || !strings.HasSuffix(last.Value, "`")) {
			return lexState{closer: "`", tokType: TokenString}
		}
	case "python":
		if last.Type == TokenString {
			for _, q := range []string{`"""`, `'''`} {
				if strings.HasPrefix(last.Value, q) && (len(last.Value) < 6 || !strings.HasSuffix(last.Value, q)) {
					return lexState{closer: q, tokType: TokenString}
				}
			}
		}
	}
	return lexState{}
}

// LexLines tokenizes a whole file's lines, carrying block comments and
// multi-line strings across line breaks.
func LexLines(lines []string, lang string) [][]Token {
	out := make([][]Token, len(lines))
	var st lexState
	for i, line := range lines {
		out[i], st = lexLineState(line, lang, st)
	}
	return out
}

// HighlightCodeLines highlights a whole file's lines; see LexLines.
func HighlightCodeLines(lines []string, lang string) []string {
	out := make([]string, len(lines))
	if !ColorsEnabled() {
		copy(out, lines)
		return out
	}
	for i, tokens := range LexLines(lines, lang) {
		out[i] = HighlightTokens(tokens, "")
	}
	return out
}
