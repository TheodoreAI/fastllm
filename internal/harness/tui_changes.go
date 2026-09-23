package harness

import (
	"fastllm/internal/gitrepo"
	"fmt"
	"path/filepath"
	"strings"
)

// The changes column only appears when the terminal leaves the conversation a
// comfortable width; on narrower terminals it would squeeze the transcript
// into something unreadable, so it is hidden instead.
const (
	changesColumnMinFrame = 100
	changesColumnWidth    = 32
)

// fileChange is the running tally for one file modified or tracked this session.
type fileChange struct {
	Path    string
	Added   int
	Removed int
	Edits   int
	Written bool   // touched by write_file, so the +count is the whole file
	Staged  bool   // staged in git index
	Status  string // "S", "M", "D", "?", etc.
}

// sessionChanges records files modified by the agent's file tools or working tree
// status reported by git.
type sessionChanges struct {
	isGit        bool
	branch       string
	upstream     string
	ahead        int
	behind       int
	latestCommit string
	files        []fileChange
}

func (c *sessionChanges) Reset() {
	c.files = nil
	c.isGit = false
	c.branch = ""
	c.upstream = ""
	c.ahead = 0
	c.behind = 0
	c.latestCommit = ""
}

// UpdateFromGit updates the changes column with accurate working tree and git status.
func (c *sessionChanges) UpdateFromGit(status gitrepo.RepoStatus) {
	if !status.IsRepo {
		c.isGit = false
		return
	}
	c.isGit = true
	c.branch = status.Branch
	c.upstream = status.Upstream
	c.ahead = status.Ahead
	c.behind = status.Behind
	c.latestCommit = status.LatestCommit

	var files []fileChange
	for _, rf := range status.Files {
		staged := rf.Staged != ""
		statusMarker := rf.Unstaged
		if staged {
			if rf.Unstaged != "" {
				statusMarker = rf.Staged + rf.Unstaged
			} else {
				statusMarker = rf.Staged
			}
		}
		if statusMarker == "" {
			statusMarker = "M"
		}
		files = append(files, fileChange{
			Path:    filepath.ToSlash(rf.Path),
			Added:   rf.Added,
			Removed: rf.Removed,
			Staged:  staged,
			Status:  statusMarker,
		})
	}
	c.files = files
}

// Record folds one completed tool call into the tally. It reports whether the
// call was a successful file mutation.
func (c *sessionChanges) Record(workingDir, tool, arguments, result string) bool {
	if tool != "write_file" && tool != "edit_file" && tool != "patch_file" {
		return false
	}
	if !strings.HasPrefix(strings.TrimSpace(result), "Successfully") {
		return false
	}
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Search  string `json:"search"`
		Replace string `json:"replace"`
		Diff    string `json:"diff"`
	}
	if decodeToolArguments(arguments, &args) != nil || strings.TrimSpace(args.Path) == "" {
		return false
	}

	var added, removed int
	switch tool {
	case "write_file":
		added = changedLineCount(args.Content)
	case "edit_file":
		added, removed = changedLineCount(args.Replace), changedLineCount(args.Search)
	case "patch_file":
		added, removed = countDiffLines(args.Diff)
	}

	path := displayChangePath(workingDir, args.Path)
	entry := fileChange{Path: path, Status: "M"}
	for i, existing := range c.files {
		if existing.Path == path {
			entry = existing
			c.files = append(c.files[:i], c.files[i+1:]...)
			break
		}
	}
	entry.Added += added
	entry.Removed += removed
	entry.Edits++
	entry.Written = entry.Written || tool == "write_file"
	c.files = append([]fileChange{entry}, c.files...)
	return true
}

func (c *sessionChanges) Totals() (files, added, removed int) {
	for _, f := range c.files {
		added += f.Added
		removed += f.Removed
	}
	return len(c.files), added, removed
}

