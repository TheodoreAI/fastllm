package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"fastllm/internal/llm"
	"fastllm/internal/media"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type fileItem struct {
	name  string
	path  string
	isDir bool
	size  int64
	mime  string
}

type filesPicker struct {
	currentDir   string
	entries      []fileItem
	cursor       int
	creatingDir  bool
	newDirInput  string
	statusNotice string
}

func (m *teaModel) openFilesModal(dir string) {
	if dir == "" {
		dir = m.workingDir
	} else if !filepath.IsAbs(dir) {
		dir = filepath.Join(m.workingDir, dir)
	}

	picker := &filesPicker{currentDir: dir}
	picker.loadEntries()
	m.filesModal = picker
	m.input.Blur()
}

func (m *teaModel) closeFilesModal() {
	m.filesModal = nil
	// Only focus if input has been initialized
	if m.input.Width() > 0 || m.input.Focused() {
		m.input.Focus()
	}
}

func (p *filesPicker) loadEntries() {
	p.entries = nil
	p.cursor = 0

	// Add parent directory entry ".." if inside workspace or has parent
	parent := filepath.Dir(p.currentDir)
	if parent != p.currentDir {
		p.entries = append(p.entries, fileItem{
			name:  "..",
			path:  parent,
			isDir: true,
		})
	}

	dirEntries, err := os.ReadDir(p.currentDir)
	if err != nil {
		p.statusNotice = "Failed to read directory: " + err.Error()
		return
	}

	var dirs, files []fileItem
	for _, de := range dirEntries {
		name := de.Name()
		// Skip hidden dot files except .fastllm
		if strings.HasPrefix(name, ".") && name != ".fastllm" {
			continue
		}
		fullPath := filepath.Join(p.currentDir, name)
		info, err := de.Info()
		if err != nil {
			continue
		}

		if de.IsDir() {
			dirs = append(dirs, fileItem{
				name:  name + "/",
				path:  fullPath,
				isDir: true,
			})
		} else {
			mime, _ := media.DetectMime(nil, name)
			files = append(files, fileItem{
				name:  name,
				path:  fullPath,
				isDir: false,
				size:  info.Size(),
				mime:  mime,
			})
		}
	}

	sort.Slice(dirs, func(i, j int) bool { return dirs[i].name < dirs[j].name })
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })

	p.entries = append(p.entries, dirs...)
	p.entries = append(p.entries, files...)
}

func (m *teaModel) handleFilesModalKey(msg tea.KeyPressMsg) tea.Cmd {
	p := m.filesModal
	if p == nil {
		return nil
	}

	if p.creatingDir {
		switch msg.String() {
		case "esc":
			p.creatingDir = false
			p.newDirInput = ""
			return nil
		case "enter":
			name := strings.TrimSpace(p.newDirInput)
			if name != "" {
				target := filepath.Join(p.currentDir, name)
				if err := os.MkdirAll(target, 0o755); err != nil {
					p.statusNotice = "Error creating folder: " + err.Error()
				} else {
					p.statusNotice = "✓ Created folder " + name
					p.loadEntries()
				}
			}
			p.creatingDir = false
			p.newDirInput = ""
			return nil
		case "backspace":
			if len(p.newDirInput) > 0 {
				p.newDirInput = p.newDirInput[:len(p.newDirInput)-1]
			}
			return nil
		default:
			p.newDirInput += msg.Text
		}
		return nil
	}

	switch msg.String() {
	case "esc", "ctrl+c":
		m.closeFilesModal()
		return nil

	case "up", "ctrl+p":
		if p.cursor > 0 {
			p.cursor--
		}

	case "down", "ctrl+n":
		if p.cursor < len(p.entries)-1 {
			p.cursor++
		}

	case "enter":
		if len(p.entries) == 0 {
			return nil
		}
		item := p.entries[p.cursor]
		if item.isDir {
			p.currentDir = item.path
			p.loadEntries()
			return nil
		}
		// Attaching file to chat prompt
		return m.attachFileAndCloseModal(item.path)

	case "q":
		m.closeFilesModal()
		return nil
	case "a":
		if len(p.entries) > 0 && !p.entries[p.cursor].isDir {
			return m.attachFileAndCloseModal(p.entries[p.cursor].path)
		}
	case "n":
		p.creatingDir = true
		p.newDirInput = ""
		return nil
	case "e":
		if len(p.entries) > 0 && !p.entries[p.cursor].isDir {
			targetPath := p.entries[p.cursor].path
			m.closeFilesModal()
			return m.handleShellSubmit("edit " + targetPath)
		}
	}
	return nil
}

