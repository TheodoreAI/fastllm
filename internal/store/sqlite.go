// Package store persists chat history and settings to a local SQLite file
// using the pure-Go (no cgo) modernc.org/sqlite driver.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

// defaultWorkspaceID must match the workspace ID callers pass in.
const defaultWorkspaceID = "default"

const schema = `
CREATE TABLE IF NOT EXISTS conversations (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	workspace_id TEXT NOT NULL,
	title TEXT NOT NULL,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS messages (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	workspace_id TEXT NOT NULL,
	conversation_id INTEGER,
	role TEXT NOT NULL,
	content TEXT NOT NULL,
	images TEXT,
	source TEXT,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS notes (
	workspace_id TEXT PRIMARY KEY,
	content TEXT NOT NULL DEFAULT '',
	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS meta (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`

type Message struct {
	ID        int64    `json:"id"`
	Role      string   `json:"role"`
	Content   string   `json:"content"`
	Images    []string `json:"images,omitempty"`
	Source    string   `json:"source,omitempty"`
	CreatedAt string   `json:"created_at"`
}

type Conversation struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func CreateConversation(db *sql.DB, workspaceID, title string) (int64, error) {
	res, err := db.Exec(`INSERT INTO conversations (workspace_id, title) VALUES (?, ?)`, workspaceID, title)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func ConversationExists(db *sql.DB, workspaceID string, conversationID int64) (bool, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM conversations WHERE id = ? AND workspace_id = ?`, conversationID, workspaceID).Scan(&count)
	return count > 0, err
}

func ListConversations(db *sql.DB, workspaceID string) ([]Conversation, error) {
	rows, err := db.Query(`SELECT id, title, created_at, updated_at FROM conversations WHERE workspace_id = ? ORDER BY updated_at DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Conversation
	for rows.Next() {
		var c Conversation
		if err := rows.Scan(&c.ID, &c.Title, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func DeleteConversation(db *sql.DB, workspaceID string, conversationID int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM messages WHERE workspace_id = ? AND conversation_id = ?`, workspaceID, conversationID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM conversations WHERE workspace_id = ? AND id = ?`, workspaceID, conversationID); err != nil {
		return err
	}
	return tx.Commit()
}

func UpdateConversationTitle(db *sql.DB, workspaceID string, conversationID int64, title string) error {
	_, err := db.Exec(`UPDATE conversations SET title = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND workspace_id = ?`, title, conversationID, workspaceID)
	return err
}

func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL; PRAGMA busy_timeout=5000;`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	if err := runMigrations(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func runMigrations(db *sql.DB) error {
	if err := addConversationIDColumn(db); err != nil {
		return err
	}
	if err := addMessagesImagesColumn(db); err != nil {
		return err
	}
	if err := addMessagesSourceColumn(db); err != nil {
		return err
	}
	return migrateOrphanMessages(db)
}

func addConversationIDColumn(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE messages ADD COLUMN conversation_id INTEGER`)
	if err != nil && strings.Contains(err.Error(), "duplicate column name") {
		return nil
	}
	return err
}

func addMessagesImagesColumn(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE messages ADD COLUMN images TEXT`)
	if err != nil && strings.Contains(err.Error(), "duplicate column name") {
		return nil
	}
	return err
}

func addMessagesSourceColumn(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE messages ADD COLUMN source TEXT`)
	if err != nil && strings.Contains(err.Error(), "duplicate column name") {
		return nil
	}
	return err
}

const migrateOrphanFlagKey = "migrated_orphan_messages"

