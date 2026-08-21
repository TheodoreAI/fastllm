package vector

import "testing"

func TestSearchReturnsClosestFirst(t *testing.T) {
	s := NewStore()
	s.Add(Chunk{ID: 1, Content: "opposite", Embedding: []float32{-1, 0}})
	s.Add(Chunk{ID: 2, Content: "exact match", Embedding: []float32{1, 0}})
	s.Add(Chunk{ID: 3, Content: "orthogonal", Embedding: []float32{0, 1}})

	results := s.Search([]float32{1, 0}, 3)
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	if results[0].ID != 2 {
		t.Errorf("closest match = chunk %d, want chunk 2 (exact match)", results[0].ID)
	}
	if results[len(results)-1].ID != 1 {
		t.Errorf("furthest match = chunk %d, want chunk 1 (opposite direction)", results[len(results)-1].ID)
	}
}

func TestSearchCapsAtAvailableChunks(t *testing.T) {
	s := NewStore()
	s.Add(Chunk{ID: 1, Embedding: []float32{1, 0}})
	s.Add(Chunk{ID: 2, Embedding: []float32{0, 1}})

	results := s.Search([]float32{1, 0}, 10)
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2 — topK should cap at available chunks, not pad", len(results))
	}
}

func TestSearchOnEmptyStoreReturnsNil(t *testing.T) {
	s := NewStore()
	results := s.Search([]float32{1, 0}, 5)
	if results != nil {
		t.Errorf("got %v, want nil for an empty store", results)
	}
}

func TestSearchRespectsTopKLessThanAvailable(t *testing.T) {
	s := NewStore()
	s.Add(Chunk{ID: 1, Embedding: []float32{1, 0}})
	s.Add(Chunk{ID: 2, Embedding: []float32{0.9, 0.1}})
	s.Add(Chunk{ID: 3, Embedding: []float32{0, 1}})

	results := s.Search([]float32{1, 0}, 1)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].ID != 1 {
		t.Errorf("got chunk %d, want chunk 1 (exact match)", results[0].ID)
	}
}

func TestLoadReplacesExistingChunks(t *testing.T) {
	s := NewStore()
	s.Add(Chunk{ID: 1, Embedding: []float32{1, 0}})

	s.Load([]Chunk{{ID: 2, Embedding: []float32{0, 1}}})

	results := s.Search([]float32{0, 1}, 10)
	if len(results) != 1 || results[0].ID != 2 {
		t.Fatalf("Load should replace, not append — got %v", results)
	}
}

func TestClearRemovesAllChunks(t *testing.T) {
	s := NewStore()
	s.Add(Chunk{ID: 1, Embedding: []float32{1, 0}})
	s.Clear()

	if results := s.Search([]float32{1, 0}, 10); results != nil {
		t.Errorf("got %v, want nil after Clear", results)
	}
}

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name string
		a, b []float32
		want float32
	}{
		{"identical vectors", []float32{1, 2, 3}, []float32{1, 2, 3}, 1},
		{"opposite vectors", []float32{1, 0}, []float32{-1, 0}, -1},
		{"orthogonal vectors", []float32{1, 0}, []float32{0, 1}, 0},
		{"mismatched lengths", []float32{1, 2}, []float32{1, 2, 3}, -1},
		{"empty vectors", []float32{}, []float32{}, -1},
		{"zero vector", []float32{0, 0}, []float32{1, 1}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cosineSimilarity(tt.a, tt.b)
			const epsilon = 1e-6
			if diff := got - tt.want; diff > epsilon || diff < -epsilon {
				t.Errorf("cosineSimilarity(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}