func (m *teaModel) attachFileAndCloseModal(path string) tea.Cmd {
	mime, dataURI, raw, err := media.FileToDataURI(path)
	if err != nil {
		m.statusNotice = "Attach failed: " + err.Error()
		m.closeFilesModal()
		return m.clearStatusAfter(3 * time.Second)
	}

	attType := "image"
	var extracted string
	if media.IsPDF(mime) {
		attType = "pdf"
		extracted, _ = media.ExtractPDFText(raw)
	}

	stagedPath, _ := media.StageAttachment(m.workingDir, filepath.Base(path), raw)
	if stagedPath == "" {
		stagedPath = path
	}

	att := llm.Attachment{
		Type:      attType,
		MimeType:  mime,
		Name:      filepath.Base(path),
		Path:      stagedPath,
		DataURI:   dataURI,
		Extracted: extracted,
	}
	m.pendingAttachments = append(m.pendingAttachments, att)
	m.statusNotice = fmt.Sprintf("✓ Attached %s", att.Name)
	m.appendHistory(styleStatusNotice.Render(fmt.Sprintf("✓ Attached %s from files browser.\n\n", att.Name)))
	m.closeFilesModal()
	return m.clearStatusAfter(3 * time.Second)
}

func (m *teaModel) renderFilesModal() string {
	p := m.filesModal
	if p == nil {
		return ""
	}

	width := m.width
	if width < 1 {
		width = 80
	}
	height := m.height
	if height < 1 {
		height = 24
	}
	modalWidth := 74
	if width < modalWidth+4 {
		modalWidth = width - 4
	}

	var sb strings.Builder
	relDir, _ := filepath.Rel(m.workingDir, p.currentDir)
	if relDir == "" || relDir == "." {
		relDir = "workspace root"
	}
	title := fmt.Sprintf("📁 Workspace Files — %s", relDir)
	sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(tuiColorCyan).Render(title) + "\n\n")

	if p.creatingDir {
		sb.WriteString(lipgloss.NewStyle().Foreground(tuiColorYellow).Bold(true).
			Render("New subfolder name: ") + p.newDirInput + "█\n")
		sb.WriteString(styleMuted.Render("[Enter: Create • Esc: Cancel]\n\n"))
	} else if p.statusNotice != "" {
		sb.WriteString(styleStatusNotice.Render(p.statusNotice) + "\n\n")
	}

	maxVisible := height - 12
	if maxVisible < 5 {
		maxVisible = 5
	}

	start := 0
	if p.cursor >= maxVisible {
		start = p.cursor - maxVisible + 1
	}
	end := start + maxVisible
	if end > len(p.entries) {
		end = len(p.entries)
	}

	for i := start; i < end; i++ {
		item := p.entries[i]
		selected := i == p.cursor

		var icon string
		if item.isDir {
			icon = "📁"
		} else if media.IsPDF(item.mime) {
			icon = "📎"
		} else if media.IsImage(item.mime) {
			icon = "📷"
		} else {
			icon = "📄"
		}

		nameStr := fmt.Sprintf("%s %s", icon, item.name)
		var sizeStr string
		if !item.isDir && item.size > 0 {
			if item.size >= 1024*1024 {
				sizeStr = fmt.Sprintf("%.1f MB", float64(item.size)/(1024*1024))
			} else if item.size >= 1024 {
				sizeStr = fmt.Sprintf("%.1f KB", float64(item.size)/1024)
			} else {
				sizeStr = fmt.Sprintf("%d B", item.size)
			}
		}

		rowContent := PadRight(nameStr, modalWidth-18) + padLeft(sizeStr, 14)
		if selected {
			rowContent = lipgloss.NewStyle().
				Background(tuiColorBorder).
				Foreground(tuiColorWhite).
				Bold(true).
				Render("› " + rowContent)
		} else {
			rowContent = "  " + rowContent
		}
		sb.WriteString(rowContent + "\n")
	}

	sb.WriteString("\n" + styleMuted.Render("↑/↓: Navigate  •  Enter/a: Attach to chat  •  n: New folder  •  e: Edit  •  Esc: Close"))

	contentBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(tuiColorCyan).
		Padding(1, 2).
		Width(modalWidth).
		Render(sb.String())

	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, contentBox)
}

func padLeft(s string, width int) string {
	vl := VisualLen(s)
	if vl >= width {
		return s
	}
	return strings.Repeat(" ", width-vl) + s
}
