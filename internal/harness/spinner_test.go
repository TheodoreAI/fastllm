package harness

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"fastllm/internal/llm"
)

func TestSpinnerIsInertWhenOutputIsNotATerminal(t *testing.T) {
	// go test captures stdout through a pipe, so this is the redirected case:
	// the spinner must write nothing at all, or -json output would be corrupted
	// by carriage returns and escape codes.
	if spinnerSupported() {
		t.Skip("stdout is a terminal in this environment")
	}
	s := NewSpinner("Thinking")
	s.Start()
	s.Stop()
	s.Stop() // double Stop must not panic or close a closed channel
}

func TestSpinnerRespectsOptOutEnvironment(t *testing.T) {
	t.Setenv("FASTLLM_NO_SPINNER", "1")
	if spinnerSupported() {
		t.Error("FASTLLM_NO_SPINNER must disable the spinner")
	}
	t.Setenv("FASTLLM_NO_SPINNER", "")
	t.Setenv("NO_COLOR", "1")
	if spinnerSupported() {
		t.Error("NO_COLOR must disable the spinner")
	}
}

func TestSpinnerStartStopIsRaceFreeAndIdempotent(t *testing.T) {
	s := NewSpinner("Working")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); s.Start() }()
		go func() { defer wg.Done(); s.Stop() }()
	}
	wg.Wait()
	s.Stop()
}

func TestNilSpinnerIsSafe(t *testing.T) {
	var s *Spinner
	s.Start()
	s.Stop()
}

// stubUsageClient reports server-side counts, like a real *llm.Client does.
type stubUsageClient struct {
	usage llm.Usage
	have  bool
}

func (s stubUsageClient) Chat(_ context.Context, _ string, _ []llm.Message, _ []llm.Tool, _ string) (llm.Message, error) {
	return llm.Message{}, nil
}
func (s stubUsageClient) LastUsage() (llm.Usage, bool) { return s.usage, s.have }

func TestResolveTurnTokensPrefersServerCounts(t *testing.T) {
	client := stubUsageClient{usage: llm.Usage{PromptTokens: 6270, CompletionTokens: 412}, have: true}
	prompt, completion, measured := resolveTurnTokens(client, 6000, 6)
	if !measured || prompt != 6270 || completion != 412 {
		t.Fatalf("expected the server's counts, got (%d, %d, %v)", prompt, completion, measured)
	}
}

func TestResolveTurnTokensFallsBackToEstimate(t *testing.T) {
	client := stubUsageClient{have: false}
	prompt, completion, measured := resolveTurnTokens(client, 6000, 6)
	if measured || prompt != 6000 || completion != 6 {
		t.Fatalf("expected the local estimate, got (%d, %d, %v)", prompt, completion, measured)
	}
}

func TestResolveTurnTokensFillsZeroFieldsFromEstimate(t *testing.T) {
	// Some servers report prompt tokens but leave completion at zero.
	client := stubUsageClient{usage: llm.Usage{PromptTokens: 6270}, have: true}
	prompt, completion, _ := resolveTurnTokens(client, 6000, 42)
	if prompt != 6270 || completion != 42 {
		t.Fatalf("expected the zero field backfilled, got (%d, %d)", prompt, completion)
	}
}

func TestResolveTurnTokensHandlesClientWithoutUsage(t *testing.T) {
	prompt, completion, measured := resolveTurnTokens(plainClient{}, 100, 7)
	if measured || prompt != 100 || completion != 7 {
		t.Fatalf("a client that cannot report usage must fall back, got (%d, %d, %v)", prompt, completion, measured)
	}
}

type plainClient struct{}

func (plainClient) Chat(_ context.Context, _ string, _ []llm.Message, _ []llm.Tool, _ string) (llm.Message, error) {
	return llm.Message{}, nil
}

var _ = os.Getenv

// TestSpinnerEmitsAnimatedFrames pins the actual escape-sequence output. The
// earlier tests only covered the inert path, which is why a spinner that never
// rendered could still have passed the suite.
func TestSpinnerEmitsAnimatedFrames(t *testing.T) {
	t.Setenv("FASTLLM_FORCE_SPINNER", "1")
	t.Setenv("FASTLLM_NO_SPINNER", "")
	t.Setenv("NO_COLOR", "")

	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	// Drain until EOF: a single Read returns after the first frame, which would
	// make an animation that never advances look correct.
	captured := make(chan []byte, 1)
	go func() {
		all, _ := io.ReadAll(r)
		captured <- all
	}()

	s := NewSpinner("Thinking")
	s.Start()
	time.Sleep(400 * time.Millisecond)
	s.Stop()
	w.Close()
	os.Stdout = original

	out := string(<-captured)
	if !strings.Contains(out, "Thinking") {
		t.Fatalf("label missing from spinner output: %q", out)
	}
	if !strings.Contains(out, "\r\033[2K") {
		t.Fatalf("spinner must rewrite its line, got %q", out)
	}
	var seen int
	for _, frame := range spinnerFrames {
		if strings.Contains(out, frame) {
			seen++
		}
	}
	if seen < 2 {
		t.Fatalf("expected the spinner to advance through frames, saw %d in %q", seen, out)
	}
}
