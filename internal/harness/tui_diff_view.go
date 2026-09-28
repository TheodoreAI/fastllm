package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fastllm/internal/gitrepo"
)

// splitDiffMinWidth is the narrowest terminal the diff modal lays out side
// by side; below it each column would be too narrow to read code in.
const splitDiffMinWidth = 100

// diffSource is what the diff modal compares.
type diffSource int

const (
	diffSourceHead     diffSource = iota // HEAD with the working tree
	diffSourceUnstaged                   // the index with the working tree
	diffSourceStaged                     // HEAD with the index
	diffSourceCount
)

// label names the source in the modal's key legend.
func (s diffSource) label() string {
	switch s {
	case diffSourceUnstaged:
		return "Unstaged"
	case diffSourceStaged:
		return "Staged"
	default:
		return "HEAD ↔ Working tree"
	}
}

// sides names the two versions compared, old first.
func (s diffSource) sides() (before, after string) {
	switch s {
	case diffSourceUnstaged:
		return "Index", "Working tree"
	case diffSourceStaged:
		return "HEAD", "Index (staged)"
	default:
		return "HEAD", "Working tree"
	}
}

// currentDiffFile is the file the diff modal is showing.
func (m *teaModel) currentDiffFile() (fileChange, bool) {
	if m.diffCursor < 0 || m.diffCursor >= len(m.changes.files) {
		return fileChange{}, false
	}
	return m.changes.files[m.diffCursor], true
}

// diffModalGeometry sizes the diff modal for the terminal. Side by side it
// takes the full width; the single-column view stays at a readable 120.
func (m *teaModel) diffModalGeometry() (modalWidth, vpWidth, vpHeight int, split bool) {
	width, height := m.width, m.height
	if width < 1 {
		width = 80
	}
	if height < 1 {
		height = 24
	}
	split = !m.diffUnified && width >= splitDiffMinWidth
	modalWidth = width - 6
	if split {
		modalWidth = width - 4
	} else if modalWidth > 120 {
		modalWidth = 120
	}
	modalWidth = max(modalWidth, 40)
	vpWidth = modalWidth - 4
	vpHeight = max(height-4-7, 3)
	if split {
		vpHeight = max(vpHeight-1, 3) // the column titles take a row
	}
	return modalWidth, vpWidth, vpHeight, split
}

// loadDiffText fetches the current file's diff for the modal's source and
// lays it out. Side by side, the diff carries the whole file, so both
// columns read as complete versions rather than hunks.
func (m *teaModel) loadDiffText() {
	f, ok := m.currentDiffFile()
	if !ok {
		return
	}
	_, vpWidth, vpHeight, split := m.diffModalGeometry()
	m.diffViewport.SetWidth(vpWidth)
	m.diffViewport.SetHeight(vpHeight)

	var text string
	switch {
	case !m.checkpointMgr.IsGitRepo():
		text = fmt.Sprintf("File: %s\nAdded: +%d lines\nRemoved: -%d lines", f.Path, f.Added, f.Removed)
	case f.Status == "?":
		fullPath := filepath.Join(m.workingDir, filepath.FromSlash(f.Path))
		if data, err := os.ReadFile(fullPath); err == nil {
			text = FormatUntrackedAsDiff(f.Path, string(data))
		} else {
			text = fmt.Sprintf("Error reading untracked file %s: %v", f.Path, err)
		}
	default:
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var err error
		switch m.diffSource {
		case diffSourceHead:
			contextLines := 3
			if split {
				contextLines = gitrepo.FullFileContext
			}
			text, err = gitrepo.DiffHEAD(ctx, m.workingDir, f.Path, contextLines)
		default:
			text, err = gitrepo.Diff(ctx, m.workingDir, f.Path, m.diffSource == diffSourceStaged)
		}
		cancel()
		if err != nil {
			text = fmt.Sprintf("Diff error: %v", err)
		}
	}
	if strings.TrimSpace(text) == "" {
		switch m.diffSource {
		case diffSourceStaged:
			text = "No staged changes in this file. Press 'd' to compare something else."
		case diffSourceUnstaged:
			text = "No unstaged changes in this file. Press 'd' to compare something else."
		default:
			text = "No changes from HEAD in this file."
		}
	}
	m.diffText = text
	m.diffShownSplit = split
	m.layoutDiffContent()
	m.diffViewport.GotoTop()
}

// layoutDiffContent renders diffText into the viewport for its current
// size, side by side or single-column. A width change keeps the scroll
// position; a change between layouts refetches, since the side-by-side
// view needs the whole file.
func (m *teaModel) layoutDiffContent() {
	_, vpWidth, _, split := m.diffModalGeometry()
	if split != m.diffShownSplit && m.diffText != "" && m.diffSource == diffSourceHead {
		if f, ok := m.currentDiffFile(); ok && f.Status != "?" {
			m.diffShownSplit = split
			m.loadDiffText()
			return
		}
	}
	m.diffShownSplit = split

	files := ParseDiffForDisplay(strings.TrimRight(m.diffText, "\r\n"))
	if !split || len(files) == 0 || files[0].MaxLine == 0 {
		// Not a diff (an error or a message), or not split: the usual view.
		content := HighlightDiffFile(m.diffText, m.diffPath)
		if len(files) == 0 || files[0].MaxLine == 0 {
			content = styleMuted.Render(m.diffText)
		}
		m.diffViewport.SetContent(content)
		return
	}
	offset := m.diffViewport.YOffset()
	rows := RenderSplitDiff(files[0], DetectLanguage(m.diffPath), vpWidth)
	m.diffViewport.SetContent(strings.Join(rows, "\n"))
	m.diffViewport.SetYOffset(offset)
}
