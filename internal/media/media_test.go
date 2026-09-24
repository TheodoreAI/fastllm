package media

import (
	"bytes"
	"compress/zlib"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectMime(t *testing.T) {
	tests := []struct {
		data     []byte
		filename string
		wantMime string
		wantSup  bool
	}{
		{[]byte("%PDF-1.7\n..."), "doc.pdf", MimePDF, true},
		{[]byte("\x89PNG\r\n\x1a\n\x00..."), "pic.png", MimePNG, true},
		{[]byte("\xff\xd8\xff\xe0..."), "photo.jpg", MimeJPEG, true},
		{[]byte("GIF89a..."), "anim.gif", MimeGIF, true},
		{[]byte("RIFF1234WEBP..."), "pic.webp", MimeWEBP, true},
		{[]byte("hello world"), "notes.txt", "", false},
		{[]byte(""), "test.pdf", MimePDF, true},
	}

	for _, tt := range tests {
		gotMime, gotSup := DetectMime(tt.data, tt.filename)
		if gotMime != tt.wantMime || gotSup != tt.wantSup {
			t.Errorf("DetectMime(%q, %q) = (%q, %v); want (%q, %v)", tt.data, tt.filename, gotMime, gotSup, tt.wantMime, tt.wantSup)
		}
	}
}

func TestDataURIConversions(t *testing.T) {
	raw := []byte("fake image data")
	uri := BuildDataURI(MimePNG, raw)
	if !strings.HasPrefix(uri, "data:image/png;base64,") {
		t.Fatalf("unexpected data uri: %s", uri)
	}

	mime, payload, ok := DataURIToMimeAndPayload(uri)
	if !ok || mime != MimePNG {
		t.Fatalf("failed parsing data uri: mime=%s, ok=%v", mime, ok)
	}
	if payload == "" {
		t.Fatalf("expected non-empty payload")
	}
}

func TestExtractPDFText(t *testing.T) {
	// Construct a synthetic minimal PDF with compressed stream
	content := "BT /F1 12 Tf (Hello PDF World) Tj ET"
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	_, _ = zw.Write([]byte(content))
	_ = zw.Close()

	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n")
	pdf.WriteString("1 0 obj\n<< /Length 100 /Filter /FlateDecode >>\nstream\n")
	pdf.Write(compressed.Bytes())
	pdf.WriteString("\nendstream\nendobj\n%%EOF")

	text, err := ExtractPDFText(pdf.Bytes())
	if err != nil {
		t.Fatalf("ExtractPDFText failed: %v", err)
	}
	if !strings.Contains(text, "Hello PDF World") {
		t.Fatalf("extracted text %q does not contain 'Hello PDF World'", text)
	}
}

func TestStageAttachment(t *testing.T) {
	tmp := t.TempDir()
	raw := []byte("\x89PNG\r\n\x1a\n...")
	dest, err := StageAttachment(tmp, "screenshot.png", raw)
	if err != nil {
		t.Fatalf("StageAttachment failed: %v", err)
	}
	if !strings.HasPrefix(dest, filepath.Join(tmp, ".fastllm", "attachments")) {
		t.Fatalf("unexpected staged path: %s", dest)
	}
	readBack, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(readBack, raw) {
		t.Fatalf("read back content mismatch")
	}
}
