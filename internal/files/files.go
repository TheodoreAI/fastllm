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

// MaxEditorReadBytes caps how large a file the text editor (a human
// directly opening a file, not the chat model) will open — much larger
// than MaxReadBytes since there's no prompt/context-window reason to
// keep it small, but still bounded so opening an accidental multi-GB
// file doesn't try to load it all into memory and the browser tab.
const MaxEditorReadBytes = 5 * 1024 * 1024

var ErrOutsideRoot = errors.New("path is outside the allowed directory")

// Reader resolves paths against a fixed root and reads/writes files from
// within it. The zero value (empty root) means the feature is disabled —
// callers should check Enabled() before registering the read_file tool,
// and WritesEnabled() before registering write_file. root/allowWrites are
// unexported and only ever mutated through SetConfig, so every access
// goes through mu — see Snapshot for why that matters for callers that
// need both values to describe the same moment in time.
type Reader struct {
	mu          sync.RWMutex
	root        string
	allowWrites bool
}

// New constructs a Reader. root == "" disables the feature entirely
// (Enabled() and WritesEnabled() both false). allowWrites additionally
// gates whether write_file is offered — reads can be enabled without
// writes, but not the reverse.
func New(root string, allowWrites bool) *Reader {
	r := &Reader{}
	_ = r.SetConfig(root, root != "", allowWrites)
	return r
}

// SetConfig replaces the sandbox root and read/write flags in one atomic
// update — the live-reconfiguration path used by PUT /api/settings/files
// (see internal/chat.UpdateFileAccessSettings), applied without a server
// restart.
func (r *Reader) SetConfig(root string, readEnabled, writeEnabled bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !readEnabled || root == "" {
		r.root = ""
		r.allowWrites = false
		return nil
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	r.root = abs
	r.allowWrites = writeEnabled
	return nil
}

func (r *Reader) Enabled() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.root != ""
}

// GetRoot returns the current sandbox root, or "" if disabled — for
// display/logging only. Use Snapshot instead when a caller needs root and
// allowWrites to be consistent with each other.
func (r *Reader) GetRoot() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.root
}

// Snapshot returns a consistent {root, allowWrites} pair read under a
// single lock acquisition — use this instead of separate Enabled()/
// WritesEnabled()/GetRoot() calls when a caller needs the two values to
// describe the same moment in time (see runFileTools in internal/chat).
func (r *Reader) Snapshot() (root string, allowWrites bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.root, r.allowWrites
}

func (r *Reader) WritesEnabled() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.root != "" && r.allowWrites
}

// Resolve joins the requested path onto Root and confirms the result is
// still inside Root — rejecting absolute paths, ".." traversal, and
// symlinks that escape the sandbox. The target must already exist.
func (r *Reader) Resolve(requested string) (string, error) {
	root, _ := r.Snapshot()
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
	root, allowWrites := r.Snapshot()
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
	// below would otherwise miss (it only checks directories). Resolved
	// against the root snapshotted above, not re-read via Resolve(), so a
	// concurrent SetConfig mid-call can't make this check and the caller's
	// eventual write disagree about which root they're validating against.
	if _, err := os.Lstat(joined); err == nil {
		resolved, err := filepath.EvalSymlinks(joined)
		if err != nil {
			return "", err
		}
		if _, err := confirmWithinRoot(root, resolved); err != nil {
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
	// filepath.Join doesn't treat an absolute second argument specially —
	// Join(root, "/tmp/x") produces "root/tmp/x", not "/tmp/x", so an
	// absolute requested path would otherwise be silently re-rooted as if
	// it were relative instead of rejected, defeating the "rejecting
	// absolute paths" this function's callers document. Checked explicitly
	// rather than relied on to fall out of the Join+Rel check below, which
	// only catches ".." traversal.
	if filepath.IsAbs(requested) {
		return "", ErrOutsideRoot
	}
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

// ReadFull resolves and reads a file for the text editor, up to
// MaxEditorReadBytes. Unlike Read (sized for what's reasonable to hand a
// chat model), this is sized for what's reasonable to open in a browser
// editor tab — larger, but still bounded.
func (r *Reader) ReadFull(requested string) (content string, truncated bool, err error) {
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

	buf := make([]byte, MaxEditorReadBytes+1)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return "", false, err
	}
	if n > MaxEditorReadBytes {
		return string(buf[:MaxEditorReadBytes]), true, nil
	}
	return string(buf[:n]), false, nil
}

// ExistingContent reads a file's current content for diffing against a
// proposed write, if it exists. Returns ("", false, false, nil) for a
// not-yet-existing file (a new-file write), and a real error only for
// unexpected failures (e.g. it's a directory). Like Read, content over
// MaxReadBytes is cut off and truncated is reported true — callers MUST
// check it: existing built from a truncated read is missing its real
// tail, so an edit_file search/replace or a write_file overwrite built
// from it would silently drop everything past the cutoff if written to
// disk as-is. See runFileTools in internal/chat for the caller that
// actually enforces this.
func (r *Reader) ExistingContent(requested string) (content string, exists bool, truncated bool, err error) {
	path, err := r.Resolve(requested)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, false, nil
		}
		return "", false, false, err
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, false, nil
		}
		return "", false, false, err
	}
	if info.IsDir() {
		return "", false, false, fmt.Errorf("%q is a directory, not a file", requested)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false, false, err
	}
	defer f.Close()
	buf := make([]byte, MaxReadBytes+1)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return "", false, false, err
	}
	return string(buf[:min(n, MaxReadBytes)]), true, n > MaxReadBytes, nil
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

