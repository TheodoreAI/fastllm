package lsp

import (
	"bufio"
	"io"
	"os/exec"
	"strings"
	"testing"
)

// TestFramingRoundTrip confirms writeFramedMessage/readContentLength
// agree with each other over a real OS pipe: it spawns `cat` as a
// stand-in for gopls (no LSP semantics involved, just something that
// echoes stdin to stdout unmodified) and checks the bytes that come back
// out are exactly what went in, with the Content-Length header correctly
// declaring the body length.
func TestFramingRoundTrip(t *testing.T) {
	cmd := exec.Command("cat")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("cat not available: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	payload := []byte(`{"jsonrpc":"2.0","id":1,"method":"test","params":{"a":1}}`)
	if err := writeFramedMessage(stdin, payload); err != nil {
		t.Fatalf("writeFramedMessage: %v", err)
	}

	r := bufio.NewReader(stdout)
	length, err := readContentLength(r)
	if err != nil {
		t.Fatalf("readContentLength: %v", err)
	}
	if length != len(payload) {
		t.Fatalf("declared length %d, want %d", length, len(payload))
	}

	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != string(payload) {
		t.Fatalf("body = %q, want %q", body, payload)
	}
}

// TestReadContentLengthMissingHeader confirms a header block with no
// Content-Length line is rejected rather than silently read as
// zero-length — a malformed or unexpected message from gopls should
// surface as an error, not a spuriously "empty" one.
func TestReadContentLengthMissingHeader(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("Content-Type: application/vscode-jsonrpc\r\n\r\n"))
	if _, err := readContentLength(r); err == nil {
		t.Fatal("expected an error for a header block with no Content-Length")
	}
}
