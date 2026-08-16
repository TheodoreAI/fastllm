package files

import (
	"os"
	"path/filepath"
	"testing"
)

func setupRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "nested.txt"), []byte("nested"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "escape-link.txt")); err != nil {
		t.Skip("symlinks not supported on this platform/permission level")
	}
	return root
}

func TestReadWithinRoot(t *testing.T) {
	root := setupRoot(t)
	r := New(root)

	content, truncated, err := r.Read("hello.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if truncated {
		t.Fatal("should not be truncated")
	}
	if content != "hello world" {
		t.Fatalf("got %q", content)
	}

	content, _, err = r.Read("sub/nested.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "nested" {
		t.Fatalf("got %q", content)
	}
}

func TestRejectsTraversal(t *testing.T) {
	root := setupRoot(t)
	r := New(root)

	cases := []string{
		"../secret.txt",
		"../../etc/passwd",
		"sub/../../secret.txt",
	}
	for _, c := range cases {
		if _, _, err := r.Read(c); err == nil {
			t.Errorf("expected error for %q, got nil", c)
		}
	}
}

func TestRejectsAbsolutePathEscape(t *testing.T) {
	root := setupRoot(t)
	r := New(root)

	outside := t.TempDir()
	abs := filepath.Join(outside, "x.txt")
	os.WriteFile(abs, []byte("x"), 0o644)

	if _, _, err := r.Read(abs); err == nil {
		t.Error("expected absolute path outside root to be rejected")
	}
}

func TestRejectsSymlinkEscape(t *testing.T) {
	root := setupRoot(t)
	r := New(root)

	if _, _, err := r.Read("escape-link.txt"); err == nil {
		t.Error("expected symlink escaping root to be rejected")
	}
}

func TestRejectsDirectory(t *testing.T) {
	root := setupRoot(t)
	r := New(root)

	if _, _, err := r.Read("sub"); err == nil {
		t.Error("expected error reading a directory")
	}
}

func TestDisabledByDefault(t *testing.T) {
	r := &Reader{}
	if r.Enabled() {
		t.Error("zero-value Reader should be disabled")
	}
	if _, _, err := r.Read("anything.txt"); err == nil {
		t.Error("expected error when disabled")
	}
}

func TestNewWithEmptyRootIsDisabled(t *testing.T) {
	r := New("")
	if r.Enabled() {
		t.Error("New(\"\") should produce a disabled Reader, not default to cwd")
	}
}

func TestTruncatesLargeFiles(t *testing.T) {
	root := t.TempDir()
	big := make([]byte, MaxReadBytes+5000)
	for i := range big {
		big[i] = 'a'
	}
	if err := os.WriteFile(filepath.Join(root, "big.txt"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(root)
	content, truncated, err := r.Read("big.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !truncated {
		t.Fatal("expected truncated=true")
	}
	if len(content) != MaxReadBytes {
		t.Fatalf("got content length %d, want %d", len(content), MaxReadBytes)
	}
}
