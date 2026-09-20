package harness

import (
	"fmt"
	"os"
	"strings"

	"github.com/mattn/go-isatty"
	"golang.org/x/term"
)

// Choice is one selectable option in an inline chooser.
type Choice struct {
	// Key is the typed shortcut that selects this option directly, preserving
	// muscle memory for anyone used to answering y/n/a.
	Key   rune
	Label string
	Value string
}

// chooseSupported reports whether a keypress-driven menu can run. It needs stdin
// for raw keys and stdout for redrawing, so both must be a terminal.
func chooseSupported() bool {
	if os.Getenv("FASTLLM_NO_MENU") != "" {
		return false
	}
	in, out := os.Stdin.Fd(), os.Stdout.Fd()
	return (isatty.IsTerminal(in) || isatty.IsCygwinTerminal(in)) &&
		(isatty.IsTerminal(out) || isatty.IsCygwinTerminal(out))
}

// Choose renders an inline menu and returns the chosen value. Left/right or
// up/down move the highlight, Enter accepts it, a shortcut key selects directly,
// and Esc or Ctrl-C cancels.
//
// ok is false when no menu could be shown (not a terminal, raw mode refused) or
// the user cancelled; callers fall back to a typed prompt so the flow still works
// over a pipe or in a dumb terminal.
func Choose(prompt string, choices []Choice, selected int) (value string, ok bool) {
	if len(choices) == 0 || !chooseSupported() {
		return "", false
	}
	if selected < 0 || selected >= len(choices) {
		selected = 0
	}

	state, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return "", false
	}
	defer term.Restore(int(os.Stdin.Fd()), state)

	draw := func() {
		var b strings.Builder
		b.WriteString("\r\033[2K  ")
		b.WriteString(prompt)
		b.WriteString("  ")
		for i, c := range choices {
			if i > 0 {
				b.WriteString("  ")
			}
			if i == selected {
				b.WriteString(ColorInverse(" " + c.Label + " "))
			} else {
				b.WriteString(ColorGray(" " + c.Label + " "))
			}
		}
		b.WriteString(ColorGray("   ↔ move · enter select"))
		fmt.Print(b.String())
	}
	draw()

	buf := make([]byte, 8)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			fmt.Print("\r\033[2K")
			return "", false
		}
		key := buf[:n]

		// Arrow keys arrive as ESC [ A/B/C/D.
		if n >= 3 && key[0] == 27 && key[1] == '[' {
			switch key[2] {
			case 'C', 'B': // right, down
				selected = (selected + 1) % len(choices)
			case 'D', 'A': // left, up
				selected = (selected - 1 + len(choices)) % len(choices)
			}
			draw()
			continue
		}

		switch key[0] {
		case '\r', '\n':
			fmt.Print("\r\033[2K")
			return choices[selected].Value, true
		case 3, 27: // Ctrl-C, bare Esc
			fmt.Print("\r\033[2K")
			return "", false
		case '\t':
			selected = (selected + 1) % len(choices)
			draw()
			continue
		}

		typed := strings.ToLower(string(key[0]))
		for _, c := range choices {
			if typed == strings.ToLower(string(c.Key)) {
				fmt.Print("\r\033[2K")
				return c.Value, true
			}
		}
	}
}
