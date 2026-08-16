// Package files provides sandboxed, read-only file access for the chat
// model's read_file tool. Every path is resolved and confirmed to stay
// inside a single allow-listed root directory before any I/O happens.
package files

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// MaxReadBytes caps how much of a file is returned to the model — large
// enough for source files and docs, small enough to not blow the context
// window or the response size on a misclick against a huge file.
const MaxReadBytes = 64 * 1024

var ErrOutsideRoot = errors.New("path is outside the allowed directory")

// Reader resolves paths against a fixed root and reads files from within
// it. The zero value (empty Root) means the feature is disabled — callers
// should check Enabled() before registering the tool.
type Reader struct {
	Root string
}

func New(root string) *Reader {
	if root == "" {
		return &Reader{}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return &Reader{}
	}
	return &Reader{Root: abs}
}

func (r *Reader) Enabled() bool {
	return r.Root != ""
}

// Resolve joins the requested path onto Root and confirms the result is
// still inside Root — rejecting absolute paths, ".." traversal, and
// symlinks that escape the sandbox.
func (r *Reader) Resolve(requested string) (string, error) {
	if !r.Enabled() {
		return "", errors.New("file access is not enabled")
	}
	joined := filepath.Join(r.Root, requested)
	rel, err := filepath.Rel(r.Root, joined)
	if err != nil || rel == ".." || len(rel) >= 2 && rel[:2] == ".." {
		return "", ErrOutsideRoot
	}

	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		return "", err
	}
	realRoot, err := filepath.EvalSymlinks(r.Root)
	if err != nil {
		return "", err
	}
	relResolved, err := filepath.Rel(realRoot, resolved)
	if err != nil || relResolved == ".." || (len(relResolved) >= 2 && relResolved[:2] == "..") {
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
