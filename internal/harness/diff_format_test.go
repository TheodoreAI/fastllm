package harness

import (
	"strings"
	"testing"
)

func TestFormatVSCodeDiff(t *testing.T) {
	diff := `diff --git a/main.go b/main.go
index 1234567..89abcdef 100644
--- a/main.go
+++ b/main.go
@@ -10,3 +10,3 @@ func main() {
 	fmt.Println("hello")
-	timeout := 5
+	timeout := 10
`

	opts := DefaultDiffOptions()
	out := FormatVSCodeDiff(diff, opts)
	t.Logf("Rendered VS Code Diff:\n%s", out)

	if !strings.Contains(out, "main.go") {
		t.Errorf("output missing file path: %s", out)
	}
	if !strings.Contains(out, "timeout") {
		t.Errorf("output missing code text: %s", out)
	}
	// Check that line numbers are present in gutter
	if !strings.Contains(out, "10") {
		t.Errorf("output missing line numbers: %s", out)
	}
	// Check that diff sign indicators + and - are present
	if !strings.Contains(out, "+") || !strings.Contains(out, "-") {
		t.Errorf("output missing diff markers: %s", out)
	}
}

func TestFormatVSCodeDiffColors(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")

	diff := `diff --git a/calc.py b/calc.py
--- a/calc.py
+++ b/calc.py
@@ -1,2 +1,2 @@
-def add(x, y): return x - y
+def add(x, y): return x + y
`

	opts := DefaultDiffOptions()
	out := FormatVSCodeDiff(diff, opts)

	if !strings.Contains(out, "calc.py") {
		t.Errorf("output missing calc.py: %s", out)
	}
	if !strings.Contains(out, "\033[") {
		t.Errorf("expected ANSI escape sequences in colored diff: %s", out)
	}
}
