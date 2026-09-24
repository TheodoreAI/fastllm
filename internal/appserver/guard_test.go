package appserver

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testToken = "s3cret-token"

func guarded() http.Handler {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	return Guard(ok, GuardConfig{Token: testToken, AllowedHosts: []string{"devbox.lan"}})
}

func request(method, host, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://"+host+"/api/harness/run", strings.NewReader(body))
	req.Host = host
	if body == "" {
		req.ContentLength = 0
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	guarded().ServeHTTP(rec, req)
	return rec
}

func TestGuardRefusesEachAttack(t *testing.T) {
	auth := map[string]string{"Authorization": "Bearer " + testToken, "Content-Type": "application/json"}
	cases := []struct {
		name    string
		host    string
		body    string
		headers map[string]string
		want    int
	}{
		{"valid request", "127.0.0.1:8080", `{}`, auth, http.StatusOK},
		{"x-api-key works", "localhost:8080", `{}`, map[string]string{"x-api-key": testToken, "Content-Type": "application/json"}, http.StatusOK},
		{"ipv6 loopback", "[::1]:8080", `{}`, auth, http.StatusOK},
		{"extra allowed host", "devbox.lan:8080", `{}`, auth, http.StatusOK},
		{"no token", "127.0.0.1:8080", `{}`, map[string]string{"Content-Type": "application/json"}, http.StatusUnauthorized},
		{"wrong token", "127.0.0.1:8080", `{}`, map[string]string{"Authorization": "Bearer nope", "Content-Type": "application/json"}, http.StatusUnauthorized},
		{"cross-site text/plain POST", "127.0.0.1:8080", `{"permission_mode":"full"}`,
			map[string]string{"Authorization": "Bearer " + testToken, "Content-Type": "text/plain"}, http.StatusUnsupportedMediaType},
		{"browser origin", "127.0.0.1:8080", `{}`,
			map[string]string{"Authorization": "Bearer " + testToken, "Content-Type": "application/json", "Origin": "https://evil.example"}, http.StatusForbidden},
		{"dns rebinding host", "evil.example:8080", `{}`, auth, http.StatusForbidden},
	}
	for _, c := range cases {
		if got := request(http.MethodPost, c.host, c.body, c.headers).Code; got != c.want {
			t.Errorf("%s: status %d, want %d", c.name, got, c.want)
		}
	}
}

func TestGuardEmptyTokenFailsClosed(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := Guard(ok, GuardConfig{})
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/models", nil)
	req.Header.Set("Authorization", "Bearer ")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a server with no token must refuse everything, got %d", rec.Code)
	}
}

func TestGuardLeavesRootNoticeOpen(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	rec := httptest.NewRecorder()
	guarded().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / should answer without a token, got %d", rec.Code)
	}
}

func TestLoadOrCreateToken(t *testing.T) {
	t.Setenv("FASTLLM_TOKEN", "")
	path := filepath.Join(t.TempDir(), "sub", "server-token")
	first, err := LoadOrCreateToken(path)
	if err != nil || len(first) != 64 {
		t.Fatalf("token %q, err %v", first, err)
	}
	second, err := LoadOrCreateToken(path)
	if err != nil || second != first {
		t.Fatal("the token must persist across restarts")
	}
	if data, _ := os.ReadFile(path); strings.TrimSpace(string(data)) != first {
		t.Fatal("token file does not hold the token")
	}
	t.Setenv("FASTLLM_TOKEN", "from-env")
	if got, _ := LoadOrCreateToken(path); got != "from-env" {
		t.Fatalf("FASTLLM_TOKEN should win, got %q", got)
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:8080": true, "localhost:8080": true, "[::1]:8080": true,
		":8080": false, "0.0.0.0:8080": false, "192.168.1.5:8080": false,
	} {
		if got := IsLoopbackAddr(addr); got != want {
			t.Errorf("IsLoopbackAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestBuiltServesThroughTheGuard(t *testing.T) {
	cfg := Config{DBPath: filepath.Join(t.TempDir(), "t.db"), LLMBaseURL: "http://127.0.0.1:1/v1", Token: testToken}
	built, err := Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.DB.Close()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/models", nil)
	rec := httptest.NewRecorder()
	built.HTTP.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("Built.HTTP must be guarded, got %d", rec.Code)
	}
}
