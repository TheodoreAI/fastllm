package harness

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Text from the model, from tools, and from the programs they run is
// untrusted: it can carry terminal escape sequences (cursor moves that
// overwrite what is on screen, OSC 52 clipboard writes, window-title
// changes), carriage returns that hide the start of a line, and invisible
// Unicode (bidi overrides, tag characters) that makes text read differently
// from what it is. It is cleaned here, before the UI styles it, so our own
// escape sequences are never confused with its.
//
// I7 depends on this: an approval prompt must show the effect being approved,
// so consent text hides nothing and marks everything it cannot print.

type sanitizeMode int

const (
	// stripUntrusted removes escapes and controls; for model and tool text.
	stripUntrusted sanitizeMode = iota
	// keepColour is stripUntrusted but keeps SGR colour sequences, which
	// cannot move the cursor or reach the clipboard; for command output.
	keepColour
	// revealAll prints every non-printing character as a visible marker; for
	// approval prompts, where silently dropping one would misstate the effect.
	revealAll
)

// sanitizeUntrusted cleans model and tool text for display.
func sanitizeUntrusted(s string) string { return sanitizeText(s, stripUntrusted) }

// sanitizeOutput cleans program output for display, keeping its colours.
func sanitizeOutput(s string) string { return sanitizeText(s, keepColour) }

// revealHidden renders s for an approval prompt and reports whether it held
// anything that does not print as itself.
func revealHidden(s string) (string, bool) {
	out := sanitizeText(s, revealAll)
	return out, out != s
}

func sanitizeText(s string, mode sanitizeMode) string {
	if isPlainText(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			end, sgr := scanEscape(s, i)
			switch {
			case mode == revealAll:
				b.WriteString(visible(s[i:end]))
			case mode == keepColour && sgr:
				b.WriteString(s[i:end])
			}
			i = end
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r == '\r':
			if i < len(s) && s[i] == '\n' {
				continue // CRLF is just a newline
			}
			// A lone CR returns to column 0 and lets later text overwrite
			// what came before it on the line.
			if mode == revealAll {
				b.WriteString("⟨CR⟩")
			} else {
				b.WriteByte('\n')
			}
		case r == utf8.RuneError && size == 1:
			b.WriteRune(utf8.RuneError)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			// C0 and C1 controls, including the one-byte CSI (U+009B).
			if mode == revealAll {
				fmt.Fprintf(&b, "⟨U+%04X⟩", r)
			}
		case r == ' ' || r == ' ':
			b.WriteByte('\n')
		case isDeceptiveRune(r):
			// Visible everywhere: these reorder or hide text, and have no
			// legitimate use in a transcript or a command.
			fmt.Fprintf(&b, "⟨U+%04X⟩", r)
		case mode == revealAll && isInvisibleRune(r):
			fmt.Fprintf(&b, "⟨U+%04X⟩", r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isPlainText is the fast path: nothing below needs changing.
func isPlainText(s string) bool {
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\t' || r >= 0x7f && r <= 0x9f ||
			r == utf8.RuneError || r == ' ' || r == ' ' ||
			isDeceptiveRune(r) || isInvisibleRune(r) {
			return false
		}
	}
	return true
}

// isDeceptiveRune reports bidi embeddings, overrides, and isolates, which
// display text in an order other than the one it runs in, and Unicode tag
// characters, which are invisible and used to smuggle instructions.
func isDeceptiveRune(r rune) bool {
	return r >= 0x202A && r <= 0x202E || r >= 0x2066 && r <= 0x2069 ||
		r >= 0xE0000 && r <= 0xE007F
}

// isInvisibleRune reports zero-width and direction marks. They are harmless
// in prose (emoji use the zero-width joiner) but not in something approved.
func isInvisibleRune(r rune) bool {
	switch r {
	case 0x200B, 0x200C, 0x200D, 0x200E, 0x200F, 0x2060, 0x061C, 0x180E, 0xFEFF:
		return true
	}
	return false
}

// scanEscape returns the end of the escape sequence starting at s[i] (ESC)
// and whether it is an SGR colour sequence. An unterminated sequence runs to
// the end of s, so a truncated one cannot leak its tail.
func scanEscape(s string, i int) (end int, sgr bool) {
	if i+1 >= len(s) {
		return len(s), false
	}
	switch s[i+1] {
	case '[': // CSI: parameters, intermediates, one final byte 0x40-0x7E.
		j := i + 2
		for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
			j++
		}
		if j >= len(s) {
			return len(s), false
		}
		params := s[i+2 : j]
		return j + 1, s[j] == 'm' && strings.Trim(params, "0123456789;:") == ""
	case ']', 'P', 'X', '^', '_': // OSC, DCS, SOS, PM, APC: to BEL or ST.
		for j := i + 2; j < len(s); j++ {
			if s[j] == 0x07 {
				return j + 1, false
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2, false
			}
		}
		return len(s), false
	default: // Two-byte sequences such as ESC c (reset) or ESC 7.
		_, size := utf8.DecodeRuneInString(s[i+1:])
		return i + 1 + size, false
	}
}

// visible spells out an escape sequence for an approval prompt.
func visible(seq string) string {
	var b strings.Builder
	for _, r := range seq {
		switch {
		case r == 0x1b:
			b.WriteString("⟨ESC⟩")
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, "⟨U+%04X⟩", r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
