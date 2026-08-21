package terminal

import "testing"

// fakeSession is a minimal Session for exercising Registry without a real
// PTY — Registry only ever calls Close on the sessions it holds, so that's
// all this needs to implement meaningfully.
type fakeSession struct {
	closed bool
}

func (f *fakeSession) Read(p []byte) (int, error)  { return 0, nil }
func (f *fakeSession) Write(p []byte) (int, error) { return len(p), nil }
func (f *fakeSession) Resize(cols, rows int) error { return nil }
func (f *fakeSession) Close() error {
	f.closed = true
	return nil
}

func TestRegistryCloseAllClosesEverySession(t *testing.T) {
	r := NewRegistry()
	a := &fakeSession{}
	b := &fakeSession{}
	r.add(a)
	r.add(b)

	r.CloseAll()

	if !a.closed {
		t.Error("session a was not closed")
	}
	if !b.closed {
		t.Error("session b was not closed")
	}
}

func TestRegistryRemoveExcludesFromCloseAll(t *testing.T) {
	r := NewRegistry()
	a := &fakeSession{}
	b := &fakeSession{}
	r.add(a)
	r.add(b)
	r.remove(a)

	r.CloseAll()

	if a.closed {
		t.Error("a was removed before CloseAll and should not have been closed")
	}
	if !b.closed {
		t.Error("b was still registered and should have been closed")
	}
}

func TestRegistryCloseAllOnEmptyRegistryDoesNotPanic(t *testing.T) {
	r := NewRegistry()
	r.CloseAll() // must not panic
}

func TestRegistryRemoveOfUnknownSessionIsNoOp(t *testing.T) {
	r := NewRegistry()
	a := &fakeSession{}
	// a was never added — remove should be a harmless no-op, not a panic.
	r.remove(a)
	r.CloseAll()
	if a.closed {
		t.Error("a was never registered and should not have been closed")
	}
}
