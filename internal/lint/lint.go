// Package lint runs oxlint against a single JS/JSX file the in-app editor
// just saved, so the Editor tab can show inline diagnostics without the
// user leaving fastllm to run a linter by hand. It only ever lints one
// file at a time (triggered by a save), and only when the *target
// project* (whatever folder file access is pointed at, not fastllm's own
// checkout) has its own oxlint installed — running fastllm's bundled
// oxlint against someone else's project would apply the wrong rules
// against the wrong node_modules.
package lint

import (
	"context"
	"encoding/json"
	"errors"
	"fastllm/internal/execution"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Timeout bounds a single lint run — oxlint is a single-file, Rust-based
// linter, so this is generous; it exists only to stop a runaway/hung
// process from blocking a save indefinitely.
const Timeout = 10 * time.Second

// jsExtensions are the file extensions oxlint is asked to check. Anything
// else is skipped without even searching for an oxlint binary.
var jsExtensions = map[string]bool{
	".js":  true,
	".jsx": true,
	".mjs": true,
	".cjs": true,
	".ts":  true,
	".tsx": true,
}

// ErrNotSupported means the file extension isn't one oxlint checks, or no
// oxlint install could be found for this file's project — callers should
// treat this as "nothing to report," not a failure.
var ErrNotSupported = errors.New("lint: no oxlint available for this file")

// Diagnostic is one oxlint finding, positioned as a byte offset/length
// into the file content — matches what CodeMirror's @codemirror/lint
// panel expects for from/to, so the frontend needs no line/column math.
// Severity is always exactly "error" or "warning" (see normalizeSeverity)
// — the frontend's @codemirror/lint severity prop only accepts those two
// values plus "info", so this package normalizes at the source rather
// than each caller having to know oxlint's full severity vocabulary
// (oxlint also emits "advice" for some rules) and re-collapse it itself.
type Diagnostic struct {
	Message  string `json:"message"`
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Offset   int    `json:"offset"`
	Length   int    `json:"length"`
}

// normalizeSeverity maps oxlint's severity vocabulary (which includes at
// least "error", "warning", and "advice") down to the two values
// Diagnostic.Severity ever holds. Unrecognized/future values fall back to
// "warning" rather than "error", so an oxlint upgrade adding a new
// severity level can't start surfacing informational findings as if they
// were build-breaking.
func normalizeSeverity(s string) string {
	if s == "error" {
		return "error"
	}
	return "warning"
}

// Lint runs oxlint against absPath (the file the editor just saved to
// disk) and returns its findings. absPath must be an absolute path
// on-disk — the caller (EditorSaveFile) already resolved it against the
// sandbox root before writing.
func Lint(ctx context.Context, absPath string) ([]Diagnostic, error) {
	ext := filepath.Ext(absPath)
	if !jsExtensions[ext] {
		return nil, ErrNotSupported
	}

	binPath, workDir, err := findOxlint(absPath)
	if err != nil {
		return nil, ErrNotSupported
	}

	runCtx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()

	result, err := execution.RunLocal(runCtx, workDir, execution.LocalPolicy(), execution.Command{Executable: binPath, Args: []string{"--format", "json", absPath}, Timeout: Timeout})
	if err != nil {
		return nil, err
	}
	if result.Truncated {
		return nil, errors.New("lint output exceeded capture limit")
	}
	// A nonzero exit with JSON diagnostics is a normal lint result.
	output := []byte(result.Stdout)

	var parsed struct {
		Diagnostics []struct {
			Message  string `json:"message"`
			Code     string `json:"code"`
			Severity string `json:"severity"`
			Labels   []struct {
				Span struct {
					Offset int `json:"offset"`
					Length int `json:"length"`
				} `json:"span"`
			} `json:"labels"`
		} `json:"diagnostics"`
	}
	if err := json.Unmarshal(output, &parsed); err != nil {
		return nil, ErrNotSupported
	}

	diagnostics := make([]Diagnostic, 0, len(parsed.Diagnostics))
	for _, d := range parsed.Diagnostics {
		diag := Diagnostic{Message: d.Message, Rule: d.Code, Severity: normalizeSeverity(d.Severity)}
		if len(d.Labels) > 0 {
			diag.Offset = d.Labels[0].Span.Offset
			diag.Length = d.Labels[0].Span.Length
		}
		diagnostics = append(diagnostics, diag)
	}
	return diagnostics, nil
}

// findOxlint walks upward from the saved file's directory looking for
// node_modules/.bin/oxlint(.cmd) — the target project's own install,
// never fastllm's — and returns the binary path plus the directory
// oxlint should run in (so it picks up that project's .oxlintrc.json).
// Stops at the first node_modules found; if none has an oxlint binary,
// returns an error and the caller skips linting for this file.
func findOxlint(startPath string) (binPath, workDir string, err error) {
	name := "oxlint"
	if runtime.GOOS == "windows" {
		name = "oxlint.cmd"
	}

	dir := filepath.Dir(startPath)
	for {
		candidate := filepath.Join(dir, "node_modules", ".bin", name)
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate, dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", errors.New("lint: no oxlint install found")
		}
		dir = parent
	}
}
