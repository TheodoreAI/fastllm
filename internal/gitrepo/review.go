package gitrepo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Root discovers the containing working tree without changing the caller's workspace.
func Root(ctx context.Context, dir string) (string, error) {
	out, err := run(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		if strings.Contains(err.Error(), "not a git repository") {
			return "", ErrNotARepo
		}
		return "", err
	}
	return filepath.Clean(strings.TrimSpace(out)), nil
}

func splitNUL(out string) []string {
	if out == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
}

// ReviewDiff compares just the index or just the working tree, with full context.
func ReviewDiff(ctx context.Context, root, path string, staged bool, original ...string) (string, error) {
	args := []string{"diff", "-M", "--no-color", "--no-ext-diff", "--no-textconv", "-U" + strconv.Itoa(FullFileContext)}
	if staged {
		args = append(args, "--cached")
	}
	args = append(args, "--", path)
	args = append(args, original...)
	return run(ctx, root, args...)
}

// ConflictDiff compares our index stage with the working file, including conflict markers.
func ConflictDiff(ctx context.Context, root, path string) (string, error) {
	return run(ctx, root, "diff", "--ours", "--no-color", "--no-ext-diff", "--no-textconv", "-U"+strconv.Itoa(FullFileContext), "--", path)
}

type CommitInfo struct {
	ID, Subject, Author, Timestamp, Message string
}

// History returns commits reachable from HEAD, newest first, with a bounded page size.
func History(ctx context.Context, root string, offset, limit int) ([]CommitInfo, error) {
	if offset < 0 || limit < 1 || limit > 50 {
		return nil, fmt.Errorf("invalid history page")
	}
	if _, err := run(ctx, root, "rev-parse", "--verify", "HEAD"); err != nil {
		if IsRepo(ctx, root) {
			return nil, nil
		}
		return nil, ErrNotARepo
	}
	out, err := run(ctx, root, "log", "--date=iso-strict", "--format=%H%x00%s%x00%an%x00%aI%x00", "--skip="+strconv.Itoa(offset), "-n", strconv.Itoa(limit), "HEAD", "--")
	if err != nil {
		return nil, err
	}
	fields := strings.Split(out, "\x00")
	var commits []CommitInfo
	for i := 0; i+3 < len(fields); i += 4 {
		commits = append(commits, CommitInfo{ID: strings.TrimSpace(fields[i]), Subject: fields[i+1], Author: fields[i+2], Timestamp: fields[i+3]})
	}
	return commits, nil
}

type CommitDetail struct {
	CommitInfo
	Parent string
	Files  []FileStatus
}

// Detail compares merges with their first parent and roots with an empty tree.
func Detail(ctx context.Context, root, id string) (CommitDetail, error) {
	var detail CommitDetail
	if !validObjectID(id) {
		return detail, fmt.Errorf("invalid commit ID")
	}
	out, err := run(ctx, root, "show", "-s", "--format=%H%x00%s%x00%an%x00%aI%x00%B%x00%P", id, "--")
	if err != nil {
		return detail, err
	}
	f := strings.SplitN(out, "\x00", 6)
	if len(f) != 6 {
		return detail, fmt.Errorf("invalid commit metadata")
	}
	detail.CommitInfo = CommitInfo{ID: f[0], Subject: f[1], Author: f[2], Timestamp: f[3], Message: f[4]}
	parents := strings.Fields(f[5])
	if len(parents) > 0 {
		detail.Parent = parents[0]
	}
	args := []string{"diff-tree", "--root", "--no-commit-id", "-r", "-M", "--name-status", "-z"}
	if detail.Parent != "" {
		args = append(args, detail.Parent)
	}
	args = append(args, id, "--")
	out, err = run(ctx, root, args...)
	if err != nil {
		return detail, err
	}
	f = splitNUL(out)
	for i := 0; i+1 < len(f); i += 2 {
		status, path := f[i], f[i+1]
		original := ""
		if (strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C")) && i+2 < len(f) {
			original = path
			i++
			path = f[i+1]
		}
		detail.Files = append(detail.Files, FileStatus{Path: path, OriginalPath: original, Staged: status})
	}
	return detail, nil
}

func CommitDiff(ctx context.Context, root string, detail CommitDetail, path string) (string, error) {
	if !validObjectID(detail.ID) || (detail.Parent != "" && !validObjectID(detail.Parent)) {
		return "", fmt.Errorf("invalid commit ID")
	}
	args := []string{"diff-tree", "--root", "--no-commit-id", "-p", "-M", "--no-color", "--no-ext-diff", "--no-textconv", "-U" + strconv.Itoa(FullFileContext)}
	if detail.Parent != "" {
		args = append(args, detail.Parent)
	}
	args = append(args, detail.ID, "--", path)
	for _, f := range detail.Files {
		if f.Path == path && f.OriginalPath != "" {
			args = append(args, f.OriginalPath)
		}
	}
	return run(ctx, root, args...)
}

func validObjectID(id string) bool {
	if len(id) != 40 && len(id) != 64 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// DeleteUntracked deletes only named untracked files; callers must confirm first.
func DeleteUntracked(ctx context.Context, root string, paths []string) error {
	status, err := Status(ctx, root)
	if err != nil {
		return err
	}
	untracked := map[string]bool{}
	for _, f := range status {
		if f.Unstaged == "?" {
			untracked[f.Path] = true
		}
	}
	for _, p := range paths {
		if !untracked[p] {
			return fmt.Errorf("%q is no longer untracked", p)
		}
	}
	for _, p := range paths {
		full := filepath.Join(root, filepath.FromSlash(p))
		rel, err := filepath.Rel(root, full)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return fmt.Errorf("path outside repository")
		}
		if err := os.Remove(full); err != nil {
			return err
		}
	}
	return nil
}
