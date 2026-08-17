// Package folderpicker shows a native OS folder-selection dialog on the
// machine running the fastllm server, for the "Choose folder" button in
// Settings → File access. A browser's own <input type=file webkitdirectory>
// never exposes an absolute filesystem path (only a folder-name hint, for
// browser sandboxing reasons that apply even to a locally-run app like this
// one) — so getting a real, usable path requires asking the OS directly on
// the server side instead of the browser.
//
// Choose is implemented per-platform (see folderpicker_windows.go,
// folderpicker_darwin.go, folderpicker_other.go) since each OS's native
// folder dialog is reached through a different mechanism. All of them
// block until the user picks a folder or cancels, and all return
// ErrCancelled/ErrUnsupported for those cases so callers don't need to
// know which platform they're on.
package folderpicker

import "errors"

// ErrCancelled is returned when the user closes the dialog without
// choosing a folder.
var ErrCancelled = errors.New("folder selection was cancelled")

// ErrUnsupported is returned on platforms without an implementation. The
// "Choose folder" button in Settings still degrades gracefully on these
// platforms — the root field accepts a typed/pasted path directly.
var ErrUnsupported = errors.New("folder picker is not supported on this platform")
