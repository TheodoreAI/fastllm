package execution

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Isolated backends grant commands read access to the toolchain the
// workspace resolves, so builds and tests work inside the sandbox.

// goReadPaths finds the module cache the workspace's toolchain uses. The
// toolchain itself lives inside it when Go auto-selected one, as it does for a
// go.mod newer than the installed release. It runs the host's go, through the
// local backend, with no enclosing scope: this is controller work, not a
// model command.
func goReadPaths(workspace string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := RunLocal(ctx, workspace, LocalPolicy(), Command{Executable: "go", Args: []string{"env", "GOROOT", "GOMODCACHE"}})
	if err != nil || result.Err() != nil {
		return nil
	}
	var found []string
	for _, line := range strings.Split(result.Stdout, "\n") {
		path := strings.TrimSpace(line)
		if path == "" || !filepath.IsAbs(path) {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			found = append(found, path)
		}
	}
	// Parents first, so a toolchain inside the module cache is covered by the
	// cache's grant instead of being walked twice.
	sort.Slice(found, func(i, j int) bool { return len(found[i]) < len(found[j]) })
	var paths []string
	for _, path := range found {
		if !within(path, paths) {
			paths = append(paths, path)
		}
	}
	return paths
}

// within reports whether path is one of roots or inside one of them.
func within(path string, roots []string) bool {
	for _, root := range roots {
		if rel, err := filepath.Rel(root, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
