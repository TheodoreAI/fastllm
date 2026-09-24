package appserver

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// The HTTP API is the harness's control plane: a request chooses the
// permission mode, the working directory, and whether commands run. The
// reference monitor cannot protect against a caller who is allowed to be the
// user, so the guard decides who may be the user at all.
//
// Every request must:
//   - name a loopback host (or one listed in FASTLLM_ALLOWED_HOSTS), so a
//     DNS-rebound page cannot reach the server under an attacker's name;
//   - carry no Origin header, so no browser page can call it cross-site;
//   - present the server token as "Authorization: Bearer <t>" or "x-api-key";
//   - send JSON when it has a body, so a form or text/plain POST is refused.

// GuardConfig is what the guard enforces.
type GuardConfig struct {
	Token        string
	AllowedHosts []string // extra Host names beyond loopback
}

// Guard wraps next with the checks above. An empty token denies everything:
// a misconfigured server fails closed rather than open.
func Guard(next http.Handler, cfg GuardConfig) http.Handler {
	extraHosts := map[string]bool{}
	for _, h := range cfg.AllowedHosts {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			extraHosts[h] = true
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(r.Host, extraHosts) {
			http.Error(w, "forbidden: unrecognised Host", http.StatusForbidden)
			return
		}
		if r.Header.Get("Origin") != "" {
			http.Error(w, "forbidden: browser requests are not accepted", http.StatusForbidden)
			return
		}
		// The root only explains that the server is headless; it reveals
		// nothing and is left open so a stray visit gets an answer.
		if r.Method == http.MethodGet && r.URL.Path == "/" {
			next.ServeHTTP(w, r)
			return
		}
		if !tokenMatches(r, cfg.Token) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="fastllm"`)
			http.Error(w, "unauthorized: send the token from ~/.fastllm/server-token as a Bearer token", http.StatusUnauthorized)
			return
		}
		if hasBody(r) && !isJSON(r.Header.Get("Content-Type")) {
			http.Error(w, "unsupported media type: send application/json", http.StatusUnsupportedMediaType)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func hostAllowed(hostport string, extra map[string]bool) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	if host == "localhost" || extra[host] {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func tokenMatches(r *http.Request, token string) bool {
	if token == "" {
		return false
	}
	presented := r.Header.Get("x-api-key")
	if auth := r.Header.Get("Authorization"); presented == "" && len(auth) > 7 && strings.EqualFold(auth[:7], "Bearer ") {
		presented = strings.TrimSpace(auth[7:])
	}
	return presented != "" && subtle.ConstantTimeCompare([]byte(presented), []byte(token)) == 1
}

func hasBody(r *http.Request) bool {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		return r.ContentLength != 0
	}
	return false
}

func isJSON(contentType string) bool {
	mediaType, _, _ := strings.Cut(contentType, ";")
	return strings.EqualFold(strings.TrimSpace(mediaType), "application/json")
}

// LoadOrCreateToken returns FASTLLM_TOKEN when set, otherwise the token in
// path, creating it (0600, 32 random bytes) on first use.
func LoadOrCreateToken(path string) (string, error) {
	if token := strings.TrimSpace(os.Getenv("FASTLLM_TOKEN")); token != "" {
		return token, nil
	}
	if data, err := os.ReadFile(path); err == nil {
		if token := strings.TrimSpace(string(data)); token != "" {
			return token, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw[:])
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write server token: %w", err)
	}
	return token, nil
}

// DefaultTokenPath is ~/.fastllm/server-token.
func DefaultTokenPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".fastllm", "server-token"), nil
}

// IsLoopbackAddr reports whether a listen address only accepts local
// connections. An empty host (":8080") listens on every interface.
func IsLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