// Render draws the column as exactly height rows of exactly width columns, so
// joining it beside the viewport can never change the frame size.
func (c *sessionChanges) Render(width, height int) string {
	inner := width - 2 // "│ " separator on the left edge
	if inner < 1 || height < 1 {
		return ""
	}
	var rows []string
	count, added, removed := c.Totals()

	headerTitle := "Changes"
	if c.isGit && c.branch != "" {
		headerTitle = truncateText(c.branch, inner-12)
	}
	rows = append(rows, styleDiffHdr.Render(headerTitle)+styleMuted.Render(fmt.Sprintf(" · %d %s", count, plural(count, "file", "files"))))

	if c.isGit {
		var subParts []string
		if count > 0 {
			subParts = append(subParts, formatLineCounts(added, removed))
		}
		if c.ahead > 0 {
			subParts = append(subParts, styleDiffAdd.Render(fmt.Sprintf("↑%d", c.ahead)))
		}
		if c.behind > 0 {
			subParts = append(subParts, styleDiffDel.Render(fmt.Sprintf("↓%d", c.behind)))
		}
		if c.ahead == 0 && c.behind == 0 && c.upstream != "" {
			subParts = append(subParts, styleStatusNotice.Render("synced"))
		}
		if len(subParts) > 0 {
			rows = append(rows, strings.Join(subParts, " · "))
		}
	} else if count > 0 {
		rows = append(rows, formatLineCounts(added, removed))
	}

	rows = append(rows, styleMuted.Render(strings.Repeat("─", inner)))

	if count == 0 {
		if c.isGit {
			rows = append(rows, styleMuted.Render("Working tree clean."))
			if c.ahead > 0 {
				rows = append(rows, styleDiffAdd.Render(fmt.Sprintf("↑ %d unpushed %s", c.ahead, plural(c.ahead, "commit", "commits"))))
				rows = append(rows, styleMuted.Render("Push with 'git push'"))
			} else if c.behind > 0 {
				rows = append(rows, styleDiffDel.Render(fmt.Sprintf("↓ %d behind upstream", c.behind)))
			} else if c.upstream != "" {
				rows = append(rows, styleStatusNotice.Render(SymCheck+" Synced with remote"))
			}
			if c.latestCommit != "" && height-len(rows) >= 3 {
				rows = append(rows, "")
				rows = append(rows, styleMuted.Render("Latest commit:"))
				rows = append(rows, styleMuted.Render(truncateText(c.latestCommit, inner)))
			}
		} else {
			rows = append(rows, styleMuted.Render("No files changed yet."))
		}
	} else {
		listRows := height - len(rows)
		shown := c.files
		if len(shown) > listRows {
			// Give up one list row to the "… N more" line.
			shown = shown[:max(listRows-1, 0)]
		}
		for _, f := range shown {
			rows = append(rows, formatChangeRow(f, inner))
		}
		if hidden := len(c.files) - len(shown); hidden > 0 {
			rows = append(rows, styleMuted.Render(fmt.Sprintf("… %d more", hidden)))
		}
	}

	sep := styleMuted.Render("│") + " "
	var b strings.Builder
	for i := 0; i < height; i++ {
		row := ""
		if i < len(rows) {
			row = rows[i]
		}
		b.WriteString(sep + PadRight(clampToWidth(row, inner), inner))
		if i < height-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func formatChangeRow(f fileChange, width int) string {
	marker := styleDiffHdr.Render("M")
	if f.Staged {
		marker = styleDiffAdd.Render("S")
	} else if f.Status == "?" {
		marker = styleMuted.Render("?")
	} else if f.Status == "D" {
		marker = styleDiffDel.Render("D")
	} else if f.Written {
		marker = styleDiffAdd.Render("W")
	}
	counts := formatLineCounts(f.Added, f.Removed)
	pathWidth := width - 2 - VisualLen(counts) - 1
	path := truncatePathLeft(f.Path, pathWidth)
	gap := width - 2 - VisualLen(path) - VisualLen(counts)
	if gap < 1 {
		gap = 1
	}
	return marker + " " + path + strings.Repeat(" ", gap) + counts
}

func formatLineCounts(added, removed int) string {
	return styleDiffAdd.Render(fmt.Sprintf("+%d", added)) + " " + styleDiffDel.Render(fmt.Sprintf("-%d", removed))
}

// truncatePathLeft keeps the end of a path, where the file name is, and drops
// leading directories when it is too long to fit.
func truncatePathLeft(path string, width int) string {
	if width < 2 {
		return ""
	}
	runes := []rune(path)
	if len(runes) <= width {
		return path
	}
	return "…" + string(runes[len(runes)-width+1:])
}

// displayChangePath shows paths relative to the working directory when they
// live inside it, so the same file is one entry however the model spelled it.
func displayChangePath(workingDir, path string) string {
	clean := filepath.Clean(path)
	if workingDir != "" {
		abs := clean
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(workingDir, abs)
		}
		if rel, err := filepath.Rel(workingDir, abs); err == nil && !strings.HasPrefix(rel, "..") {
			clean = rel
		}
	}
	return filepath.ToSlash(clean)
}

// changedLineCount counts lines without treating a trailing newline as an extra
// empty line, unlike countLines.
func changedLineCount(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(text, "\n"), "\n") + 1
}

func countDiffLines(diff string) (added, removed int) {
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return added, removed
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
