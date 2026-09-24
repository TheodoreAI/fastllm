package harness

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fastllm/internal/llm"
	"fastllm/internal/media"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
)

func (m *teaModel) handlePasteCommand() tea.Cmd {
	cm, err := ReadClipboardMedia()
	if err != nil {
		m.statusNotice = "Clipboard read error: " + err.Error()
		m.appendHistory(styleDiffDel.Render(fmt.Sprintf("Clipboard error: %v\n\n", err)))
		return m.clearStatusAfter(3 * time.Second)
	}

	if cm.IsMedia {
		stagedPath, err := media.StageAttachment(m.workingDir, cm.Filename, cm.Raw)
		if err != nil {
			m.statusNotice = "Staging failed: " + err.Error()
			m.appendHistory(styleDiffDel.Render(fmt.Sprintf("Failed to stage attachment: %v\n\n", err)))
			return m.clearStatusAfter(3 * time.Second)
		}

		attType := "image"
		var extracted string
		if media.IsPDF(cm.MimeType) {
			attType = "pdf"
			extracted, _ = media.ExtractPDFText(cm.Raw)
		}

		att := llm.Attachment{
			Type:      attType,
			MimeType:  cm.MimeType,
			Name:      filepath.Base(stagedPath),
			Path:      stagedPath,
			DataURI:   media.BuildDataURI(cm.MimeType, cm.Raw),
			Extracted: extracted,
		}
		m.pendingAttachments = append(m.pendingAttachments, att)
		m.statusNotice = fmt.Sprintf("✓ Attached %s (%s)", att.Name, cm.MimeType)
		m.appendHistory(styleStatusNotice.Render(fmt.Sprintf("✓ Attached %s from clipboard. Type your prompt and press Enter.\n\n", att.Name)))
		return m.clearStatusAfter(3 * time.Second)
	}

	// Plain text clipboard fallback
	text, err := clipboard.ReadAll()
	if err == nil && strings.TrimSpace(text) != "" {
		m.input.InsertString(text)
		m.statusNotice = "Pasted text from clipboard"
		return m.clearStatusAfter(2 * time.Second)
	}

	m.statusNotice = "Clipboard is empty or unsupported format"
	m.appendHistory(styleMuted.Render("Clipboard is empty or does not contain an image, PDF, or text.\n\n"))
	return m.clearStatusAfter(3 * time.Second)
}

func (m *teaModel) handleAttachCommand(parts []string) tea.Cmd {
	if len(parts) < 2 {
		m.appendHistory(styleMuted.Render("Usage: /attach <file-path> [prompt...]\n\n"))
		return nil
	}

	filePath := strings.Trim(parts[1], "\"'")
	targetPath := filePath
	if strings.HasPrefix(targetPath, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			targetPath = filepath.Join(home, targetPath[2:])
		}
	} else if !filepath.IsAbs(targetPath) {
		targetPath = filepath.Join(m.workingDir, targetPath)
	}

	mime, dataURI, raw, err := media.FileToDataURI(targetPath)
	if err != nil {
		m.statusNotice = "Attach error: " + err.Error()
		m.appendHistory(styleDiffDel.Render(fmt.Sprintf("Could not attach file: %v\n\n", err)))
		return m.clearStatusAfter(3 * time.Second)
	}

	attType := "image"
	var extracted string
	if media.IsPDF(mime) {
		attType = "pdf"
		extracted, _ = media.ExtractPDFText(raw)
	}

	stagedPath, _ := media.StageAttachment(m.workingDir, filepath.Base(targetPath), raw)
	if stagedPath == "" {
		stagedPath = targetPath
	}

	att := llm.Attachment{
		Type:      attType,
		MimeType:  mime,
		Name:      filepath.Base(targetPath),
		Path:      stagedPath,
		DataURI:   dataURI,
		Extracted: extracted,
	}
	m.pendingAttachments = append(m.pendingAttachments, att)
	m.statusNotice = fmt.Sprintf("✓ Attached %s", att.Name)

	if len(parts) > 2 {
		remainingPrompt := strings.Join(parts[2:], " ")
		return m.handleAgentSubmit(remainingPrompt)
	}

	m.appendHistory(styleStatusNotice.Render(fmt.Sprintf("✓ Attached %s. Type your prompt and press Enter.\n\n", att.Name)))
	return m.clearStatusAfter(3 * time.Second)
}

