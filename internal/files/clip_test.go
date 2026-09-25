package files

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClipMatchShowsTheMatchInALongLine(t *testing.T) {
	line := strings.Repeat("a", 100000) + "NEEDLE" + strings.Repeat("ü", 100000)
	got := ClipMatch(line, regexp.MustCompile("NEEDLE"))
	if !strings.Contains(got, "NEEDLE") {
		t.Fatalf("clipped line lost the match: %q", got[:80])
	}
	if len(got) > MaxMatchLineChars+60 {
		t.Fatalf("clipped line is %d bytes", len(got))
	}
	if !strings.HasPrefix(got, "…") || !strings.Contains(got, "[line is 300006 characters]") {
		t.Fatalf("clip is not marked: %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatal("clip split a character")
	}
}

func TestClipMatchLeavesShortLinesAlone(t *testing.T) {
	if got := ClipMatch("  x := frame  ", regexp.MustCompile("frame")); got != "x := frame" {
		t.Fatalf("got %q", got)
	}
}
