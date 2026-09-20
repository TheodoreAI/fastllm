package harness

import (
	"fmt"
	"strings"
	"time"

	"fastllm/internal/llm"
)

// LegacyConversation is one conversation recovered from the pre-TUI SQLite store
// that backed the web frontend. It is a plain value so that this package stays
// free of any database dependency; internal/legacystore supplies the concrete
// reader and cmd/cli wires the two together.
type LegacyConversation struct {
	ID        int64
	Title     string
	CreatedAt time.Time
	UpdatedAt time.Time
	Messages  []llm.Message
}

// LegacyConversationSource reads conversations out of the legacy store. The
// index carries the metadata and LoadMessages fetches one body, so importing N
// conversations costs one index read plus N body reads.
type LegacyConversationSource interface {
	ListConversations() ([]LegacyConversation, error)
	LoadMessages(id int64) ([]llm.Message, error)
}

// OpenLegacyConversations opens the legacy store. It is a package-level hook
// rather than a direct call because the concrete reader lives in
// internal/legacystore, which imports this package; binaries that want the
// in-TUI /conversations and /import commands install an opener at startup, and
// binaries that do not stay free of the SQLite driver. Nil means the legacy
// store is unreachable, which is the normal state for a stock CLI build.
var OpenLegacyConversations func() (LegacyConversationSource, func() error, error)

// legacyOrigin is the ImportedFrom tag written onto an imported session. It keys
// the conversation back to its row so a repeated import is a no-op rather than a
// duplicate.
func legacyOrigin(conversationID int64) string {
	return fmt.Sprintf("sqlite:conversation/%d", conversationID)
}

// ImportReport summarizes one import run.
type ImportReport struct {
	Imported []string // session IDs written
	Skipped  int      // already imported previously
	Empty    int      // no usable messages
}

func (r ImportReport) String() string {
	return fmt.Sprintf("imported %d, skipped %d already present, ignored %d empty",
		len(r.Imported), r.Skipped, r.Empty)
}

// conversationToSession converts a legacy conversation into an interactive
// session. Only user and assistant turns survive: the legacy store also holds
// rendering-only rows, and replaying those as context would corrupt the
// conversation rather than restore it.
func conversationToSession(conv LegacyConversation, workingDir, model string, runtime InteractiveRuntime) *InteractiveSession {
	messages := make([]llm.Message, 0, len(conv.Messages))
	for _, message := range conv.Messages {
		if message.Role != "user" && message.Role != "assistant" {
			continue
		}
		if strings.TrimSpace(message.Content) == "" {
			continue
		}
		messages = append(messages, llm.Message{Role: message.Role, Content: message.Content})
	}
	if len(messages) == 0 {
		return nil
	}

	title := strings.TrimSpace(conv.Title)
	if title == "" {
		title = sessionTitle(messages)
	}
	created := conv.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	updated := conv.UpdatedAt
	if updated.IsZero() {
		updated = created
	}
	// Imported conversations are history, not the session to resume into: leaving
	// ClosedAt nil would make the newest import hijack the next TUI launch.
	closed := updated

	return &InteractiveSession{
		ID:           newSessionID(created),
		Title:        title,
		CreatedAt:    created.UTC(),
		UpdatedAt:    updated.UTC(),
		WorkingDir:   workingDir,
		Model:        model,
		Runtime:      runtime,
		Messages:     messages,
		ClosedAt:     &closed,
		CustomTitle:  true,
		ImportedFrom: legacyOrigin(conv.ID),
	}
}

// alreadyImported maps the legacy origins present in the session store.
func alreadyImported(store *SessionStore) (map[string]bool, error) {
	existing, err := store.List()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(existing))
	for _, session := range existing {
		if session.ImportedFrom != "" {
			seen[session.ImportedFrom] = true
		}
	}
	return seen, nil
}

// ImportLegacyConversations drains every conversation from src into store. It is
// idempotent: a conversation already imported is skipped, so re-running after
// adding new web conversations only brings across what is new.
func ImportLegacyConversations(src LegacyConversationSource, store *SessionStore, workingDir, model string, runtime InteractiveRuntime) (ImportReport, error) {
	var report ImportReport
	if src == nil || store == nil {
		return report, fmt.Errorf("import requires both a source and a session store")
	}

	conversations, err := src.ListConversations()
	if err != nil {
		return report, fmt.Errorf("list legacy conversations: %w", err)
	}
	seen, err := alreadyImported(store)
	if err != nil {
		return report, fmt.Errorf("scan existing sessions: %w", err)
	}

	for _, summary := range conversations {
		if seen[legacyOrigin(summary.ID)] {
			report.Skipped++
			continue
		}
		messages, err := src.LoadMessages(summary.ID)
		if err != nil {
			return report, fmt.Errorf("load conversation %d: %w", summary.ID, err)
		}
		conv := summary
		conv.Messages = messages
		session := conversationToSession(conv, workingDir, model, runtime)
		if session == nil {
			report.Empty++
			continue
		}
		if err := store.Save(session); err != nil {
			return report, fmt.Errorf("save imported conversation %d: %w", summary.ID, err)
		}
		// Guard against duplicate ids within a single run.
		seen[session.ImportedFrom] = true
		report.Imported = append(report.Imported, session.ID)
	}
	return report, nil
}
