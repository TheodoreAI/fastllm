package execution

import (
	"context"
	"strings"
	"sync"
)

type Chunk struct {
	Stream string
	Data   string
	Offset uint64
}
type Output struct {
	Chunks    []Chunk
	Next      uint64
	Truncated bool
	Done      bool
}

type outputBuffer struct {
	mu          sync.Mutex
	chunks      []Chunk
	size, limit int
	next        uint64
	done        bool
	changed     chan struct{}
}

func newOutput(limit int) *outputBuffer {
	return &outputBuffer{limit: limit, changed: make(chan struct{})}
}
func (b *outputBuffer) notify() { close(b.changed); b.changed = make(chan struct{}) }
func (b *outputBuffer) write(stream string, data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(data)
	offset := b.next
	b.next += uint64(n)
	if n > b.limit {
		offset += uint64(n - b.limit)
		data = data[n-b.limit:]
	}
	if len(data) > 0 {
		b.chunks = append(b.chunks, Chunk{stream, string(data), offset})
		b.size += len(data)
	}
	for b.size > b.limit {
		excess := b.size - b.limit
		if len(b.chunks[0].Data) <= excess {
			b.size -= len(b.chunks[0].Data)
			b.chunks[0] = Chunk{}
			b.chunks = b.chunks[1:]
		} else {
			b.chunks[0].Data = strings.Clone(b.chunks[0].Data[excess:])
			b.chunks[0].Offset += uint64(excess)
			b.size -= excess
		}
	}
	b.notify()
	return n, nil
}
func (b *outputBuffer) finish() { b.mu.Lock(); defer b.mu.Unlock(); b.done = true; b.notify() }
func (b *outputBuffer) read(ctx context.Context, cursor uint64) (Output, error) {
	for {
		b.mu.Lock()
		if cursor < b.next || b.done {
			out := Output{Next: b.next, Done: b.done}
			if len(b.chunks) > 0 {
				out.Truncated = cursor < b.chunks[0].Offset
			}
			for _, c := range b.chunks {
				end := c.Offset + uint64(len(c.Data))
				if end <= cursor {
					continue
				}
				if cursor > c.Offset {
					c.Data = c.Data[cursor-c.Offset:]
					c.Offset = cursor
				}
				out.Chunks = append(out.Chunks, c)
			}
			b.mu.Unlock()
			return out, nil
		}
		changed := b.changed
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return Output{}, ctx.Err()
		case <-changed:
		}
	}
}

type streamWriter struct {
	buffer *outputBuffer
	stream string
}

func (w streamWriter) Write(data []byte) (int, error) { return w.buffer.write(w.stream, data) }
