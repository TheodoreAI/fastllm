package harness

import (
	"strings"
	"testing"
	"time"

	"fastllm/internal/config"
)

func TestTUIStyleBasic(t *testing.T) {
	// Test ANSI stripping and visual length
	styled := ColorCyan(StyleBold("test-string"))
	if VisualLen(styled) != 11 {
		t.Errorf("expected visual len 11, got %d", VisualLen(styled))
	}
	if StripANSI(styled) != "test-string" {
		t.Errorf("expected stripped 'test-string', got %q", StripANSI(styled))
	}

	// Test PadRight
	padded := PadRight(styled, 15)
	if VisualLen(padded) != 15 {
		t.Errorf("expected visual len 15, got %d", VisualLen(padded))
	}

	// Test FormatPrompt
	prompt := FormatPrompt("llama3.1")
	if !strings.Contains(prompt, "llama3.1") || !strings.Contains(prompt, "fastllm") {
		t.Errorf("unexpected prompt output: %s", prompt)
	}

	// Test FormatDivider
	div := FormatDivider("test label", 40)
	if !strings.Contains(div, "test label") {
		t.Errorf("expected divider to contain label: %s", div)
	}
}

func TestFormatCard(t *testing.T) {
	lines := []string{
		"Line 1",
		"Line 2 is longer than Line 1",
	}
	card := FormatCard("Test Card", lines, 60)
	if !strings.Contains(card, "Test Card") {
		t.Errorf("card missing title: %s", card)
	}
	if !strings.Contains(card, "Line 1") {
		t.Errorf("card missing content: %s", card)
	}
	if !strings.Contains(card, SymCornerTL) || !strings.Contains(card, SymCornerBR) {
		t.Errorf("card missing corners: %s", card)
	}
}

func TestFormatWelcomeBanner(t *testing.T) {
	banner := FormatWelcomeBanner("/test/dir", "example-model", "/test/config.json", true, 2, true)
	if !strings.Contains(banner, "/test/dir") {
		t.Errorf("banner missing dir: %s", banner)
	}
	if !strings.Contains(banner, "example-model") {
		t.Errorf("banner missing model: %s", banner)
	}
	if !strings.Contains(banner, "AGENTS.md") {
		t.Errorf("banner missing rules text: %s", banner)
	}
}

func TestFormatWelcomeBannerAlignment(t *testing.T) {
	banner := FormatWelcomeBanner("C:\\Users\\mateo\\go\\fastllm", "muse-glimmer", "C:\\Users\\mateo\\.fastllm\\config.json", true, 0, true)
	lines := strings.Split(strings.TrimSpace(banner), "\n")
	if len(lines) < 5 {
		t.Fatalf("banner has too few lines: %d", len(lines))
	}
	expectedWidth := VisualLen(lines[0])
	for i, line := range lines {
		vLen := VisualLen(line)
		if vLen != expectedWidth {
			t.Errorf("line %d has mismatched visual width: got %d, expected %d\nLine: %q", i, vLen, expectedWidth, line)
		}
	}
}

func TestFormatToolCallAndResult(t *testing.T) {
	call := FormatToolCall("read_file", "path=main.go")
	if !strings.Contains(call, "read_file") || !strings.Contains(call, "path=main.go") {
		t.Errorf("unexpected tool call render: %s", call)
	}

	res := FormatToolResult("read_file", "line 1\nline 2\nline 3\nline 4\nline 5", 3)
	if !strings.Contains(res, "line 1") || !strings.Contains(res, "lines output") {
		t.Errorf("unexpected tool result render: %s", res)
	}
}

func TestHighlightDiff(t *testing.T) {
	diff := "diff --git a/file b/file\n--- a/file\n+++ b/file\n@@ -1,2 +1,2 @@\n-old line\n+new line\n unchanged"
	highlighted := HighlightDiff(diff)
	if !strings.Contains(highlighted, "old line") || !strings.Contains(highlighted, "new line") {
		t.Errorf("highlighted diff missing content: %s", highlighted)
	}
}

func TestFormatMarkdown(t *testing.T) {
	input := "# Result\n\n- **passed** with `go test`\n\n```go\nfmt.Println(\"ok\")\n```\n\n[docs](https://example.com)"
	formatted := FormatMarkdown(input)
	plain := StripANSI(formatted)
	for _, expected := range []string{"Result", "● passed with go test", "code: go", `fmt.Println("ok")`, "docs (https://example.com)"} {
		if !strings.Contains(plain, expected) {
			t.Errorf("formatted Markdown missing %q:\n%s", expected, plain)
		}
	}
}

func TestFormatStatusCardAndModelsTable(t *testing.T) {
	sm := SessionMetrics{
		TotalTurns:            3,
		TotalPromptTokens:     100,
		TotalCompletionTokens: 50,
		TotalTokens:           150,
		TotalDuration:         2 * time.Second,
		TotalCost:             0.0012,
	}
	pm := NewProcessManager()
	defer pm.KillAll()

	status := FormatStatusCard("/work", "gpt-4o", 1, sm, pm)
	if !strings.Contains(status, "Session Status") || !strings.Contains(status, "150 total") {
		t.Errorf("unexpected status card: %s", status)
	}
	runtime := FormatRuntimeCard(InteractiveRuntime{MaxTurns: 20, CommandTimeout: time.Minute, ThinkLevel: "high", AllowCommands: true, PermissionMode: PermissionAsk}, "session-1")
	if !strings.Contains(runtime, "permissions") || !strings.Contains(runtime, "ask") {
		t.Errorf("runtime card missing permissions: %s", runtime)
	}

	models := []config.ModelEndpoint{
		{ID: "example-model", Name: "Example Model", URL: "http://localhost:8010/v1"},
		{ID: "llama3.1", Name: "Llama 3.1", URL: "http://localhost:11434/v1"},
	}
	table := FormatModelsTable(models, "example-model", "/config.json")
	if !strings.Contains(table, "example-model") || !strings.Contains(table, "Llama 3.1") {
		t.Errorf("unexpected models table: %s", table)
	}
}

func TestShellPromptAndHelp(t *testing.T) {
	prompt := FormatShellPrompt("C:/Users/mateo/go/fastllm")
	if !strings.Contains(prompt, "shell: fastllm") || !strings.Contains(prompt, "fastllm") {
		t.Errorf("unexpected shell prompt: %s", prompt)
	}

	help := FormatHelp()
	if !strings.Contains(help, "/shell") || !strings.Contains(help, "/sessions") || !strings.Contains(help, "/set turns") {
		t.Errorf("expected help to contain shell, session, and runtime commands: %s", help)
	}

	// Ensure ClearScreen does not panic
	ClearScreen()
}
