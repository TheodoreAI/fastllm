package harness

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"fastllm/internal/llm"
)

type InteractiveRuntime struct {
	MaxTurns       int            `json:"max_turns"`
	CommandTimeout time.Duration  `json:"command_timeout"`
	ThinkLevel     string         `json:"think_level"`
	AllowCommands  bool           `json:"allow_commands"`
	PermissionMode PermissionMode `json:"permission_mode"`
	ExpandedTools  bool           `json:"expanded_tools,omitempty"`
	Sandbox        bool           `json:"sandbox,omitempty"`
	Budget         Budget         `json:"budget,omitempty"`
}

type InteractiveSession struct {
	ID          string             `json:"id"`
	Title       string             `json:"title"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
	WorkingDir  string             `json:"working_dir"`
	Model       string             `json:"model"`
	Runtime     InteractiveRuntime `json:"runtime"`
	Messages    []llm.Message      `json:"messages"`
	Metrics     SessionMetrics     `json:"metrics,omitempty"`
	ClosedAt    *time.Time         `json:"closed_at,omitempty"`
	CustomTitle bool               `json:"custom_title,omitempty"`
	// ImportedFrom tags a session recovered from the legacy SQLite store so a
	// repeated import skips it instead of duplicating it.
	ImportedFrom string `json:"imported_from,omitempty"`
}

type SessionStore struct {
	Dir string
}

func DefaultSessionStore() (*SessionStore, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &SessionStore{Dir: filepath.Join(home, ".fastllm", "sessions")}, nil
}

// newSessionID mints a store-unique id ordered by the supplied timestamp.
func newSessionID(at time.Time) string {
	var random [4]byte
	_, _ = rand.Read(random[:])
	return at.UTC().Format("20060102-150405000") + "-" + hex.EncodeToString(random[:])
}

func (s *SessionStore) New(workingDir, model string, runtime InteractiveRuntime) *InteractiveSession {
	now := time.Now().UTC()
	return &InteractiveSession{
		ID:         newSessionID(now),
		Title:      "New session",
		CreatedAt:  now,
		UpdatedAt:  now,
		WorkingDir: workingDir,
		Model:      model,
		Runtime:    runtime,
	}
}

func (s *SessionStore) Save(session *InteractiveSession) error {
	if session == nil || !validSessionID(session.ID) {
		return fmt.Errorf("invalid session")
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	session.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return err
	}
	target := filepath.Join(s.Dir, session.ID+".json")
	temp, err := os.CreateTemp(s.Dir, session.ID+"-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(append(data, '\n')); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	renameErr := os.Rename(tempName, target)
	if renameErr == nil {
		return nil
	}
	// Windows does not replace an existing destination with os.Rename.
	if runtime.GOOS != "windows" {
		return renameErr
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(tempName, target)
}

func (s *SessionStore) Delete(id string) error {
	if !validSessionID(id) {
		return fmt.Errorf("invalid session ID %q", id)
	}
	err := os.Remove(filepath.Join(s.Dir, id+".json"))
	if os.IsNotExist(err) {
		return fmt.Errorf("session %q not found", id)
	}
	return err
}

func (s *SessionStore) Rename(id, title string) (*InteractiveSession, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, fmt.Errorf("session title cannot be empty")
	}
	session, err := s.Load(id)
	if err != nil {
		return nil, err
	}
	session.Title = title
	session.CustomTitle = true
	if err := s.Save(session); err != nil {
		return nil, err
	}
	return session, nil
}

func (s *SessionStore) Latest() (*InteractiveSession, error) {
	sessions, err := s.List()
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return nil, fmt.Errorf("no saved sessions")
	}
	return s.Load(sessions[0].ID)
}

// Prune removes abandoned empty sessions older than a day and caps retained
// non-empty sessions by count. Content is never removed solely because of age.
func (s *SessionStore) Prune(now time.Time, maxNonEmpty int) (int, error) {
	sessions, err := s.List()
	if err != nil {
		return 0, err
	}
	removed, keptNonEmpty := 0, 0
	for _, session := range sessions {
		empty := len(session.Messages) == 0
		remove := empty && now.Sub(session.UpdatedAt) > 24*time.Hour
		if !empty {
			keptNonEmpty++
			remove = remove || (maxNonEmpty > 0 && keptNonEmpty > maxNonEmpty)
		}
		if remove {
			if err := s.Delete(session.ID); err != nil {
				return removed, err
			}
			removed++
		}
	}
	return removed, nil
}

func (s *SessionStore) Load(id string) (*InteractiveSession, error) {
	if !validSessionID(id) {
		return nil, fmt.Errorf("invalid session ID %q", id)
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, id+".json"))
	if err != nil {
		return nil, err
	}
	var session InteractiveSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, err
	}
	return &session, nil
}

func (s *SessionStore) List() ([]InteractiveSession, error) {
	entries, err := os.ReadDir(s.Dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sessions []InteractiveSession
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		session, err := s.Load(id)
		if err == nil {
			sessions = append(sessions, *session)
		}
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt) })
	return sessions, nil
}

func validSessionID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func sessionTitle(messages []llm.Message) string {
	for _, message := range messages {
		if message.Role != "user" {
			continue
		}
		title := strings.Join(strings.Fields(message.Content), " ")
		if runes := []rune(title); len(runes) > 60 {
			title = string(runes[:57]) + "..."
		}
		if title != "" {
			return title
		}
	}
	return "New session"
}
