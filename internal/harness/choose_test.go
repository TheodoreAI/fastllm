package harness

import (
	"os"
	"strings"
	"testing"
)

func TestChooseFallsBackWhenNotATerminal(t *testing.T) {
	// go test runs with stdin/stdout as pipes, which is exactly the redirected
	// case: Choose must decline so the caller can use its typed prompt.
	if chooseSupported() {
		t.Skip("stdin and stdout are terminals in this environment")
	}
	value, ok := Choose("Allow?", []Choice{{Key: 'y', Label: "Yes", Value: "y"}}, 0)
	if ok || value != "" {
		t.Fatalf("expected a decline outside a terminal, got (%q, %v)", value, ok)
	}
}

func TestChooseRespectsOptOut(t *testing.T) {
	t.Setenv("FASTLLM_NO_MENU", "1")
	if chooseSupported() {
		t.Error("FASTLLM_NO_MENU must disable the inline menu")
	}
}

func TestChooseDeclinesWithNoChoices(t *testing.T) {
	if _, ok := Choose("Allow?", nil, 0); ok {
		t.Error("an empty choice list must not be selectable")
	}
}

// The deny-first ordering is a safety property: an accidental Enter must never
// grant a mutation, so it is pinned rather than left to whoever edits the list.
func TestPermissionChoicesDefaultToDeny(t *testing.T) {
	source, err := os.ReadFile("permissions.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(source)
	first := strings.Index(body, `{Key: 'n'`)
	yes := strings.Index(body, `{Key: 'y'`)
	all := strings.Index(body, `{Key: 'a'`)
	if first < 0 || yes < 0 || all < 0 {
		t.Fatal("expected y/n/a choices in the permission prompt")
	}
	if !(first < yes && yes < all) {
		t.Error("deny must be the first (highlighted) option so Enter cannot grant by accident")
	}
	if !strings.Contains(body, "Choose(") {
		t.Error("the permission prompt should offer the inline menu")
	}
	if !strings.Contains(body, "p.Input.ReadLine") {
		t.Error("the typed fallback must remain for non-terminal sessions")
	}
}
