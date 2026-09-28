package harness

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

const splitTestDiff = "diff --git a/f.go b/f.go\n--- a/f.go\n+++ b/f.go\n" +
	"@@ -1,5 +1,5 @@\n" +
	" package f\n" +
	"-var a = 1\n" +
	"-var b = 2\n" +
	"+var a = 10\n" +
	" \n" +
	"+var c = 3\n" +
	" func F() {}\n" +
	"-// gone\n"

func TestBuildSplitRowsPairsRunsAndPadsShorterSide(t *testing.T) {
	file := ParseDiffForDisplay(splitTestDiff)[0]
	rows := BuildSplitRows(file)

	type want struct{ old, new string }
	wants := []want{
		{"package f", "package f"},
		{"var a = 1", "var a = 10"},
		{"var b = 2", ""},
		{"", ""},
		{"", "var c = 3"},
		{"func F() {}", "func F() {}"},
		{"// gone", ""},
	}
	if len(rows) != len(wants) {
		t.Fatalf("got %d rows, want %d: %+v", len(rows), len(wants), rows)
	}
	content := func(l *ParsedDiffLine) (string, bool) {
		if l == nil {
			return "", false
		}
		return l.Content, true
	}
	for i, w := range wants {
		old, hasOld := content(rows[i].Old)
		nw, hasNew := content(rows[i].New)
		if old != w.old || nw != w.new {
			t.Errorf("row %d = (%q, %q), want (%q, %q)", i, old, nw, w.old, w.new)
		}
		// Filler cells are exactly the missing sides.
		if i == 2 && (hasNew || !hasOld) || i == 4 && (hasOld || !hasNew) || i == 6 && hasNew {
			t.Errorf("row %d has the wrong filler side: %+v", i, rows[i])
		}
	}
	if rows[1].New.NewLineNo != 2 || rows[1].Old.OldLineNo != 2 {
		t.Errorf("paired line numbers = %d/%d, want 2/2", rows[1].Old.OldLineNo, rows[1].New.NewLineNo)
	}
}

func TestBuildSplitRowsSeparatesLaterHunks(t *testing.T) {
	diff := "@@ -1,1 +1,1 @@\n-a\n+b\n@@ -10,1 +10,1 @@ func X\n-c\n+d\n"
	rows := BuildSplitRows(ParseDiffForDisplay(diff)[0])
	if len(rows) != 3 || rows[0].Hunk != "" || rows[1].Hunk == "" {
		t.Fatalf("rows = %+v, want change, separator, change", rows)
	}
}

func TestRenderSplitDiffRowsAreExactlyWidth(t *testing.T) {
	for _, noColor := range []string{"", "1"} {
		t.Setenv("NO_COLOR", noColor)
		file := ParseDiffForDisplay(splitTestDiff + "+" + strings.Repeat("x", 300) + "\n\tindented\n")[0]
		for _, width := range []int{60, 101, 160} {
			for i, row := range RenderSplitDiff(file, "go", width) {
				if got := ansi.StringWidth(row); got != width {
					t.Errorf("NO_COLOR=%q width %d: row %d is %d wide: %q", noColor, width, i, got, StripANSI(row))
				}
			}
		}
	}
}

func TestRenderSplitDiffShowsBothVersions(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	rows := RenderSplitDiff(ParseDiffForDisplay(splitTestDiff)[0], "go", 80)
	left, right, ok := strings.Cut(rows[1], SymVLine)
	if !ok {
		t.Fatalf("row has no separator: %q", rows[1])
	}
	if !strings.Contains(left, "- var a = 1") || !strings.Contains(right, "+ var a = 10") {
		t.Errorf("changed row = %q | %q", left, right)
	}
	if !strings.Contains(rows[2], "╱") {
		t.Errorf("row with no new line should be filler: %q", rows[2])
	}
}
