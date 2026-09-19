package webtools

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTMLToMarkdown(t *testing.T) {
	rawHTML := `
<!DOCTYPE html>
<html>
<head><title>Test Page</title><style>body { color: red; }</style></head>
<body>
  <nav><a href="/home">Home</a></nav>
  <h1>Main Title</h1>
  <p>This is a paragraph with <code>code snippet</code> and a <a href="https://example.com">link</a>.</p>
  <h2>Sub Title</h2>
  <ul>
    <li>Item 1</li>
    <li>Item 2</li>
  </ul>
  <script>console.log("ignore me");</script>
  <footer>Copyright 2026</footer>
</body>
</html>
`
	md := HTMLToMarkdown(rawHTML, "https://example.com")

	if strings.Contains(md, "console.log") {
		t.Errorf("expected script content to be stripped, got: %s", md)
	}
	if strings.Contains(md, "color: red") {
		t.Errorf("expected style content to be stripped, got: %s", md)
	}
	if strings.Contains(md, "Copyright 2026") {
		t.Errorf("expected footer content to be stripped, got: %s", md)
	}
	if !strings.Contains(md, "# Main Title") {
		t.Errorf("expected h1 header '# Main Title', got: %s", md)
	}
	if !strings.Contains(md, "## Sub Title") {
		t.Errorf("expected h2 header '## Sub Title', got: %s", md)
	}
	if !strings.Contains(md, "`code snippet`") {
		t.Errorf("expected inline code '`code snippet`', got: %s", md)
	}
	if !strings.Contains(md, "* Item 1") {
		t.Errorf("expected list item '* Item 1', got: %s", md)
	}
}

func TestFetchMockServer(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`
<html>
<body>
  <h1>FastLLM Server</h1>
  <p>FastLLM is a lightweight local LLM runner and harness.</p>
</body>
</html>
`))
	}))
	defer ts.Close()

	ctx := context.Background()
	_, err := Fetch(ctx, ts.URL, 4000)
	if err == nil || !strings.Contains(err.Error(), "does not resolve to a public IP address") {
		t.Fatalf("expected private-address rejection, got: %v", err)
	}
}

func TestIsPublicIP(t *testing.T) {
	cases := []struct {
		address string
		want    bool
	}{
		{"8.8.8.8", true},
		{"2001:4860:4860::8888", true},
		{"127.0.0.1", false},
		{"10.0.0.1", false},
		{"100.64.0.1", false},
		{"169.254.169.254", false},
		{"192.0.2.1", false},
		{"::1", false},
		{"fc00::1", false},
	}
	for _, tc := range cases {
		if got := isPublicIP(net.ParseIP(tc.address)); got != tc.want {
			t.Errorf("isPublicIP(%q) = %v; want %v", tc.address, got, tc.want)
		}
	}
}

func TestCleanDDGURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{
			in:   "https://go.dev/",
			want: "https://go.dev/",
		},
		{
			in:   "//html.duckduckgo.com/l/?uddg=https%3A%2F%2Fgolang.org%2Fpkg%2F&rut=...",
			want: "https://golang.org/pkg/",
		},
	}

	for _, tc := range cases {
		got := cleanDDGURL(tc.in)
		if got != tc.want {
			t.Errorf("cleanDDGURL(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}
