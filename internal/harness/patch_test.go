package harness

import (
	"strings"
	"testing"
)

func TestParseUnifiedDiffAndApplyPatch(t *testing.T) {
	diff := `--- a/math.go
+++ b/math.go
@@ -1,5 +1,5 @@
 package math

-func Add(a, b int) int {
-	return a - b
+func Add(a, b int) int {
+	return a + b
 }
`

	files, err := ParseUnifiedDiff(diff)
	if err != nil {
		t.Fatalf("ParseUnifiedDiff failed: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].NewPath != "math.go" {
		t.Errorf("expected NewPath 'math.go', got %q", files[0].NewPath)
	}
	if len(files[0].Hunks) != 1 {
		t.Fatalf("expected 1 hunk, got %d", len(files[0].Hunks))
	}

	original := `package math

func Add(a, b int) int {
	return a - b
}
`
	patched, err := ApplyPatch(original, files[0].Hunks)
	if err != nil {
		t.Fatalf("ApplyPatch failed: %v", err)
	}

	expected := `package math

func Add(a, b int) int {
	return a + b
}
`
	if strings.TrimSpace(patched) != strings.TrimSpace(expected) {
		t.Errorf("patched content mismatch:\ngot:\n%s\nwant:\n%s", patched, expected)
	}
}

func TestResilientReplaceExact(t *testing.T) {
	original := "line 1\nline 2\nline 3\n"
	search := "line 2\n"
	replace := "line two\n"

	got, err := ResilientReplace(original, search, replace)
	if err != nil {
		t.Fatalf("ResilientReplace failed: %v", err)
	}
	want := "line 1\nline two\nline 3\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResilientReplaceFuzzyWhitespace(t *testing.T) {
	original := "  func Test() { \r\n    return 42 \r\n  }"
	// LLM forgot the trailing whitespace or used unix newlines
	search := "func Test() {\n  return 42\n}"
	replace := "func Test() {\n  return 100\n}"

	got, err := ResilientReplace(original, search, replace)
	if err != nil {
		t.Fatalf("ResilientReplace failed on fuzzy whitespace: %v", err)
	}
	if !strings.Contains(got, "100") {
		t.Errorf("expected '100' in result, got: %s", got)
	}
}
