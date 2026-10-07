package gitrepo

import (
	"context"
	"fmt"
	"strings"

	"fastllm/internal/execution"
)

// HeadContents returns a stable HEAD blob, or empty content when HEAD/path
// does not exist. Query failures are errors, never interpreted as a new file.
func HeadContents(ctx context.Context, root, path string) (string, error) {
	if _, err := Root(ctx, root); err != nil {
		return "", err
	}
	result, err := execution.RunLocal(ctx, root, execution.LocalPolicy(), execution.Command{
		Executable: "git", Args: HardenedArgs("rev-parse", "--verify", "--quiet", "HEAD"),
	})
	if err != nil {
		return "", err
	}
	if result.ExitCode == 1 && result.Stderr == "" && result.Reason == "exit" {
		return "", nil // unborn branch
	}
	if err := result.Err(); err != nil {
		return "", fmt.Errorf("read HEAD: %w: %s", err, strings.TrimSpace(result.Stderr))
	}
	commit := strings.TrimSpace(result.Stdout)
	entries, err := run(ctx, root, "ls-tree", "-z", commit, "--", path)
	if err != nil {
		return "", err
	}
	if entries == "" {
		return "", nil
	}
	entry := splitNUL(entries)[0]
	metadata, _, _ := strings.Cut(entry, "\t")
	fields := strings.Fields(metadata)
	if len(fields) != 3 || fields[1] != "blob" {
		return "", fmt.Errorf("HEAD path is not a file: %s", path)
	}
	return run(ctx, root, "cat-file", "blob", fields[2])
}
