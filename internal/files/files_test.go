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
	r := New(root, false)

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
	r := New(root, false)

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
	r := New(root, false)

	outside := t.TempDir()
	abs := filepath.Join(outside, "x.txt")
	os.WriteFile(abs, []byte("x"), 0o644)

	if _, _, err := r.Read(abs); err == nil {
		t.Error("expected absolute path outside root to be rejected")
	}
}

func TestRejectsSymlinkEscape(t *testing.T) {
	root := setupRoot(t)
	r := New(root, false)

	if _, _, err := r.Read("escape-link.txt"); err == nil {
		t.Error("expected symlink escaping root to be rejected")
	}
}

func TestRejectsDirectory(t *testing.T) {
	root := setupRoot(t)
	r := New(root, false)

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
	r := New("", true)
	if r.Enabled() {
		t.Error("New(\"\") should produce a disabled Reader, not default to cwd")
	}
	if r.WritesEnabled() {
		t.Error("New(\"\") should never enable writes even if allowWrites=true")
	}
}

func TestWritesDisabledByDefault(t *testing.T) {
	root := setupRoot(t)
	r := New(root, false)
	if r.WritesEnabled() {
		t.Error("WritesEnabled() should be false when allowWrites=false")
	}
	if err := r.Write("new.txt", "content"); err == nil {
		t.Error("expected error writing when writes are disabled")
	}
}

func TestWriteNewFile(t *testing.T) {
	root := setupRoot(t)
	r := New(root, true)

	if err := r.Write("new.txt", "hello new file"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	content, _, err := r.Read("new.txt")
	if err != nil {
		t.Fatalf("unexpected error reading back: %v", err)
	}
	if content != "hello new file" {
		t.Fatalf("got %q", content)
	}
}

func TestWriteOverwritesExisting(t *testing.T) {
	root := setupRoot(t)
	r := New(root, true)

	if err := r.Write("hello.txt", "overwritten"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	content, _, err := r.Read("hello.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "overwritten" {
		t.Fatalf("got %q", content)
	}
}

func TestWriteCreatesParentDirs(t *testing.T) {
	root := setupRoot(t)
	r := New(root, true)

	if err := r.Write("new/nested/dir/file.txt", "deep"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	content, _, err := r.Read("new/nested/dir/file.txt")
	if err != nil {
		t.Fatalf("unexpected error reading back: %v", err)
	}
	if content != "deep" {
		t.Fatalf("got %q", content)
	}
}

func TestWriteRejectsTraversal(t *testing.T) {
	root := setupRoot(t)
	r := New(root, true)

	cases := []string{
		"../escape.txt",
		"../../etc/passwd",
		"sub/../../escape.txt",
	}
	for _, c := range cases {
		if err := r.Write(c, "malicious"); err == nil {
			t.Errorf("expected error writing to %q, got nil", c)
		}
	}
}

func TestWriteRejectsAbsolutePathEscape(t *testing.T) {
	root := setupRoot(t)
	r := New(root, true)

	outside := t.TempDir()
	abs := filepath.Join(outside, "escape.txt")
	if err := r.Write(abs, "malicious"); err == nil {
		t.Error("expected absolute path outside root to be rejected")
	}
	if _, err := os.Stat(abs); err == nil {
		t.Error("file should not have been created outside the sandbox")
	}
}

func TestWriteRejectsSymlinkEscape(t *testing.T) {
	root := setupRoot(t)
	r := New(root, true)

	// escape-link.txt is a symlink (created in setupRoot) pointing outside root.
	if err := r.Write("escape-link.txt", "malicious"); err == nil {
		t.Error("expected write through a symlink escaping root to be rejected")
	}
}

func TestReaderCanUpdateRootAndWriteState(t *testing.T) {
	root := setupRoot(t)
	r := New(root, false)

	if err := r.SetConfig(root, true, true); err != nil {
		t.Fatalf("SetConfig returned unexpected error: %v", err)
	}
	if !r.Enabled() {
		t.Fatal("reader should remain enabled after a live config update")
	}
	if !r.WritesEnabled() {
		t.Fatal("writes should be enabled after SetConfig")
	}

	if err := r.SetConfig("", false, false); err != nil {
		t.Fatalf("SetConfig with empty root returned unexpected error: %v", err)
	}
	if r.Enabled() {
		t.Fatal("empty root should disable the reader")
	}
	if r.WritesEnabled() {
		t.Fatal("empty root should disable writes")
	}
}

func TestSnapshotIsConsistent(t *testing.T) {
	root := setupRoot(t)
	r := New(root, true)

	snapRoot, allowWrites := r.Snapshot()
	if snapRoot == "" {
		t.Fatal("expected non-empty root from Snapshot")
	}
	if !allowWrites {
		t.Fatal("expected allowWrites=true from Snapshot")
	}

	_ = r.SetConfig("", false, false)
	snapRoot, allowWrites = r.Snapshot()
	if snapRoot != "" || allowWrites {
		t.Fatal("Snapshot should reflect the disabled state after SetConfig")
	}
}

func TestExistingContentForNewFile(t *testing.T) {
	root := setupRoot(t)
	r := New(root, true)

	content, exists, err := r.ExistingContent("does-not-exist-yet.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Error("expected exists=false for a not-yet-existing file")
	}
	if content != "" {
		t.Errorf("expected empty content, got %q", content)
	}
}

func TestExistingContentForExistingFile(t *testing.T) {
	root := setupRoot(t)
	r := New(root, true)

	content, exists, err := r.ExistingContent("hello.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Error("expected exists=true")
	}
	if content != "hello world" {
		t.Errorf("got %q", content)
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
	r := New(root, false)
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
