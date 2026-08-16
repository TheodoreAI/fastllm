// Package store persists chat history, documents, and chunk embeddings
// to a local SQLite file using the pure-Go (no cgo) driver.
package store

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"

	_ "modernc.org/sqlite"

	"fastllm/internal/vector"
)

//go:embed seed/skills.json
var seedSkillsJSON []byte

// defaultWorkspaceID must match the workspace ID callers pass in (see
// internal/chat.defaultWorkspace) — fastllm has no multi-workspace UI yet,
// so every caller uses this same literal.
const defaultWorkspaceID = "default"

const schema = `
CREATE TABLE IF NOT EXISTS messages (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	workspace_id TEXT NOT NULL,
	role TEXT NOT NULL,
	content TEXT NOT NULL,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS documents (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	workspace_id TEXT NOT NULL,
	filename TEXT NOT NULL,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS chunks (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	document_id INTEGER NOT NULL,
	content TEXT NOT NULL,
	embedding TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS skills (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	workspace_id TEXT NOT NULL,
	name TEXT NOT NULL,
	prompt TEXT NOT NULL,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- meta holds one-time migration/seed flags, e.g. "seeded_default_skills",
-- so a fresh install gets starter data exactly once even if the user
-- later deletes all of it.
CREATE TABLE IF NOT EXISTS meta (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`

type Message struct {
	ID        int64  `json:"id"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
}

func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	if err := seedDefaultSkills(db); err != nil {
		return nil, fmt.Errorf("store: seed skills: %w", err)
	}
	return db, nil
}

// seedSkill is the shape of each entry in seed/skills.json — the starter
// presets a fresh install ships with.
type seedSkill struct {
	Name   string `json:"name"`
	Prompt string `json:"prompt"`
}

const seedFlagKey = "seeded_default_skills"

// seedDefaultSkills inserts the starter presets exactly once per database,
// tracked via the meta table rather than "is the skills table empty" — the
// latter would re-seed every time a user deletes all their skills.
func seedDefaultSkills(db *sql.DB) error {
	var seeded string
	err := db.QueryRow(`SELECT value FROM meta WHERE key = ?`, seedFlagKey).Scan(&seeded)
	if err == nil {
		return nil // already seeded, whatever the current skill count is
	}
	if err != sql.ErrNoRows {
		return err
	}

	var defaultSkills []seedSkill
	if err := json.Unmarshal(seedSkillsJSON, &defaultSkills); err != nil {
		return fmt.Errorf("parse seed/skills.json: %w", err)
	}

	for _, s := range defaultSkills {
		if _, err := SaveSkill(db, defaultWorkspaceID, s.Name, s.Prompt); err != nil {
			return err
		}
	}
	_, err = db.Exec(`INSERT INTO meta (key, value) VALUES (?, '1')`, seedFlagKey)
	return err
}

func SaveMessage(db *sql.DB, workspaceID, role, content string) error {
	_, err := db.Exec(`INSERT INTO messages (workspace_id, role, content) VALUES (?, ?, ?)`, workspaceID, role, content)
	return err
}

func LoadMessages(db *sql.DB, workspaceID string) ([]Message, error) {
	rows, err := db.Query(`SELECT id, role, content, created_at FROM messages WHERE workspace_id = ? ORDER BY id ASC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func SaveDocument(db *sql.DB, workspaceID, filename string) (int64, error) {
	res, err := db.Exec(`INSERT INTO documents (workspace_id, filename) VALUES (?, ?)`, workspaceID, filename)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func SaveChunk(db *sql.DB, documentID int64, content string, embedding []float32) (int64, error) {
	enc, err := json.Marshal(embedding)
	if err != nil {
		return 0, err
	}
	res, err := db.Exec(`INSERT INTO chunks (document_id, content, embedding) VALUES (?, ?, ?)`, documentID, content, string(enc))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Document is a document with a count of chunks indexed for it.
type Document struct {
	ID         int64  `json:"id"`
	Filename   string `json:"filename"`
	ChunkCount int    `json:"chunk_count"`
	CreatedAt  string `json:"created_at"`
}

// ListDocuments returns every document in the workspace along with how
// many chunks were indexed for it, newest first.
func ListDocuments(db *sql.DB, workspaceID string) ([]Document, error) {
	rows, err := db.Query(`
		SELECT d.id, d.filename, d.created_at, COUNT(c.id)
		FROM documents d
		LEFT JOIN chunks c ON c.document_id = d.id
		WHERE d.workspace_id = ?
		GROUP BY d.id
		ORDER BY d.id DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Document{}
	for rows.Next() {
		var d Document
		if err := rows.Scan(&d.ID, &d.Filename, &d.CreatedAt, &d.ChunkCount); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Skill is a saved, reusable system prompt.
type Skill struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Prompt    string `json:"prompt"`
	CreatedAt string `json:"created_at"`
}

func SaveSkill(db *sql.DB, workspaceID, name, prompt string) (int64, error) {
	res, err := db.Exec(`INSERT INTO skills (workspace_id, name, prompt) VALUES (?, ?, ?)`, workspaceID, name, prompt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func ListSkills(db *sql.DB, workspaceID string) ([]Skill, error) {
	rows, err := db.Query(`SELECT id, name, prompt, created_at FROM skills WHERE workspace_id = ? ORDER BY id ASC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Skill{}
	for rows.Next() {
		var s Skill
		if err := rows.Scan(&s.ID, &s.Name, &s.Prompt, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func GetSkill(db *sql.DB, workspaceID string, id int64) (Skill, error) {
	var s Skill
	err := db.QueryRow(`SELECT id, name, prompt, created_at FROM skills WHERE workspace_id = ? AND id = ?`, workspaceID, id).
		Scan(&s.ID, &s.Name, &s.Prompt, &s.CreatedAt)
	return s, err
}

func DeleteSkill(db *sql.DB, workspaceID string, id int64) error {
	_, err := db.Exec(`DELETE FROM skills WHERE workspace_id = ? AND id = ?`, workspaceID, id)
	return err
}

// LoadAllChunks reads every chunk back out, used to repopulate the
// in-memory vector store on startup.
func LoadAllChunks(db *sql.DB) ([]vector.Chunk, error) {
	rows, err := db.Query(`SELECT id, document_id, content, embedding FROM chunks`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []vector.Chunk
	for rows.Next() {
		var c vector.Chunk
		var enc string
		if err := rows.Scan(&c.ID, &c.DocumentID, &c.Content, &enc); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(enc), &c.Embedding); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
