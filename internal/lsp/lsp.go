// Package lsp bridges a single gopls subprocess to the in-app editor over
// a WebSocket, giving Go files real go-to-definition, autocomplete,
// diagnostics, and hover — the same semantic features JS/TS files get
// today from internal/lint, but as a persistent language server session
// instead of a one-shot linter run per save. Unlike internal/lint's
// findOxlint (which walks the *target* project for a per-project install,
// since a JS project's own oxlint config/plugins matter), gopls is a
// global Go-toolchain binary — Available() checks PATH once, at
// appserver.Build time, not per file.
//
// There is exactly one live Session at a time, for whatever project root
// files.Reader currently points at (see Registry) — gopls itself
// multiplexes every open file over one JSON-RPC connection, so this
// package mirrors that 1:1 rather than spawning one process per tab.
package lsp

import (
	"errors"
	"os/exec"
)

// ErrUnavailable means gopls wasn't found on PATH — callers should treat
// this as "Go language features are simply off," not a failure worth
// surfacing to the user, matching internal/lint's ErrNotSupported.
var ErrUnavailable = errors.New("lsp: gopls not found on PATH")

// Available reports whether gopls is installed. Checked once when the
// server starts (see appserver.Build) — if false, /api/editor/lsp/ws is
// never registered at all, so a missing gopls looks identical to the
// route not existing, the same "no user-facing toggle, just graceful
// absence" shape internal/lint uses for oxlint.
func Available() bool {
	_, err := exec.LookPath("gopls")
	return err == nil
}
