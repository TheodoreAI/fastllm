package harness

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"fastllm/internal/files"
)

// The write journal is how /undo reverts what the model changed, and only
// that. Before the first change a run makes to a file, the journal keeps the
// file's previous contents (or notes that it did not exist); after each
// change it keeps a hash of what the model left. Undo puts the previous
// contents back, deleting files the run created, but skips any file that no
// longer matches what the model left: the user has edited it since, and those
// edits are theirs. It works in any directory, git repository or not, and
// never touches a file the model did not write through its file tools.
//
// Commands run by the model can change files too; those changes are not
// journaled. The journal lives for the session, in memory.

// Journal limits: a pre-image larger than this is not kept (that file cannot
// be undone), and the oldest runs are dropped once the total passes the cap.
const (
	maxJournalFileBytes  = 10 << 20
	maxJournalTotalBytes = 256 << 20
)

type journalEntry struct {
	path     string // absolute
	display  string // as the model named it
	existed  bool
	before   []byte
	tooLarge bool
	mode     fs.FileMode
	after    [32]byte // hash of the model's last write
	written  bool
}

type journalRun struct {
	label   string
	started time.Time
	order   []string // paths, in first-write order
	entries map[string]*journalEntry
}

// WriteJournal records model file writes, grouped by run. Nil is a no-op.
type WriteJournal struct {
	mu   sync.Mutex
	runs []*journalRun
}

func NewWriteJournal() *WriteJournal { return &WriteJournal{} }

// begin starts a new group; each top-level run (one prompt) is one group.
func (j *WriteJournal) begin(label string) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.runs = append(j.runs, &journalRun{label: label, started: time.Now(), entries: map[string]*journalEntry{}})
}

// before records path's state ahead of a write, once per run.
func (j *WriteJournal) before(path, display string) {
	if j == nil || path == "" {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.runs) == 0 {
		j.runs = append(j.runs, &journalRun{label: "model changes", started: time.Now(), entries: map[string]*journalEntry{}})
	}
	run := j.runs[len(j.runs)-1]
	if _, seen := run.entries[path]; seen {
		return
	}
	entry := &journalEntry{path: path, display: display}
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		entry.existed = true
		entry.mode = info.Mode().Perm()
		if info.Size() > maxJournalFileBytes {
			entry.tooLarge = true
		} else if data, err := os.ReadFile(path); err == nil {
			entry.before = data
		} else {
			entry.tooLarge = true
		}
	}
	run.entries[path] = entry
	run.order = append(run.order, path)
	j.trimLocked()
}

// after records what the model left at path.
func (j *WriteJournal) after(path string) {
	if j == nil || path == "" {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.runs) == 0 {
		return
	}
	entry := j.runs[len(j.runs)-1].entries[path]
	if entry == nil {
		return
	}
	if data, err := os.ReadFile(path); err == nil {
		entry.after = sha256.Sum256(data)
		entry.written = true
	}
}

// trimLocked drops the oldest runs once the journal is over its size cap.
func (j *WriteJournal) trimLocked() {
	total := 0
	for _, run := range j.runs {
		for _, e := range run.entries {
			total += len(e.before)
		}
	}
	for total > maxJournalTotalBytes && len(j.runs) > 1 {
		for _, e := range j.runs[0].entries {
			total -= len(e.before)
		}
		j.runs = j.runs[1:]
	}
}

// UndoReport says what an undo did.
type UndoReport struct {
	Label     string
	Restored  []string
	Deleted   []string
	Conflicts []string // changed since the model wrote them; left alone
	Failed    []string
}

// Undo reverts the most recent run that changed files. force also reverts
// files edited since the model wrote them.
func (j *WriteJournal) Undo(force bool) (UndoReport, error) {
	if j == nil {
		return UndoReport{}, errors.New("no changes recorded")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for len(j.runs) > 0 && len(j.runs[len(j.runs)-1].order) == 0 {
		j.runs = j.runs[:len(j.runs)-1]
	}
	if len(j.runs) == 0 {
		return UndoReport{}, errors.New("no model file changes to undo in this session")
	}
	run := j.runs[len(j.runs)-1]
	report := UndoReport{Label: run.label}
	var remaining []string
	for i := len(run.order) - 1; i >= 0; i-- {
		e := run.entries[run.order[i]]
		if !e.written {
			continue // the write never happened
		}
		current, err := os.ReadFile(e.path)
		switch {
		case err == nil && sha256.Sum256(current) != e.after && !force:
			report.Conflicts = append(report.Conflicts, e.display)
			remaining = append(remaining, e.path)
			continue
		case err != nil && !errors.Is(err, fs.ErrNotExist) && !force:
			report.Failed = append(report.Failed, e.display+": "+err.Error())
			remaining = append(remaining, e.path)
			continue
		}
		switch {
		case e.tooLarge:
			report.Failed = append(report.Failed, e.display+": its earlier contents were too large to keep")
		case !e.existed:
			if err := os.Remove(e.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				report.Failed = append(report.Failed, e.display+": "+err.Error())
				remaining = append(remaining, e.path)
			} else {
				report.Deleted = append(report.Deleted, e.display)
			}
		default:
			if err := os.WriteFile(e.path, e.before, e.mode); err != nil {
				report.Failed = append(report.Failed, e.display+": "+err.Error())
				remaining = append(remaining, e.path)
			} else {
				report.Restored = append(report.Restored, e.display)
			}
		}
	}
	if len(remaining) == 0 {
		j.runs = j.runs[:len(j.runs)-1]
	} else {
		// Keep what could not be undone, so /undo force can still reach it.
		kept := map[string]bool{}
		for _, p := range remaining {
			kept[p] = true
		}
		var order []string
		for _, p := range run.order {
			if kept[p] {
				order = append(order, p)
			} else {
				delete(run.entries, p)
			}
		}
		run.order = order
	}
	for _, list := range [][]string{report.Restored, report.Deleted, report.Conflicts} {
		sort.Strings(list)
	}
	return report, nil
}

// FormatUndoReport renders an UndoReport for either terminal UI.
func FormatUndoReport(r UndoReport) string {
	var lines []string
	add := func(label string, paths []string) {
		for _, p := range paths {
			lines = append(lines, FormatKV(label, sanitizeUntrusted(p), 10))
		}
	}
	add("restored", r.Restored)
	add("deleted", r.Deleted)
	add("skipped", r.Conflicts)
	add("failed", r.Failed)
	if len(lines) == 0 {
		lines = append(lines, ColorGray("Nothing needed changing."))
	}
	if len(r.Conflicts) > 0 {
		lines = append(lines, "", ColorYellow("Skipped files changed after the model wrote them; /undo force reverts them anyway."))
	}
	title := fmt.Sprintf("Undo · %s", truncateText(sanitizeUntrusted(strings.TrimSpace(r.Label)), 50))
	return FormatCard(title, append([]string{""}, append(lines, "")...), 86)
}

// writeJournal is the TUI session's journal, created on first use.
func (m *teaModel) writeJournal() *WriteJournal {
	if m.journal == nil {
		m.journal = NewWriteJournal()
	}
	return m.journal
}

// journalTarget resolves a write tool's path the way the file layer will.
func journalTarget(reader *files.Reader, rawArgs string) (requested, target string) {
	var parsed struct {
		Path string `json:"path"`
	}
	if reader == nil || json.Unmarshal([]byte(rawArgs), &parsed) != nil || strings.TrimSpace(parsed.Path) == "" {
		return "", ""
	}
	resolved, err := reader.ResolveForWrite(parsed.Path)
	if err != nil {
		return "", ""
	}
	return parsed.Path, resolved
}
