package gitrepo

import (
	"context"
	"os/exec"
	"testing"
)

func TestHeadContentsDistinguishesMissingAndQueryFailure(t *testing.T) {
	root := newTestRepo(t)
	ctx := context.Background()
	for _, test := range []struct{ path, want string }{{"committed.txt", "hello\n"}, {"new file.txt", ""}} {
		got, err := HeadContents(ctx, root, test.path)
		if err != nil || got != test.want {
			t.Fatalf("HEAD %q = %q, %v", test.path, got, err)
		}
	}
	if _, err := HeadContents(ctx, t.TempDir(), "file"); err == nil {
		t.Fatal("nonrepository accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := HeadContents(cancelled, root, "committed.txt"); err == nil {
		t.Fatal("cancellation interpreted as new file")
	}
	empty := t.TempDir()
	cmd := exec.Command("git", "init", "-q", empty)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if got, err := HeadContents(ctx, empty, "new.txt"); err != nil || got != "" {
		t.Fatalf("unborn HEAD: %q %v", got, err)
	}
}
