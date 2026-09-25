package llm

import (
	"errors"
	"regexp"
	"strconv"
)

// ContextOverflowError is a provider refusing a request because it does not fit
// the model's context window. Window is the limit the provider stated, in
// tokens, or zero when it gave none. A caller that knows the real window can
// compact to it and retry instead of failing the run.
type ContextOverflowError struct {
	Window int
	Err    error
}

func (e *ContextOverflowError) Error() string { return e.Err.Error() }
func (e *ContextOverflowError) Unwrap() error { return e.Err }

// contextOverflowPatterns match the messages providers send for an oversized
// request. The first group is the window in tokens where the message states it.
var contextOverflowPatterns = []*regexp.Regexp{
	// llama.cpp and Ollama: "request (80956 tokens) exceeds the available
	// context size (16384 tokens), try increasing it"
	regexp.MustCompile(`exceeds the available context size \((\d+) tokens\)`),
	// OpenAI and vLLM: "This model's maximum context length is 128000 tokens."
	regexp.MustCompile(`maximum context length is (\d+) tokens`),
	// Anthropic: "prompt is too long: 250000 tokens > 200000 maximum"
	regexp.MustCompile(`prompt is too long: \d+ tokens > (\d+) maximum`),
	// Gemini: "The input token count (1100000) exceeds the maximum number of
	// tokens allowed (1048576)."
	regexp.MustCompile(`exceeds the maximum number of tokens allowed \((\d+)\)`),
}

// contextOverflowCodes name an overflow without stating the window.
var contextOverflowCodes = regexp.MustCompile(`context_length_exceeded|exceed_context_size_error|context window exceeded`)

// classifyContextOverflow wraps err as a ContextOverflowError when message is a
// provider's context-overflow message, and returns err unchanged otherwise.
func classifyContextOverflow(err error, message string) error {
	if err == nil {
		return nil
	}
	for _, pattern := range contextOverflowPatterns {
		match := pattern.FindStringSubmatch(message)
		if match == nil {
			continue
		}
		window, _ := strconv.Atoi(match[1])
		return &ContextOverflowError{Window: window, Err: err}
	}
	if contextOverflowCodes.MatchString(message) {
		return &ContextOverflowError{Err: err}
	}
	return err
}

// AsContextOverflow reports whether err is a context overflow, and the window
// the provider stated.
func AsContextOverflow(err error) (*ContextOverflowError, bool) {
	var overflow *ContextOverflowError
	if errors.As(err, &overflow) {
		return overflow, true
	}
	return nil, false
}
