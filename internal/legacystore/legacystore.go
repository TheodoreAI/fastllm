// Package legacystore adapts the SQLite database that backed the web frontend to
// the harness import interface. It exists as its own package so that
// internal/harness stays free of a SQLite dependency: only binaries that
// actually perform an import link the driver.
package legacystore

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fastllm/internal/harness"
	"fastllm/internal/llm"
	"fastllm/internal/store"
)

// WorkspaceID matches the workspace the web frontend wrote under.
const WorkspaceID = "default"

// Source reads conversations from an open legacy database.
type Source struct {
	db          *sql.DB
	workspaceID string
	closer      func() error
}

// Open opens the legacy database at path for reading.
func Open(path string) (*Source, error) {
	db, err := store.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open legacy database %s: %w", path, err)
	}
	return &Source{db: db, workspaceID: WorkspaceID, closer: db.Close}, nil
}

// NewSource wraps an already-open database; the caller retains ownership.
func NewSource(db *sql.DB, workspaceID string) *Source {
	if workspaceID == "" {
		workspaceID = WorkspaceID
	}
	return &Source{db: db, workspaceID: workspaceID}
}

func (s *Source) Close() error {
	if s == nil || s.closer == nil {
		return nil
	}
	return s.closer()
}

// sqliteTimeLayouts covers the formats the DATETIME defaults and the Go driver
// produce; an unparseable stamp degrades to a zero time rather than an error,
// since a missing timestamp must not block recovering the messages.
var sqliteTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999999 -0700 MST",
	"2006-01-02 15:04:05",
}

func parseSQLiteTime(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range sqliteTimeLayouts {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

// ListConversations returns every conversation in the workspace, without bodies.
func (s *Source) ListConversations() ([]harness.LegacyConversation, error) {
	rows, err := store.ListConversations(s.db, s.workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]harness.LegacyConversation, 0, len(rows))
	for _, row := range rows {
		out = append(out, harness.LegacyConversation{
			ID:        row.ID,
			Title:     row.Title,
			CreatedAt: parseSQLiteTime(row.CreatedAt),
			UpdatedAt: parseSQLiteTime(row.UpdatedAt),
		})
	}
	return out, nil
}

// LoadMessages returns the user/assistant bodies for one conversation.
func (s *Source) LoadMessages(id int64) ([]llm.Message, error) {
	rows, err := store.LoadMessages(s.db, s.workspaceID, id)
	if err != nil {
		return nil, err
	}
	messages := make([]llm.Message, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, llm.Message{Role: row.Role, Content: row.Content})
	}
	return messages, nil
}

// DefaultPath resolves the legacy database the web frontend wrote to: an
// explicit FASTLLM_DB override, then fastllm.db beside the working directory
// (how the server runs from a checkout), then the per-user config copy.
func DefaultPath() (string, error) {
	if override := strings.TrimSpace(os.Getenv("FASTLLM_DB")); override != "" {
		return override, nil
	}
	if _, err := os.Stat("fastllm.db"); err == nil {
		return "fastllm.db", nil
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "fastllm", "fastllm.db"), nil
}

// Installer returns an opener suitable for harness.OpenLegacyConversations.
// It reports a clear error when no legacy database exists, so the TUI can say
// so instead of creating an empty one.
func Installer() func() (harness.LegacyConversationSource, func() error, error) {
	return func() (harness.LegacyConversationSource, func() error, error) {
		path, err := DefaultPath()
		if err != nil {
			return nil, nil, err
		}
		if _, statErr := os.Stat(path); statErr != nil {
			return nil, nil, fmt.Errorf("no legacy database at %s", path)
		}
		src, err := Open(path)
		if err != nil {
			return nil, nil, err
		}
		return src, src.Close, nil
	}
}
