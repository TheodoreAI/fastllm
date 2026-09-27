package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A reply the server stopped at max_tokens must be reported as truncated on
// both the plain and the streaming path; a normal stop must not be.
func TestChatReportsOutputLimitTruncation(t *testing.T) {
	for _, tc := range []struct {
		finish string
		want   bool
	}{{"length", true}, {"stop", false}} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body strings.Builder
			buf := make([]byte, 4096)
			n, _ := r.Body.Read(buf)
			body.Write(buf[:n])
			if strings.Contains(body.String(), `"stream":true`) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Write([]byte(`data: {"choices":[{"delta":{"content":"part"},"finish_reason":null}]}` + "\n\n"))
				w.Write([]byte(`data: {"choices":[{"delta":{},"finish_reason":"` + tc.finish + `"}]}` + "\n\n"))
				w.Write([]byte("data: [DONE]\n\n"))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"part"},"finish_reason":"` + tc.finish + `"}]}`))
		}))

		c := New(ts.URL, "", "m", "")
		msgs := []Message{{Role: "user", Content: "hi"}}
		plain, err := c.ChatWithUsage(context.Background(), "m", msgs, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		if plain.Truncated != tc.want {
			t.Errorf("finish_reason %q: plain Truncated = %v; want %v", tc.finish, plain.Truncated, tc.want)
		}
		streamed, _, err := c.StreamChatWithTools(context.Background(), "m", msgs, nil, "", func(string) {}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if streamed.Truncated != tc.want {
			t.Errorf("finish_reason %q: streamed Truncated = %v; want %v", tc.finish, streamed.Truncated, tc.want)
		}
		ts.Close()
	}
}
