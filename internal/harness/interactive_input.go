package harness

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/reeflective/readline"
)

type interactiveInput interface {
	ReadLine(prompt string) (string, error)
	Close() error
}

type scannerInput struct{ scanner *bufio.Scanner }

func (s *scannerInput) ReadLine(prompt string) (string, error) {
	fmt.Print(prompt)
	if !s.scanner.Scan() {
		if err := s.scanner.Err(); err != nil {
			return "", err
		}
		return "", fmt.Errorf("end of input")
	}
	return s.scanner.Text(), nil
}
func (s *scannerInput) Close() error { return nil }

type readlineInput struct {
	shell  *readline.Shell
	prompt string
}

func newInteractiveInput(historyPath string, complete func(string, int) []string) interactiveInput {
	if !isatty.IsTerminal(os.Stdin.Fd()) && !isatty.IsCygwinTerminal(os.Stdin.Fd()) {
		return &scannerInput{scanner: bufio.NewScanner(os.Stdin)}
	}
	rl := readline.NewShell()
	in := &readlineInput{shell: rl}
	rl.Prompt.Primary(func() string { return in.prompt })
	rl.Prompt.Secondary(func() string { return ColorGray("... ") })
	rl.AcceptMultiline = inputComplete
	rl.Completer = func(line []rune, cursor int) readline.Completions {
		return readline.CompleteValues(complete(string(line), cursor)...)
	}
	if err := os.MkdirAll(filepath.Dir(historyPath), 0o700); err == nil {
		if history, err := readline.NewHistoryFromFile(historyPath); err == nil {
			rl.History.Add("fastllm", history)
		}
	}
	return in
}

func (r *readlineInput) ReadLine(prompt string) (string, error) {
	r.prompt = prompt
	return r.shell.Readline()
}
func (r *readlineInput) Close() error { return nil }

func inputComplete(line []rune) bool {
	text := strings.TrimSpace(string(line))
	if text == "" {
		return true
	}
	var braces, brackets, parens int
	var quote rune
	escaped := false
	for _, ch := range line {
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' || ch == '`' {
			quote = ch
			continue
		}
		switch ch {
		case '{':
			braces++
		case '}':
			braces--
		case '[':
			brackets++
		case ']':
			brackets--
		case '(':
			parens++
		case ')':
			parens--
		}
	}
	return quote == 0 && braces <= 0 && brackets <= 0 && parens <= 0 && !strings.HasSuffix(text, "\\")
}

var slashCommands = []string{"/clear", "/cls", "/delete-session", "/diff", "/dir", "/exit", "/fetch", "/help", "/image", "/kill", "/model", "/models", "/new", "/ps", "/quit", "/rename", "/resume", "/rules", "/search", "/session", "/sessions", "/set", "/shell", "/status", "/undo"}

func interactiveCompletions(line string, cursor int, cwd string, models, sessions []string) []string {
	if cursor < 0 || cursor > len(line) {
		cursor = len(line)
	}
	prefix := line[:cursor]
	fields := strings.Fields(prefix)
	if strings.HasPrefix(prefix, "/") && len(fields) <= 1 && !strings.HasSuffix(prefix, " ") {
		return filterCompletionValues(slashCommands, fields[0])
	}
	if len(fields) > 0 && (fields[0] == "/model" || fields[0] == "/models") {
		return filterCompletionValues(models, lastField(prefix))
	}
	if len(fields) > 0 && (fields[0] == "/resume" || fields[0] == "/delete-session" || fields[0] == "/session") {
		return filterCompletionValues(sessions, lastField(prefix))
	}
	if strings.HasPrefix(prefix, "/dir ") || strings.HasPrefix(prefix, "!cd ") || strings.HasPrefix(prefix, "$ cd ") {
		return completePaths(cwd, lastField(prefix))
	}
	return nil
}

func lastField(s string) string {
	if strings.HasSuffix(s, " ") {
		return ""
	}
	parts := strings.Fields(s)
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}
func filterCompletionValues(values []string, prefix string) []string {
	var out []string
	for _, value := range values {
		if strings.HasPrefix(strings.ToLower(value), strings.ToLower(prefix)) {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}
func completePaths(cwd, typed string) []string {
	base, leaf := filepath.Split(typed)
	dir := base
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(cwd, dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		if !strings.HasPrefix(strings.ToLower(entry.Name()), strings.ToLower(leaf)) {
			continue
		}
		candidate := base + entry.Name()
		if entry.IsDir() {
			candidate += string(filepath.Separator)
		}
		out = append(out, candidate)
	}
	return out
}
