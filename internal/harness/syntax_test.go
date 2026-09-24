package harness

import (
	"strings"
	"testing"
)

func TestDetectLanguage(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"main.go", "go"},
		{"internal/harness/tui_style.go", "go"},
		{"src/app.tsx", "typescript"},
		{"index.js", "javascript"},
		{"script.py", "python"},
		{"src/lib.rs", "rust"},
		{"config.json", "json"},
		{"deploy.yaml", "yaml"},
		{"run.sh", "shell"},
		{"schema.sql", "sql"},
		{"README.md", "markdown"},
		{"unknown.xyz", ""},
	}

	for _, tt := range tests {
		got := DetectLanguage(tt.path)
		if got != tt.want {
			t.Errorf("DetectLanguage(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestLexLineGo(t *testing.T) {
	line := `func hello(msg string) int { return 42 } // a comment`
	tokens := LexLine(line, "go")

	var keywords []string
	var stringsLit []string
	var comments []string
	var numbers []string
	var types []string

	for _, tok := range tokens {
		switch tok.Type {
		case TokenKeyword:
			keywords = append(keywords, tok.Value)
		case TokenString:
			stringsLit = append(stringsLit, tok.Value)
		case TokenComment:
			comments = append(comments, tok.Value)
		case TokenNumber:
			numbers = append(numbers, tok.Value)
		case TokenTypeIdent:
			types = append(types, tok.Value)
		}
	}

	if len(keywords) < 2 || keywords[0] != "func" || keywords[1] != "return" {
		t.Errorf("unexpected keywords: %v", keywords)
	}
	if len(types) < 2 || types[0] != "string" || types[1] != "int" {
		t.Errorf("unexpected types: %v", types)
	}
	if len(numbers) != 1 || numbers[0] != "42" {
		t.Errorf("unexpected numbers: %v", numbers)
	}
	if len(comments) != 1 || !strings.Contains(comments[0], "a comment") {
		t.Errorf("unexpected comments: %v", comments)
	}
}

func TestHighlightCodeLine(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	line := `const timeout = 10 * time.Second`
	highlighted := HighlightCodeLine(line, "go")
	if !strings.Contains(highlighted, "timeout") {
		t.Errorf("highlighted line missing identifier: %s", highlighted)
	}
	if !strings.Contains(highlighted, "\033[") {
		t.Errorf("expected ANSI escape codes in highlighted line: %s", highlighted)
	}
}
