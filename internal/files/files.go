// Package files provides sandboxed file access for the chat model's
// read_file and write_file tools. Every path is resolved and confirmed to
// stay inside a single allow-listed root directory before any I/O
// happens. Writes are additionally gated by a separate enable flag and
// never touch disk directly — see PendingWrites.
package files

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// MaxReadBytes caps how much of a file is returned to the model — large
// enough for source files and docs, small enough to not blow the context
// window or the response size on a misclick against a huge file.
const MaxReadBytes = 64 * 1024

// MaxWriteBytes caps how large a proposed write can be — a human has to
// review this in a diff, so it stays well below MaxReadBytes.
const MaxWriteBytes = 32 * 1024

var ErrOutsideRoot = errors.New("path is outside the allowed directory")

// Reader resolves paths against a fixed root and reads/writes files from
// within it. The zero value (empty Root) means the feature is disabled —
// callers should check Enabled() before registering the read_file tool,
// and WritesEnabled() before registering write_file.
type Reader struct {
	mu          sync.RWMutex
	Root        string
	AllowWrites bool
}

// New constructs a Reader. root == "" disables the feature entirely
// (Enabled() and WritesEnabled() both false). allowWrites additionally
// gates whether write_file is offered — reads can be enabled without
// writes, but not the reverse.
func New(root string, allowWrites bool) *Reader {
	if root == "" {
		return &Reader{}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return &Reader{}
	}
	return &Reader{Root: abs, AllowWrites: allowWrites}
}

func (r *Reader) SetRoot(root string, allowWrites bool) error {
	return r.SetConfig(root, root != "", allowWrites)
}

func (r *Reader) SetConfig(root string, readEnabled, writeEnabled bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !readEnabled {
		r.Root = ""
		r.AllowWrites = false
		return nil
	}
	if root == "" {
		r.Root = ""
		r.AllowWrites = false
		return nil
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	r.Root = abs
	r.AllowWrites = writeEnabled
	return nil
}

func (r *Reader) Enabled() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.Root != ""
}

func (r *Reader) WritesEnabled() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.Root != "" && r.AllowWrites
}

// Resolve joins the requested path onto Root and confirms the result is
// still inside Root — rejecting absolute paths, ".." traversal, and
// symlinks that escape the sandbox. The target must already exist.
func (r *Reader) Resolve(requested string) (string, error) {
	r.mu.RLock()
	root := r.Root
	r.mu.RUnlock()
	if root == "" {
		return "", errors.New("file access is not enabled")
	}
	joined, err := resolveWithinRoot(root, requested)
	if err != nil {
		return "", err
	}

	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", err
	}
	return confirmWithinRoot(root, resolved)
}

// ResolveForWrite is like Resolve but tolerates a target that doesn't
// exist yet (creating a new file), validating the nearest existing
// ancestor directory instead so a symlinked parent still can't be used to
// escape the sandbox.
func (r *Reader) ResolveForWrite(requested string) (string, error) {
	r.mu.RLock()
	root := r.Root
	allowWrites := r.AllowWrites
	r.mu.RUnlock()
	if root == "" || !allowWrites {
		return "", errors.New("file writes are not enabled")
	}
	joined, err := resolveWithinRoot(root, requested)
	if err != nil {
		return "", err
	}

	// If the target itself already exists (including as a symlink), it
	// must resolve within root — this is what catches overwriting through
	// a symlink that points outside the sandbox, which the ancestor-walk
	// below would otherwise miss (it only checks directories).
	if _, err := os.Lstat(joined); err == nil {
		if _, err := r.Resolve(requested); err != nil {
			return "", err
		}
		return joined, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}

	// Target doesn't exist yet — walk up to the nearest ancestor that
	// does, resolve symlinks on that, and confirm it's still within root,
	// so a symlinked parent directory can't be used to escape the sandbox.
	dir := filepath.Dir(joined)
	for {
		if info, err := os.Stat(dir); err == nil {
			if !info.IsDir() {
				return "", fmt.Errorf("%q is not a directory", dir)
			}
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ErrOutsideRoot
		}
		dir = parent
	}
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	if _, err := confirmWithinRoot(root, resolvedDir); err != nil {
		return "", err
	}
	return joined, nil
}

func resolveWithinRoot(root, requested string) (string, error) {
	joined := filepath.Join(root, requested)
	rel, err := filepath.Rel(root, joined)
	if err != nil || rel == ".." || (len(rel) >= 3 && rel[:3] == ".."+string(filepath.Separator)) {
		return "", ErrOutsideRoot
	}
	return joined, nil
}

func confirmWithinRoot(root, resolved string) (string, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(realRoot, resolved)
	if err != nil || rel == ".." || (len(rel) >= 3 && rel[:3] == ".."+string(filepath.Separator)) {
		return "", ErrOutsideRoot
	}
	return resolved, nil
}

// Read resolves and reads a file, truncating to MaxReadBytes. Returns the
// content and whether it was truncated.
func (r *Reader) Read(requested string) (content string, truncated bool, err error) {
	path, err := r.Resolve(requested)
	if err != nil {
		return "", false, err
	}

	info, err := os.Stat(path)
	if err != nil {
		return "", false, err
	}
	if info.IsDir() {
		return "", false, fmt.Errorf("%q is a directory, not a file", requested)
	}

	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()

	buf := make([]byte, MaxReadBytes+1)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return "", false, err
	}
	if n > MaxReadBytes {
		return string(buf[:MaxReadBytes]), true, nil
	}
	return string(buf[:n]), false, nil
}

// ExistingContent reads a file's current content for diffing against a
// proposed write, if it exists. Returns ("", false, nil) for a
// not-yet-existing file (a new-file write), and a real error only for
// unexpected failures (e.g. it's a directory).
func (r *Reader) ExistingContent(requested string) (content string, exists bool, err error) {
	path, err := r.Resolve(requested)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	if info.IsDir() {
		return "", false, fmt.Errorf("%q is a directory, not a file", requested)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	buf := make([]byte, MaxReadBytes+1)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return "", false, err
	}
	return string(buf[:min(n, MaxReadBytes)]), true, nil
}

// Write validates and performs the actual write to disk. Only called
// after a human has approved a PendingWrite — never directly from a tool
// call. Creates parent directories as needed within the sandbox.
func (r *Reader) Write(requested, content string) error {
	path, err := r.ResolveForWrite(requested)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