func migrateOrphanMessages(db *sql.DB) error {
	var done string
	err := db.QueryRow(`SELECT value FROM meta WHERE key = ?`, migrateOrphanFlagKey).Scan(&done)
	if err == nil {
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}

	var orphanCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE conversation_id IS NULL`).Scan(&orphanCount); err != nil {
		return err
	}
	if orphanCount > 0 {
		convID, err := CreateConversation(db, defaultWorkspaceID, "Previous conversation")
		if err != nil {
			return err
		}
		if _, err := db.Exec(`UPDATE messages SET conversation_id = ? WHERE conversation_id IS NULL`, convID); err != nil {
			return err
		}
	}
	_, err = db.Exec(`INSERT INTO meta (key, value) VALUES (?, '1')`, migrateOrphanFlagKey)
	return err
}

func SaveMessage(db *sql.DB, workspaceID string, conversationID int64, role, content string, images []string) (int64, error) {
	return SaveMessageWithSource(db, workspaceID, conversationID, role, content, images, "")
}

func SaveMessageWithSource(db *sql.DB, workspaceID string, conversationID int64, role, content string, images []string, source string) (int64, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var imagesJSON any = nil
	if len(images) > 0 {
		data, err := json.Marshal(images)
		if err != nil {
			return 0, err
		}
		imagesJSON = string(data)
	}

	var sourceVal any = nil
	if source != "" {
		sourceVal = source
	}

	res, err := tx.Exec(`INSERT INTO messages (workspace_id, conversation_id, role, content, images, source) VALUES (?, ?, ?, ?, ?, ?)`,
		workspaceID, conversationID, role, content, imagesJSON, sourceVal)
	if err != nil {
		return 0, err
	}

	msgID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	if conversationID != 0 {
		if _, err := tx.Exec(`UPDATE conversations SET updated_at = CURRENT_TIMESTAMP WHERE id = ? AND workspace_id = ?`,
			conversationID, workspaceID); err != nil {
			return 0, err
		}
	}

	return msgID, tx.Commit()
}

func LoadMessages(db *sql.DB, workspaceID string, conversationID int64) ([]Message, error) {
	rows, err := db.Query(`SELECT id, role, content, images, source, created_at FROM messages WHERE workspace_id = ? AND conversation_id = ? ORDER BY id ASC`, workspaceID, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Message
	for rows.Next() {
		var m Message
		var imagesRaw, sourceRaw sql.NullString
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &imagesRaw, &sourceRaw, &m.CreatedAt); err != nil {
			return nil, err
		}
		if imagesRaw.Valid && imagesRaw.String != "" {
			_ = json.Unmarshal([]byte(imagesRaw.String), &m.Images)
		}
		if sourceRaw.Valid {
			m.Source = sourceRaw.String
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

type FileAccessSettings struct {
	Root         string `json:"root"`
	ReadEnabled  bool   `json:"read_enabled"`
	WriteEnabled bool   `json:"write_enabled"`
}

var DefaultFileAccessSettings = FileAccessSettings{}

const settingFileAccess = "file_access"

func GetFileAccessSettings(db *sql.DB) (FileAccessSettings, error) {
	s := DefaultFileAccessSettings
	var value string
	err := db.QueryRow(`SELECT value FROM settings WHERE key = ?`, settingFileAccess).Scan(&value)
	if err == sql.ErrNoRows {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal([]byte(value), &s); err != nil {
		return s, err
	}
	if !s.ReadEnabled {
		s.WriteEnabled = false
	}
	return s, nil
}

const fileAccessSeededFlagKey = "seeded_file_access_from_env"

func SeedFileAccessSettingsFromEnv(db *sql.DB, root string, write bool) error {
	var done string
	err := db.QueryRow(`SELECT value FROM meta WHERE key = ?`, fileAccessSeededFlagKey).Scan(&done)
	if err == nil {
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}

	if err := SaveFileAccessSettings(db, FileAccessSettings{
		Root:         root,
		ReadEnabled:  root != "",
		WriteEnabled: write && root != "",
	}); err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO meta (key, value) VALUES (?, '1')`, fileAccessSeededFlagKey)
	return err
}

func SaveFileAccessSettings(db *sql.DB, s FileAccessSettings) error {
	if s.WriteEnabled && !s.ReadEnabled {
		return fmt.Errorf("write access requires read access to be enabled")
	}
	if s.Root == "" && (s.ReadEnabled || s.WriteEnabled) {
		return fmt.Errorf("root is required when read or write access is enabled")
	}

	payload, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, settingFileAccess, string(payload))
	return err
}

type CloudProviderSettings struct {
	AnthropicAPIKey     string `json:"anthropic_api_key"`
	OpenAIAPIKey        string `json:"openai_api_key"`
	GeminiAPIKey        string `json:"gemini_api_key"`
	NvidiaAPIKey        string `json:"nvidia_api_key"`
	CloudflareAPIKey    string `json:"cloudflare_api_key"`
	CloudflareAccountID string `json:"cloudflare_account_id"`
	OSUBaseURL          string `json:"osu_base_url,omitempty"`
	OSUAPIKey           string `json:"osu_api_key,omitempty"`
}

var DefaultCloudProviderSettings = CloudProviderSettings{}

const settingCloudProviders = "cloud_providers"

func GetCloudProviderSettings(db *sql.DB) (CloudProviderSettings, error) {
	s := DefaultCloudProviderSettings
	var value string
	err := db.QueryRow(`SELECT value FROM settings WHERE key = ?`, settingCloudProviders).Scan(&value)
	if err == sql.ErrNoRows {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal([]byte(value), &s); err != nil {
		return s, err
	}
	return s, nil
}

func SaveCloudProviderSettings(db *sql.DB, s CloudProviderSettings) error {
	payload, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, settingCloudProviders, string(payload))
	return err
}

func ClearConversations(db *sql.DB, workspaceID string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM messages WHERE workspace_id = ?`, workspaceID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM conversations WHERE workspace_id = ?`, workspaceID); err != nil {
		return err
	}
	return tx.Commit()
}

func GetNotes(db *sql.DB, workspaceID string) (string, error) {
	var content string
	err := db.QueryRow(`SELECT content FROM notes WHERE workspace_id = ?`, workspaceID).Scan(&content)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return content, err
}

func SaveNotes(db *sql.DB, workspaceID, content string) error {
	_, err := db.Exec(`INSERT INTO notes (workspace_id, content) VALUES (?, ?) ON CONFLICT(workspace_id) DO UPDATE SET content = excluded.content, updated_at = CURRENT_TIMESTAMP`, workspaceID, content)
	return err
}

func AppendNotes(db *sql.DB, workspaceID, text string) (string, error) {
	current, err := GetNotes(db, workspaceID)
	if err != nil {
		return "", err
	}
	var newContent string
	if current == "" {
		newContent = text
	} else {
		newContent = current + "\n" + text
	}
	if err := SaveNotes(db, workspaceID, newContent); err != nil {
		return "", err
	}
	return newContent, nil
}

