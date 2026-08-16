// Package store persists chat history, documents, and chunk embeddings
// to a local SQLite file using the pure-Go (no cgo) driver.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"

	_ "modernc.org/sqlite"

	"fastllm/internal/vector"
)

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
	return db, nil
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
