// Package vector implements a minimal in-memory similarity search.
// It is intentionally simple (linear scan + cosine similarity) so the
// scaffold has no external vector-database dependency. Swap this out
// for Qdrant/sqlite-vec/etc. once chunk counts get large.
package vector

import (
	"math"
	"sort"
	"sync"
)

type Chunk struct {
	ID         int64
	DocumentID int64
	Content    string
	Embedding  []float32
}

type Store struct {
	mu     sync.RWMutex
	chunks []Chunk
}

func NewStore() *Store {
	return &Store{}
}

func (s *Store) Add(c Chunk) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chunks = append(s.chunks, c)
}

func (s *Store) Load(chunks []Chunk) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chunks = chunks
}

// Clear removes every chunk from the index.
func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chunks = nil
}

type scored struct {
	chunk Chunk
	score float32
}

// Search returns the topK chunks most similar to the query embedding.
func (s *Store) Search(query []float32, topK int) []Chunk {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.chunks) == 0 {
		return nil
	}

	results := make([]scored, 0, len(s.chunks))
	for _, c := range s.chunks {
		results = append(results, scored{chunk: c, score: cosineSimilarity(query, c.Embedding)})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].score > results[j].score })

	if topK > len(results) {
		topK = len(results)
	}
	out := make([]Chunk, topK)
	for i := 0; i < topK; i++ {
		out[i] = results[i].chunk
	}
	return out
}

func cosineSimilarity(a, b []float32) float32 {
	if len(a) == 0 || len(a) != len(b) {
		return -1
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(normA) * math.Sqrt(normB)))
}
