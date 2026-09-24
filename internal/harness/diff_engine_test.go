package harness

import (
	"testing"
)

func TestParseUnifiedDiff(t *testing.T) {
	diff := `diff --git a/main.go b/main.go
index 1234567..89abcdef 100644
--- a/main.go
+++ b/main.go
@@ -10,4 +10,4 @@ func main() {
 	fmt.Println("hello")
-	timeout := 5
+	timeout := 10
 	run(timeout)
`

	files := ParseDiffForDisplay(diff)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}

	f := files[0]
	if f.NewPath != "main.go" {
		t.Errorf("expected NewPath=main.go, got %q", f.NewPath)
	}

	var kinds []DiffLineKind
	var oldLines []int
	var newLines []int
	for _, l := range f.Lines {
		kinds = append(kinds, l.Kind)
		oldLines = append(oldLines, l.OldLineNo)
		newLines = append(newLines, l.NewLineNo)
	}

	// Find the deletion and addition lines
	var delLine, addLine *ParsedDiffLine
	for i := range f.Lines {
		if f.Lines[i].Kind == DiffLineDeleted {
			delLine = &f.Lines[i]
		}
		if f.Lines[i].Kind == DiffLineAdded {
			addLine = &f.Lines[i]
		}
	}

	if delLine == nil || addLine == nil {
		t.Fatal("expected both deleted and added lines")
	}

	if delLine.OldLineNo != 11 {
		t.Errorf("expected delLine.OldLineNo=11, got %d", delLine.OldLineNo)
	}
	if addLine.NewLineNo != 11 {
		t.Errorf("expected addLine.NewLineNo=11, got %d", addLine.NewLineNo)
	}

	// Verify word spans
	if len(delLine.WordSpans) == 0 || len(addLine.WordSpans) == 0 {
		t.Fatal("expected word spans on paired changed lines")
	}

	// "5" should be changed in delLine, "10" changed in addLine
	hasChanged5 := false
	for _, span := range delLine.WordSpans {
		if span.Changed && span.Text == "5" {
			hasChanged5 = true
		}
	}
	if !hasChanged5 {
		t.Errorf("expected '5' to be marked as changed in delLine: %+v", delLine.WordSpans)
	}

	hasChanged10 := false
	for _, span := range addLine.WordSpans {
		if span.Changed && span.Text == "10" {
			hasChanged10 = true
		}
	}
	if !hasChanged10 {
		t.Errorf("expected '10' to be marked as changed in addLine: %+v", addLine.WordSpans)
	}
}

func TestDiffWords(t *testing.T) {
	oldLine := `const timeout = 5 * time.Second`
	newLine := `const timeout = 10 * time.Second`

	oldSpans, newSpans := DiffWords(oldLine, newLine)

	changedOld := ""
	for _, s := range oldSpans {
		if s.Changed {
			changedOld += s.Text
		}
	}
	if changedOld != "5" {
		t.Errorf("expected changedOld='5', got %q", changedOld)
	}

	changedNew := ""
	for _, s := range newSpans {
		if s.Changed {
			changedNew += s.Text
		}
	}
	if changedNew != "10" {
		t.Errorf("expected changedNew='10', got %q", changedNew)
	}
}
