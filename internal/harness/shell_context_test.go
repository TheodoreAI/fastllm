package harness

import (
	"strings"
	"testing"
	"time"
)

func TestShellActivityTrackerRingBuffer(t *testing.T) {
	tracker := NewShellActivityTracker(3)
	tracker.Record("echo 1", "1", 0, 10*time.Millisecond)
	tracker.Record("echo 2", "2", 0, 20*time.Millisecond)
	tracker.Record("echo 3", "3", 0, 30*time.Millisecond)
	tracker.Record("echo 4", "4", 0, 40*time.Millisecond)

	records := tracker.RecentRecords(time.Time{})
	if len(records) != 3 {
		t.Fatalf("expected 3 records, got %d", len(records))
	}
	if records[0].Command != "echo 2" || records[2].Command != "echo 4" {
		t.Errorf("unexpected records in ring buffer: %+v", records)
	}
}

func TestShellActivityTrackerFormatSince(t *testing.T) {
	tracker := NewShellActivityTracker(5)
	start := time.Now()

	time.Sleep(5 * time.Millisecond)
	tracker.Record("gh pr list", "#123 Fix header\n#124 Update auth", 0, 250*time.Millisecond)

	formatted := tracker.FormatRecentSince(start)
	if !strings.Contains(formatted, "<recent_shell_activity>") {
		t.Errorf("expected <recent_shell_activity> tag, got %q", formatted)
	}
	if !strings.Contains(formatted, "$ gh pr list") {
		t.Errorf("expected command gh pr list, got %q", formatted)
	}
	if !strings.Contains(formatted, "#123 Fix header") {
		t.Errorf("expected output #123 Fix header, got %q", formatted)
	}

	// Asking for activity after the command was recorded should be empty
	future := time.Now().Add(1 * time.Minute)
	if tracker.FormatRecentSince(future) != "" {
		t.Errorf("expected empty string for future timestamp")
	}
}
