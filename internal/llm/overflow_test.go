package llm

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func errorResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader(body))}
}

func TestContextOverflowIsRecognizedAcrossProviders(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		window int
	}{
		// Verbatim from the Ollama server that rejected a fastllm request.
		{"ollama", readChatError(errorResponse(400, `{"error":{"code":400,"message":"request (80956 tokens) exceeds the available context size (16384 tokens), try increasing it","type":"exceed_context_size_error","n_prompt_tokens":80956,"n_ctx":16384}}`)), 16384},
		{"openai", readChatError(errorResponse(400, `{"error":{"message":"This model's maximum context length is 128000 tokens. However, your messages resulted in 130211 tokens.","type":"invalid_request_error","code":"context_length_exceeded"}}`)), 128000},
		{"vllm plain body", readChatError(errorResponse(400, `This model's maximum context length is 262144 tokens. However, you requested 270000 tokens.`)), 262144},
		{"code only", readChatError(errorResponse(400, `{"error":{"message":"input too long","code":"context_length_exceeded"}}`)), 0},
		{"anthropic", anthropicError(errorResponse(400, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 1050000 tokens > 1000000 maximum"}}`)), 1000000},
		{"gemini", geminiError(errorResponse(400, `{"error":{"code":400,"message":"The input token count (1100000) exceeds the maximum number of tokens allowed (1048576).","status":"INVALID_ARGUMENT"}}`)), 1048576},
	} {
		t.Run(tc.name, func(t *testing.T) {
			overflow, ok := AsContextOverflow(tc.err)
			if !ok {
				t.Fatalf("not recognized as an overflow: %v", tc.err)
			}
			if overflow.Window != tc.window {
				t.Fatalf("window = %d, want %d", overflow.Window, tc.window)
			}
			if !strings.Contains(tc.err.Error(), "failed") && !strings.Contains(tc.err.Error(), ":") {
				t.Fatalf("the provider's message was lost: %v", tc.err)
			}
		})
	}
}

func TestOtherErrorsAreNotOverflows(t *testing.T) {
	for _, err := range []error{
		readChatError(errorResponse(401, `{"error":{"message":"invalid api key"}}`)),
		readChatError(errorResponse(429, `{"error":{"message":"rate limit reached"}}`)),
		anthropicError(errorResponse(529, `{"error":{"message":"overloaded"}}`)),
		errors.New("connection refused"),
	} {
		if _, ok := AsContextOverflow(err); ok {
			t.Errorf("%v was classified as a context overflow", err)
		}
	}
}