// Delete removes a file (not a directory — the editor only ever deletes
// single files, never recursively) from within the sandbox. Requires
// write access, same as Write.
func (r *Reader) Delete(requested string) error {
	path, err := r.Resolve(requested)
	if err != nil {
		return err
	}
	_, allowWrites := r.Snapshot()
	if !allowWrites {
		return errors.New("file writes are not enabled")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%q is a directory, not a file", requested)
	}
	return os.Remove(path)
}

// CountFilesUnder returns how many files (not directories themselves)
// exist under the sandboxed directory at requested — used to populate the
// "delete this folder and its N files?" confirmation copy before
// DeleteFolder is actually called, so the UI can tell the human what
// they're about to lose. Read-only; doesn't require write access.
func (r *Reader) CountFilesUnder(requested string) (int, error) {
	path, err := r.Resolve(requested)
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("%q is not a directory", requested)
	}
	count := 0
	err = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			count++
		}
		return nil
	})
	return count, err
}

// DeleteFolder recursively removes every file and subdirectory under the
// sandboxed directory at requested. Requires write access, same as
// Delete/Write. Unlike Delete (which explicitly refuses a directory
// target — see that method's doc comment), this is folder-only and
// refuses a plain file target instead, so the two methods can't be
// confused for one another at a call site.
func (r *Reader) DeleteFolder(requested string) error {
	path, err := r.Resolve(requested)
	if err != nil {
		return err
	}
	_, allowWrites := r.Snapshot()
	if !allowWrites {
		return errors.New("file writes are not enabled")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", requested)
	}
	return os.RemoveAll(path)
}

// Rename moves a file OR a folder (recursively, since a move is a single
// os.Rename syscall regardless of what's on either end) from one
// sandboxed path to another — used for renaming in place, moving to a
// different folder within the root, and the file tree's drag-and-drop
// (see FileTree.jsx's movePath, the only caller that ever passes a
// directory source). The destination must not already exist (no silent
// overwrite) and its parent directories are created as needed, same as
// Write.
func (r *Reader) Rename(fromRequested, toRequested string) error {
	from, err := r.Resolve(fromRequested)
	if err != nil {
		return err
	}
	_, allowWrites := r.Snapshot()
	if !allowWrites {
		return errors.New("file writes are not enabled")
	}
	if _, err := os.Stat(from); err != nil {
		return err
	}
	to, err := r.ResolveForWrite(toRequested)
	if err != nil {
		return err
	}
	if _, err := os.Stat(to); err == nil {
		return fmt.Errorf("%q already exists", toRequested)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	return os.Rename(from, to)
}
