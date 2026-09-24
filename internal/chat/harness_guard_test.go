package chat

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fastllm/internal/harness"
)

func harnessRun(h *Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/harness/run", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.HarnessRun(rec, req)
	return rec
}

func TestHarnessRunCapsTheRequestedMode(t *testing.T) {
	root := t.TempDir()
	h := &Handler{MaxMode: harness.PermissionEdit, AllowedRoots: []string{root}}
	for _, mode := range []string{"full", "auto"} {
		rec := harnessRun(h, `{"task":"x","permission_mode":"`+mode+`","allow_commands":true,"working_dir":"`+filepath.ToSlash(root)+`"}`)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "FASTLLM_MAX_MODE") {
			t.Fatalf("%s above an edit cap: %d %s", mode, rec.Code, rec.Body.String())
		}
	}
	// With no cap configured the server fails closed to plan.
	rec := harnessRun(&Handler{}, `{"task":"x","permission_mode":"edit"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("an unset cap must mean plan, got %d", rec.Code)
	}
}

func TestHarnessRunConfinesWorkingDir(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	h := &Handler{MaxMode: harness.PermissionFull, AllowedRoots: []string{root}}
	rec := harnessRun(h, `{"task":"x","working_dir":"`+filepath.ToSlash(outside)+`"}`)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "FASTLLM_ALLOWED_ROOTS") {
		t.Fatalf("working_dir outside the roots: %d %s", rec.Code, rec.Body.String())
	}

	sub := filepath.Join(root, "project")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if !withinAllowedRoots(sub, []string{root}) || !withinAllowedRoots(root, []string{root}) {
		t.Fatal("the root and its subdirectories must be allowed")
	}
	if withinAllowedRoots(filepath.Join(root, "..", filepath.Base(outside)), []string{root}) {
		t.Fatal("a .. escape must be refused")
	}
	if withinAllowedRoots(root+"-sibling", []string{root}) {
		t.Fatal("a sibling sharing the prefix must be refused")
	}
}
