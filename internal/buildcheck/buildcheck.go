// Package buildcheck lets the chat model verify its own proposed file
// writes before a human reviews them: it copies the sandboxed project
// into a temp directory, applies the proposed (not-yet-approved) content
// on top, and runs "go build ./..." there. Nothing in this package ever
// touches the real sandbox root — it only ever reads from it and writes
// into a throwaway copy, so a failing or even malicious build check can't
// affect the user's actual files.
package buildcheck

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Timeout bounds how long a single build check may run — generous enough
// for a small Go module on local hardware, short enough that one slow
// check doesn't stall the whole chat turn.
const Timeout = 60 * time.Second

// TestTimeout is Timeout's counterpart for "go test ./..." — tests
// legitimately take longer than a compile check (setup/teardown, actual
// test bodies running), so this gets a larger budget rather than sharing
// Timeout and risking a slow-but-passing suite being cut off mid-run.
const TestTimeout = 180 * time.Second

// MaxOutputBytes caps how much build output is fed back to the model —
// it only needs enough to see the first error(s), not a full flood.
const MaxOutputBytes = 8 * 1024

// dirsToSkip are excluded when copying the project into the scratch
// directory: version control metadata and dependency/build directories
// that are both huge and irrelevant to whether the source compiles (Go
// re-resolves its own module cache from GOPATH regardless of what's
// copied here).
var dirsToSkip = map[string]bool{
	".git":         true,
	"node_modules": true,
	"dist":         true,
}

// ErrNoGoModule means root doesn't look like a Go module, so there's
// nothing this package knows how to build-check yet.
var ErrNoGoModule = errors.New("buildcheck: no go.mod found at project root")

// Overlay is one proposed file write to layer on top of the copied
// project before building — path is relative to the project root, same
// as the write_file tool's argument.
type Overlay struct {
	Path    string
	Content string
}

// Result is the outcome of a build check.
type Result struct {
	Passed bool
	Output string
}

// Run copies root into a temp directory, applies overlays on top, and
// runs "go build ./..." there. root must contain a go.mod — callers
// should check for that (or catch ErrNoGoModule) before offering the
// run_build tool to the model at all.
func Run(ctx context.Context, root string, overlays []Overlay) (Result, error) {
	scratch, err := scratchCopy(root, overlays)
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(scratch)
	return runGoCommand(ctx, scratch, Timeout, "build", "./...")
}

// RunTests is Run's counterpart for "go test ./...": same scratch-copy-
// and-overlay mechanism (never touches the real sandbox root), so the
// model can check its proposed, not-yet-approved writes against the test
// suite the same way it can check they compile.
func RunTests(ctx context.Context, root string, overlays []Overlay) (Result, error) {
	scratch, err := scratchCopy(root, overlays)
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(scratch)
	return runGoCommand(ctx, scratch, TestTimeout, "test", "./...")
}

// RunTestsInPlace runs "go test ./..." directly against root — no scratch
// copy, no overlays. Meant for a human explicitly asking to run tests
// against their own already-saved files (see the editor's Test panel),
// where there's nothing unapproved to isolate: the human is the approval
// step, the same reasoning internal/chat/editor.go's other Editor* methods
// already apply to reads/writes/git actions on the real sandbox root.
func RunTestsInPlace(ctx context.Context, root string) (Result, error) {
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return Result{}, ErrNoGoModule
	}
	return runGoCommand(ctx, root, TestTimeout, "test", "./...")
}

// scratchCopy copies root into a fresh temp directory and applies
// overlays on top of it, returning the temp directory's path. The caller
// is responsible for removing it (os.RemoveAll) once done.
func scratchCopy(root string, overlays []Overlay) (string, error) {
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return "", ErrNoGoModule
	}

	scratch, err := os.MkdirTemp("", "fastllm-buildcheck-*")
	if err != nil {
		return "", err
	}

	if err := copyTree(root, scratch); err != nil {
		os.RemoveAll(scratch)
		return "", err
	}

	for _, ov := range overlays {
		dest, err := resolveOverlayPath(scratch, ov.Path)
		if err != nil {
			os.RemoveAll(scratch)
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			os.RemoveAll(scratch)
			return "", err
		}
		if err := os.WriteFile(dest, []byte(ov.Content), 0o644); err != nil {
			os.RemoveAll(scratch)
			return "", err
		}
	}
	return scratch, nil
}

// runGoCommand runs "go <args...>" in dir under timeout, capping captured
// output at MaxOutputBytes — the shared tail end of Run/RunTests/
// RunTestsInPlace, all of which differ only in which directory they point
// at and which go subcommand/timeout they use.
func runGoCommand(ctx context.Context, dir string, timeout time.Duration, args ...string) (Result, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "go", args...)
	cmd.Dir = dir
	output, runErr := cmd.CombinedOutput()

	text := string(output)
	if len(text) > MaxOutputBytes {
		text = text[:MaxOutputBytes] + "\n[output truncated]"
	}

	if runCtx.Err() != nil {
		return Result{Passed: false, Output: "timed out after " + timeout.String()}, nil
	}
	if runErr != nil {
		if text == "" {
			text = runErr.Error()
		}
		return Result{Passed: false, Output: text}, nil
	}
	return Result{Passed: true, Output: text}, nil
}

// resolveOverlayPath mirrors internal/files' traversal guard — an
// overlay path is only ever generated internally from an
// already-sandbox-validated PendingWrite.Path, but this keeps the
// package safe to call with arbitrary input on its own terms too.
func resolveOverlayPath(scratchRoot, requested string) (string, error) {
	joined := filepath.Join(scratchRoot, requested)
	rel, err := filepath.Rel(scratchRoot, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("buildcheck: overlay path escapes project root")
	}
	return joined, nil
}

func copyTree(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() && dirsToSkip[entry.Name()] {
			continue
		}
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err := os.MkdirAll(dstPath, 0o755); err != nil {
				return err
			}
			if err := copyTree(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue // skip symlinks — irrelevant for a build check, avoids escape edge cases
		}
		if err := copyFile(srcPath, dstPath, info.Mode()); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, mode)
}
