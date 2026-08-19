package chat

import (
	"strings"
	"testing"

	"fastllm/internal/store"
)

func TestCheckTruncation(t *testing.T) {
	bigFile := strings.Repeat("line\n", 100) // 100 lines, well above truncationMinLines

	tests := []struct {
		name    string
		current string
		next    string
		wantErr bool
	}{
		{"identical content passes", bigFile, bigFile, false},
		{"small trim passes", bigFile, strings.Repeat("line\n", 90), false},
		{"drastic shrink rejected", bigFile, strings.Repeat("line\n", 10), true},
		{"exactly at ratio boundary passes", bigFile, strings.Repeat("line\n", 50), false},
		{"just under ratio boundary rejected", bigFile, strings.Repeat("line\n", 49), true},
		{"small file exempt even when gutted", strings.Repeat("line\n", 5), "line\n", false},
		{"growing content always passes", bigFile, strings.Repeat("line\n", 200), false},
		{"emptying a small file is exempt", strings.Repeat("line\n", 3), "", false},
		{"emptying a large file is rejected", bigFile, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkTruncation(tt.current, tt.next)
			if tt.wantErr && got == "" {
				t.Errorf("checkTruncation(%d lines -> %d lines) = \"\", want a rejection message", countLines(tt.current), countLines(tt.next))
			}
			if !tt.wantErr && got != "" {
				t.Errorf("checkTruncation(%d lines -> %d lines) = %q, want no rejection", countLines(tt.current), countLines(tt.next), got)
			}
		})
	}
}

func TestCheckWriteFreshness(t *testing.T) {
	const path = "src/main.go"
	const content = "package main\n"

	t.Run("new file is always exempt", func(t *testing.T) {
		if got := checkWriteFreshness(map[string]string{}, path, "", false); got != "" {
			t.Errorf("got %q, want no rejection for a non-existent file", got)
		}
	})

	t.Run("no prior read is rejected", func(t *testing.T) {
		got := checkWriteFreshness(map[string]string{}, path, content, true)
		if got == "" {
			t.Fatal("got no rejection, want one — path was never read this turn")
		}
		if !strings.Contains(got, "read_file") {
			t.Errorf("rejection message %q doesn't mention read_file", got)
		}
	})

	t.Run("read followed by write on unchanged content passes", func(t *testing.T) {
		lastRead := map[string]string{path: hashContent(content)}
		if got := checkWriteFreshness(lastRead, path, content, true); got != "" {
			t.Errorf("got %q, want no rejection — content matches what was read", got)
		}
	})

	t.Run("content changed since read is rejected", func(t *testing.T) {
		lastRead := map[string]string{path: hashContent(content)}
		changed := content + "\nfunc main() {}\n"
		got := checkWriteFreshness(lastRead, path, changed, true)
		if got == "" {
			t.Fatal("got no rejection, want one — on-disk content no longer matches what was read")
		}
		if !strings.Contains(got, "changed since") {
			t.Errorf("rejection message %q doesn't explain the file changed", got)
		}
	})

	t.Run("read of a different path doesn't satisfy this path's check", func(t *testing.T) {
		lastRead := map[string]string{"other/file.go": hashContent(content)}
		got := checkWriteFreshness(lastRead, path, content, true)
		if got == "" {
			t.Fatal("got no rejection, want one — the read was for a different path")
		}
	})
}

func TestCountLines(t *testing.T) {
	tests := []struct {
		s    string
		want int
	}{
		{"", 0},
		{"one line, no trailing newline", 1},
		{"line1\n", 1},
		{"line1\nline2\n", 2},
		{"line1\nline2\nline3", 3},
	}
	for _, tt := range tests {
		if got := countLines(tt.s); got != tt.want {
			t.Errorf("countLines(%q) = %d, want %d", tt.s, got, tt.want)
		}
	}
}

func TestHashContentStableAndDistinct(t *testing.T) {
	a := hashContent("hello")
	b := hashContent("hello")
	c := hashContent("world")
	if a != b {
		t.Errorf("hashContent not stable: %q != %q for identical input", a, b)
	}
	if a == c {
		t.Errorf("hashContent collided for different input: %q == %q", a, c)
	}
}

// Regression test for the gap where approving or rejecting a proposed
// write left the model with no way to learn the outcome: everything about
// a write_file/edit_file call — including the tool result telling the
// model "proposed and awaiting approval, do not tell the user it's been
// written yet" — lives only in the local *messages slice for that one
// Chat call, and is never persisted. Without saveWriteOutcomeMessage, the
// next turn's buildPrompt/store.LoadMessages replay would still only ever
// show the model its own "proposed, awaiting approval" reply, with no
// record of what a human later did about it — leaving the model to guess,
// hedge, or need to be told again by hand.
func TestSaveWriteOutcomeMessagePersistsIntoConversationHistory(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()

	convID, err := store.CreateConversation(db, defaultWorkspace, "test conversation")
	if err != nil {
		t.Fatalf("store.CreateConversation: %v", err)
	}

	t.Run("approved write is recorded", func(t *testing.T) {
		pw := &PendingWrite{ID: "w1", Path: "src/main.go", conversationID: convID}
		saveWriteOutcomeMessage(db, pw, `The human approved and wrote the proposed change to "src/main.go" (id: w1). It is now saved on disk exactly as proposed.`)

		history, err := store.LoadMessages(db, defaultWorkspace, convID)
		if err != nil {
			t.Fatalf("store.LoadMessages: %v", err)
		}
		if len(history) != 1 {
			t.Fatalf("got %d messages, want 1", len(history))
		}
		if history[0].Role != "system" {
			t.Errorf("role = %q, want %q", history[0].Role, "system")
		}
		if !strings.Contains(history[0].Content, "approved") || !strings.Contains(history[0].Content, "src/main.go") {
			t.Errorf("content %q doesn't describe the approval outcome", history[0].Content)
		}
	})

	t.Run("rejected write is recorded distinctly from an approval", func(t *testing.T) {
		pw := &PendingWrite{ID: "w2", Path: "src/other.go", conversationID: convID}
		saveWriteOutcomeMessage(db, pw, `The human rejected the proposed change to "src/other.go" (id: w2). The file was NOT changed — it still has its original content.`)

		history, err := store.LoadMessages(db, defaultWorkspace, convID)
		if err != nil {
			t.Fatalf("store.LoadMessages: %v", err)
		}
		last := history[len(history)-1]
		if !strings.Contains(last.Content, "rejected") || !strings.Contains(last.Content, "NOT changed") {
			t.Errorf("content %q doesn't describe the rejection outcome", last.Content)
		}
	})

	t.Run("a write with no conversation ID is a no-op, not a panic", func(t *testing.T) {
		pw := &PendingWrite{ID: "w3", Path: "orphan.go"}
		saveWriteOutcomeMessage(db, pw, "should never be persisted")

		history, err := store.LoadMessages(db, defaultWorkspace, convID)
		if err != nil {
			t.Fatalf("store.LoadMessages: %v", err)
		}
		for _, m := range history {
			if strings.Contains(m.Content, "orphan.go") {
				t.Errorf("a write with no conversationID should not have written into conversation %d", convID)
			}
		}
	})
}