func (m *teaModel) handleAddFileCommand(parts []string) tea.Cmd {
	if len(parts) < 2 {
		m.appendHistory(styleMuted.Render("Usage: /add <source-path> [destination-folder]\n\n"))
		return nil
	}

	src := strings.Trim(parts[1], "\"'")
	if strings.HasPrefix(src, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			src = filepath.Join(home, src[2:])
		}
	} else if !filepath.IsAbs(src) {
		src = filepath.Join(m.workingDir, src)
	}

	srcInfo, err := os.Stat(src)
	if err != nil {
		m.statusNotice = "Add error: " + err.Error()
		m.appendHistory(styleDiffDel.Render(fmt.Sprintf("Source file not found: %v\n\n", err)))
		return m.clearStatusAfter(3 * time.Second)
	}

	destDir := m.workingDir
	if len(parts) > 2 {
		destDir = filepath.Join(m.workingDir, strings.Trim(parts[2], "\"'"))
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		m.statusNotice = "Add error: " + err.Error()
		m.appendHistory(styleDiffDel.Render(fmt.Sprintf("Failed to create destination folder: %v\n\n", err)))
		return m.clearStatusAfter(3 * time.Second)
	}

	targetFile := filepath.Join(destDir, srcInfo.Name())
	if srcInfo.IsDir() {
		err = copyDir(src, targetFile)
	} else {
		err = copyFile(src, targetFile)
	}

	if err != nil {
		m.statusNotice = "Copy error: " + err.Error()
		m.appendHistory(styleDiffDel.Render(fmt.Sprintf("Failed to copy into workspace: %v\n\n", err)))
		return m.clearStatusAfter(3 * time.Second)
	}

	relDest, _ := filepath.Rel(m.workingDir, targetFile)
	m.statusNotice = fmt.Sprintf("✓ Added %s to %s", srcInfo.Name(), relDest)
	m.appendHistory(styleStatusNotice.Render(fmt.Sprintf("✓ Added %s to workspace folder: %s\n\n", srcInfo.Name(), relDest)))
	return tea.Batch(m.clearStatusAfter(3*time.Second), m.refreshGitStatusCmd())
}

func (m *teaModel) detectInlineAttachments(inputVal string) (string, []llm.Attachment) {
	words := strings.Fields(inputVal)
	var keptWords []string
	var found []llm.Attachment

	for _, word := range words {
		candidate := strings.Trim(word, "\"'")
		if strings.HasPrefix(candidate, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				candidate = filepath.Join(home, candidate[2:])
			}
		} else if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(m.workingDir, candidate)
		}

		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			if mime, dataURI, raw, err := media.FileToDataURI(candidate); err == nil {
				attType := "image"
				var extracted string
				if media.IsPDF(mime) {
					attType = "pdf"
					extracted, _ = media.ExtractPDFText(raw)
				}
				stagedPath, _ := media.StageAttachment(m.workingDir, filepath.Base(candidate), raw)
				if stagedPath == "" {
					stagedPath = candidate
				}
				found = append(found, llm.Attachment{
					Type:      attType,
					MimeType:  mime,
					Name:      filepath.Base(candidate),
					Path:      stagedPath,
					DataURI:   dataURI,
					Extracted: extracted,
				})
				continue
			}
		}
		keptWords = append(keptWords, word)
	}

	if len(found) > 0 {
		cleaned := strings.Join(keptWords, " ")
		if strings.TrimSpace(cleaned) == "" {
			cleaned = "Analyze the attached file."
		}
		return cleaned, found
	}
	return inputVal, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}
