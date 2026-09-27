package harness

import (
	"bytes"
	"compress/zlib"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fastllm/internal/files"
	"fastllm/internal/llm"

	"charm.land/bubbles/v2/textarea"
)

func createTestPDF(t *testing.T, dir, filename, textContent string) string {
	t.Helper()
	content := "BT /F1 12 Tf (" + textContent + ") Tj ET"
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	_, _ = zw.Write([]byte(content))
	_ = zw.Close()

	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n")
	pdf.WriteString("1 0 obj\n<< /Length 100 /Filter /FlateDecode >>\nstream\n")
	pdf.Write(compressed.Bytes())
	pdf.WriteString("\nendstream\nendobj\n%%EOF")

	filePath := filepath.Join(dir, filename)
	if err := os.WriteFile(filePath, pdf.Bytes(), 0o644); err != nil {
		t.Fatalf("failed creating test pdf: %v", err)
	}
	return filePath
}

func createTestPNG(t *testing.T, dir, filename string) string {
	t.Helper()
	pngData := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15c4")
	filePath := filepath.Join(dir, filename)
	if err := os.WriteFile(filePath, pngData, 0o644); err != nil {
		t.Fatalf("failed creating test png: %v", err)
	}
	return filePath
}

func TestTuiHandleAttachCommand(t *testing.T) {
	tmp := t.TempDir()
	pdfPath := createTestPDF(t, tmp, "sample.pdf", "Quarterly Financial Results")
	pngPath := createTestPNG(t, tmp, "chart.png")

	m := &teaModel{
		workingDir: tmp,
	}

	// 1. Attach PDF
	cmd := m.handleAttachCommand([]string{"/attach", pdfPath})
	if cmd != nil {
		// Nil is expected since no prompt was included
	}
	if len(m.pendingAttachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(m.pendingAttachments))
	}
	att := m.pendingAttachments[0]
	if att.Type != "pdf" || att.MimeType != "application/pdf" {
		t.Errorf("expected pdf attachment, got %+v", att)
	}
	if !strings.Contains(att.Extracted, "Quarterly Financial Results") {
		t.Errorf("expected extracted text to contain 'Quarterly Financial Results', got %q", att.Extracted)
	}

	// 2. Attach PNG
	m.handleAttachCommand([]string{"/attach", pngPath})
	if len(m.pendingAttachments) != 2 {
		t.Fatalf("expected 2 attachments, got %d", len(m.pendingAttachments))
	}
	imgAtt := m.pendingAttachments[1]
	if imgAtt.Type != "image" || imgAtt.MimeType != "image/png" {
		t.Errorf("expected image attachment, got %+v", imgAtt)
	}
}

func TestTuiHandleAddFileCommand(t *testing.T) {
	srcDir := t.TempDir()
	workspaceDir := t.TempDir()

	testFile := filepath.Join(srcDir, "data.csv")
	if err := os.WriteFile(testFile, []byte("id,val\n1,100"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := &teaModel{
		workingDir: workspaceDir,
	}

	// Add file to "data" subfolder
	m.handleAddFileCommand([]string{"/add", testFile, "data"})

	destFile := filepath.Join(workspaceDir, "data", "data.csv")
	content, err := os.ReadFile(destFile)
	if err != nil {
		t.Fatalf("expected file copied to %s: %v", destFile, err)
	}
	if string(content) != "id,val\n1,100" {
		t.Errorf("copied content mismatch: %q", string(content))
	}
}

func TestTuiDetectInlineAttachments(t *testing.T) {
	tmp := t.TempDir()
	pngPath := createTestPNG(t, tmp, "screenshot.png")

	m := &teaModel{
		workingDir: tmp,
	}

	input := "Please explain this diagram: " + pngPath + " and suggest improvements"
	cleaned, atts := m.detectInlineAttachments(input)

	if len(atts) != 1 {
		t.Fatalf("expected 1 detected attachment, got %d", len(atts))
	}
	if atts[0].Type != "image" {
		t.Errorf("expected image type, got %s", atts[0].Type)
	}
	if !strings.Contains(cleaned, "Please explain this diagram:") || strings.Contains(cleaned, pngPath) {
		t.Errorf("cleaned prompt should preserve text but remove path: %q", cleaned)
	}
}

func TestInteractiveSessionPreservesAttachments(t *testing.T) {
	tmp := t.TempDir()
	store := &SessionStore{Dir: tmp}

	session := store.New(tmp, "test-model", InteractiveRuntime{})
	session.Messages = []llm.Message{
		{
			Role:    "user",
			Content: "Analyze this image",
			Attachments: []llm.Attachment{
				{
					Type:     "image",
					MimeType: "image/png",
					Name:     "img.png",
					DataURI:  "data:image/png;base64,YWJj",
				},
			},
		},
		{
			Role:    "assistant",
			Content: "This is a test image.",
		},
	}

	if err := store.Save(session); err != nil {
		t.Fatalf("save session: %v", err)
	}

	loaded, err := store.Load(session.ID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}

	if len(loaded.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(loaded.Messages))
	}
	if len(loaded.Messages[0].Attachments) != 1 {
		t.Fatalf("expected 1 attachment in loaded message, got %d", len(loaded.Messages[0].Attachments))
	}
	if loaded.Messages[0].Attachments[0].Name != "img.png" {
		t.Errorf("attachment name mismatch: got %q, want img.png", loaded.Messages[0].Attachments[0].Name)
	}
}

func TestRunnerExecuteReadFileWithPDFAndImage(t *testing.T) {
	tmp := t.TempDir()
	createTestPDF(t, tmp, "doc.pdf", "Invoice Summary Total")
	createTestPNG(t, tmp, "picture.png")

	r := &Runner{}
	fr := files.New(tmp, false)

	pdfOut := r.executeReadFile(fr, "doc.pdf")
	if !strings.Contains(pdfOut, "Invoice Summary Total") {
		t.Errorf("expected extracted text, got %q", pdfOut)
	}

	pngOut := r.executeReadFile(fr, "picture.png")
	if !strings.Contains(pngOut, "[Image file: picture.png") {
		t.Errorf("expected image notice, got %q", pngOut)
	}
}

func TestTuiFilesModalAndDetach(t *testing.T) {
	tmp := t.TempDir()
	createTestPDF(t, tmp, "doc.pdf", "Summary of quarterly findings")
	_ = os.Mkdir(filepath.Join(tmp, "src"), 0o755)

	m := &teaModel{
		workingDir: tmp,
		input:      textarea.New(),
	}

	// 1. Open files modal
	m.openFilesModal(tmp)
	if m.filesModal == nil {
		t.Fatal("expected filesModal to be open")
	}
	if len(m.filesModal.entries) < 2 {
		t.Fatalf("expected entries in files modal, got %d", len(m.filesModal.entries))
	}

	// 2. Render files modal
	rendered := m.renderFilesModal()
	if !strings.Contains(rendered, "Workspace Files") || !strings.Contains(rendered, "doc.pdf") {
		t.Fatalf("renderFilesModal missing expected content: %s", rendered)
	}

	// 3. Attach file via modal
	m.attachFileAndCloseModal(filepath.Join(tmp, "doc.pdf"))
	if len(m.pendingAttachments) != 1 {
		t.Fatalf("expected 1 pending attachment, got %d", len(m.pendingAttachments))
	}

	// 4. Detach command clears pending attachments
	m.handleAgentSubmit("/detach")
	if len(m.pendingAttachments) != 0 {
		t.Fatalf("expected pending attachments cleared after /detach, got %d", len(m.pendingAttachments))
	}
}
