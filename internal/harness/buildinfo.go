package harness

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
)

// BuildIdentity returns a short description of which build is running: the commit
// it was built from, whether the tree was dirty, and when.
//
// This comes from debug.ReadBuildInfo rather than -ldflags so it is populated by a
// plain `go build`, with no cooperation needed from whatever script did the build.
// It exists because fastllm legitimately has two binaries — a dev build in the
// repo and an installed one on PATH — and without a visible stamp there is no way
// to tell which one a session is running.
func BuildIdentity() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown build"
	}
	var revision, stamp string
	var dirty bool
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.time":
			stamp = setting.Value
		case "vcs.modified":
			dirty = setting.Value == "true"
		}
	}
	var parts []string
	if revision != "" {
		short := revision
		if len(short) > 7 {
			short = short[:7]
		}
		if dirty {
			short += "+dirty"
		}
		parts = append(parts, short)
	}
	if stamp != "" {
		if parsed, err := time.Parse(time.RFC3339, stamp); err == nil {
			parts = append(parts, parsed.Local().Format("2006-01-02 15:04"))
		}
	}
	if len(parts) == 0 {
		return "dev build"
	}
	return strings.Join(parts, " · ")
}

// runningExecutable returns the path of the running binary with the home
// directory abbreviated, short enough to sit on the banner line.
func runningExecutable() string {
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if strings.HasPrefix(path, home) {
			return "~" + filepath.ToSlash(strings.TrimPrefix(path, home))
		}
	}
	return filepath.ToSlash(path)
}
