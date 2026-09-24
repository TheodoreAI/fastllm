package harness

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// ShellActivityRecord represents a recorded shell command execution in Shell Mode.
type ShellActivityRecord struct {
	Command   string
	Output    string
	ExitCode  int
	Duration  time.Duration
	Timestamp time.Time
}

// ShellActivityTracker retains a bounded history of recent shell commands
// so that Agent Mode has awareness of recent terminal actions.
type ShellActivityTracker struct {
	mu       sync.Mutex
	capacity int
	records  []ShellActivityRecord
}

// NewShellActivityTracker creates a tracker with the given maximum record capacity.
func NewShellActivityTracker(capacity int) *ShellActivityTracker {
	if capacity <= 0 {
		capacity = 5
	}
	return &ShellActivityTracker{
		capacity: capacity,
	}
}

// Record saves a completed shell command and its output into the ring buffer.
func (t *ShellActivityTracker) Record(cmd string, output string, exitCode int, duration time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	summary := strings.TrimSpace(output)
	lines := strings.Split(summary, "\n")
	// Bound the summary to 20 lines to protect context budget
	if len(lines) > 20 {
		summary = strings.Join(lines[:10], "\n") + fmt.Sprintf("\n... [%d lines omitted] ...\n", len(lines)-15) + strings.Join(lines[len(lines)-5:], "\n")
	}

	record := ShellActivityRecord{
		Command:   cmd,
		Output:    summary,
		ExitCode:  exitCode,
		Duration:  duration,
		Timestamp: time.Now(),
	}

	t.records = append(t.records, record)
	if len(t.records) > t.capacity {
		t.records = t.records[len(t.records)-t.capacity:]
	}
}

// RecentRecords returns all records recorded after the specified timestamp.
func (t *ShellActivityTracker) RecentRecords(since time.Time) []ShellActivityRecord {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	var recent []ShellActivityRecord
	for _, r := range t.records {
		if r.Timestamp.After(since) {
			recent = append(recent, r)
		}
	}
	return recent
}

// FormatRecentSince formats recent shell activity into a prompt-friendly XML block.
func (t *ShellActivityTracker) FormatRecentSince(since time.Time) string {
	recent := t.RecentRecords(since)
	if len(recent) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("<recent_shell_activity>\n")
	sb.WriteString("User recently ran the following shell commands in this workspace:\n")
	for _, r := range recent {
		sb.WriteString(fmt.Sprintf("$ %s (exit: %d, duration: %s)\n", r.Command, r.ExitCode, r.Duration.Round(100*time.Millisecond)))
		if r.Output != "" {
			sb.WriteString(r.Output + "\n")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("</recent_shell_activity>")
	return strings.TrimSpace(sb.String())
}
