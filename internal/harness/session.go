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
}

type InteractiveSession struct {
	ID         string             `json:"id"`
	Title      string             `json:"title"`
	CreatedAt  time.Time          `json:"created_at"`
	UpdatedAt  time.Time          `json:"updated_at"`
	WorkingDir string             `json:"working_dir"`
	Model      string             `json:"model"`
	Runtime    InteractiveRuntime `json:"runtime"`
	Messages   []llm.Message      `json:"messages"`
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

func (s *SessionStore) New(workingDir, model string, runtime InteractiveRuntime) *InteractiveSession {
	var random [4]byte
	_, _ = rand.Read(random[:])
	now := time.Now().UTC()
	return &InteractiveSession{
		ID:         now.Format("20060102-150405000") + "-" + hex.EncodeToString(random[:]),
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
