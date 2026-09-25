package harness

import (
	"fmt"
	"strings"
	"testing"

	"fastllm/internal/llm"
)

func TestObservationStoreRoundTripAndSlice(t *testing.T) {
	store := &ObservationStore{Root: t.TempDir()}
	store.SetSession("session-1")
	original := "zero\none\ntwo\nthree"
	observation, err := store.Put("run_command", `{"command":"test"}`, original)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Slice(observation.Ref, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "one\ntwo") || strings.Contains(got, "three") {
		t.Fatalf("unexpected slice: %q", got)
	}
}

func TestObservationPackingWaitsTwoProviderRequests(t *testing.T) {
	raw := strings.Repeat("line content\n", 1000)
	store := &ObservationStore{Root: t.TempDir()}
	store.SetSession("session-1")
	manager := &ObservationManager{Store: store}
	outcome := manager.Process("read_file", `{"path":"large.log"}`, raw, 1)
	if outcome.Observation == "" || !strings.Contains(outcome.ModelView, raw) {
		t.Fatal("large output was not archived in a full envelope")
	}
	messages := []llm.Message{{Role: "tool", Content: outcome.ModelView}}
	if got := ProjectObservations(messages, 3)[0].Content; !strings.Contains(got, raw) {
		t.Fatal("packed before two subsequent requests")
	}
	got := ProjectObservations(messages, 4)[0].Content
	if strings.Contains(got, raw) || !strings.Contains(got, outcome.Observation) {
		t.Fatal("did not replace old output with stable handle")
	}
}

func TestEvidenceReceiptContainsOnlyExactLines(t *testing.T) {
	output := strings.Repeat("passing boilerplate\n", 300) + "--- FAIL: TestParser\nparser_test.go:42: expected EOF\nFAIL\n"
	store := &ObservationStore{Root: t.TempDir()}
	store.SetSession("session-1")
	manager := &ObservationManager{Store: store}
	outcome := manager.Process("run_command", `{"command":"go test ./..."}`, output, 1)
	if !strings.Contains(outcome.ModelView, "Verified evidence receipt") {
		t.Fatalf("expected receipt, got %q", outcome.ModelView)
	}
	if !strings.Contains(outcome.ModelView, "parser_test.go:42: expected EOF") {
		t.Fatal("receipt dropped exact failure evidence")
	}
	if strings.Contains(outcome.ModelView, strings.Repeat("passing boilerplate", 20)) {
		t.Fatal("receipt retained boilerplate")
	}
}

func TestEvidenceReducerFallsBackWhenSecretIsSuspected(t *testing.T) {
	output := strings.Repeat("build output\n", 400) + "API_KEY=sk-abcdefghijklmnop\nFAIL\n"
	store := &ObservationStore{Root: t.TempDir()}
	store.SetSession("session-1")
	outcome := (&ObservationManager{Store: store}).Process("run_command", `{"command":"go test ./..."}`, output, 1)
	if strings.Contains(outcome.ModelView, "Verified evidence receipt") {
		t.Fatal("secret-bearing output must not enter a reduced receipt")
	}
	if outcome.Observation == "" {
		t.Fatal("secret-bearing output should still be archived for exact retrieval")
	}
}

func TestOnlineCompactionCreatesStructuredCheckpoint(t *testing.T) {
	messages := []llm.Message{{Role: "system", Content: "system"}, {Role: "user", Content: "fix parser"}}
	for index := 0; index < 8; index++ {
		var call llm.ToolCall
		call.Function.Name = "run_command"
		call.Function.Arguments = `{"command":"go test ./..."}`
		messages = append(messages, llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call}})
		messages = append(messages, llm.Message{Role: "tool", Content: strings.Repeat("FAIL parser_test.go:42\n", 50)})
	}
	messages = append(messages, llm.Message{Role: "user", Content: "continue"}, llm.Message{Role: "assistant", Content: "working"})
	compacted, ok := OnlineCompactMessages(messages, CompactionConfig{MaxTotalChars: 1000, KeepRecentMessages: 2, MaxToolOutputChars: 100})
	if !ok || len(compacted) >= len(messages) {
		t.Fatalf("compaction did not collapse trajectory: %d -> %d", len(messages), len(compacted))
	}
	// The summary is folded into a user message -- the next one, or the task when
	// the cut falls before an assistant message -- never sent as a system message
	// or as a user message of its own.
	summary := summaryMessage(compacted)
	if summary < 0 || !strings.Contains(compacted[summary].Content, "run_command") {
		t.Fatalf("bad checkpoint: %+v", compacted)
	}
	assertAlternates(t, compacted)
}

func TestVerificationCommandMatching(t *testing.T) {
	positives := []string{
		"go test ./...",
		"go build ./...",
		"go vet ./internal/harness/",
		"CGO_ENABLED=0 go test -run TestParser ./...",
		"cargo clippy",
		"npm run test",
		"pnpm build",
		"yarn lint",
		"npm run typecheck",
		"dotnet test",
		"./gradlew check",
		"make lint",
		"pytest -q tests/",
		"npx eslint .",
		`C:\tools\golangci-lint.exe run`,
		"go build ./... && go test ./...",
		"echo starting | go test ./...",
	}
	for _, command := range positives {
		if !isVerificationCommand(command) {
			t.Errorf("expected verification command: %q", command)
		}
	}

	negatives := []string{
		`grep -rn "test" .`,
		"cat build.log",
		"go run main.go",
		"ls internal/lint",
		"git commit -m 'fix the build'",
		"curl https://example.com/test",
		"rm -rf testdata",
		"echo test",
		"",
	}
	for _, command := range negatives {
		if isVerificationCommand(command) {
			t.Errorf("did not expect verification command: %q", command)
		}
	}
}

func TestVerificationCommandsRespectToolAndFusion(t *testing.T) {
	if got := verificationCommands("run_command", `{"command":"go test ./..."}`); len(got) != 1 || got[0] != "go test ./..." {
		t.Fatalf("run_command should yield its own command, got %v", got)
	}
	if got := verificationCommands("edit_file", `{"path":"a.go","search":"x","replace":"y"}`); len(got) != 0 {
		t.Fatalf("an unfused mutation is not evidence, got %v", got)
	}
	if got := verificationCommands("edit_file", `{"path":"a.go","then_run":{"command":"go test ./..."}}`); len(got) != 1 || got[0] != "go test ./..." {
		t.Fatalf("a fused mutation should yield its follow-up, got %v", got)
	}
	if got := verificationCommands("read_file", `{"command":"go test ./..."}`); len(got) != 0 {
		t.Fatalf("unrelated tools are never evidence, got %v", got)
	}
	if got := verificationCommands("run_command", `not json`); len(got) != 0 {
		t.Fatalf("malformed arguments should yield nothing, got %v", got)
	}
}

func TestGrepMentioningTestIsNotReducedToReceipt(t *testing.T) {
	output := strings.Repeat("internal/harness/runner_test.go:12: some matching line\n", 200)
	store := &ObservationStore{Root: t.TempDir()}
	store.SetSession("session-1")
	outcome := (&ObservationManager{Store: store}).Process("run_command", `{"command":"grep -rn \"test\" ."}`, output, 1)
	if strings.Contains(outcome.ModelView, "Verified evidence receipt") {
		t.Fatal("a grep that merely mentions test must not be reduced as test evidence")
	}
}

// goTestVerboseOutput builds output shaped like `go test -v`, including the buffered
// subtest ordering where verdicts trail far behind the diagnostics they cover.
func goTestVerboseOutput(failing bool) string {
	var builder strings.Builder
	builder.WriteString("=== RUN   TestRunFailsWithoutUpstream\n")
	builder.WriteString("--- PASS: TestRunFailsWithoutUpstream (0.24s)\n")
	builder.WriteString("=== RUN   TestSkipped\n")
	builder.WriteString("    lint_test.go:162: fake oxlint script is a POSIX shell script\n")
	builder.WriteString("--- SKIP: TestSkipped (0.00s)\n")
	for index := 0; index < 120; index++ {
		builder.WriteString(fmt.Sprintf("=== RUN   TestNoisy/case%d\n", index))
		builder.WriteString(fmt.Sprintf("    noise_test.go:8: checked scenario %d with padding detail\n", index))
	}
	for index := 0; index < 120; index++ {
		builder.WriteString(fmt.Sprintf("    --- PASS: TestNoisy/case%d (0.00s)\n", index))
	}
	builder.WriteString("--- PASS: TestNoisy (0.12s)\n")
	if failing {
		builder.WriteString("=== RUN   TestRealFailure\n")
		builder.WriteString("    noise_test.go:20: expected EOF, got \"unexpected-token\"\n")
		builder.WriteString("--- FAIL: TestRealFailure (0.00s)\n")
		builder.WriteString("FAIL\n")
		builder.WriteString("FAIL\tfailmod/pkg\t0.652s\n")
		return builder.String()
	}
	builder.WriteString("PASS\n")
	builder.WriteString("ok  \tfastllm/internal/harness\t4.729s\n")
	return builder.String()
}

func receiptFor(t *testing.T, output string) string {
	t.Helper()
	store := &ObservationStore{Root: t.TempDir()}
	store.SetSession("session-1")
	outcome := (&ObservationManager{Store: store}).Process("run_command", `{"command":"go test ./... -v"}`, output, 1)
	if !strings.Contains(outcome.ModelView, "Verified evidence receipt") {
		t.Fatalf("expected a receipt, got:\n%s", outcome.ModelView)
	}
	return outcome.ModelView
}

func TestReceiptKeepsVerdictOnAPassingRun(t *testing.T) {
	receipt := receiptFor(t, goTestVerboseOutput(false))
	if !strings.Contains(receipt, "ok  \tfastllm/internal/harness") {
		t.Fatalf("receipt dropped the package verdict:\n%s", receipt)
	}
	if !strings.Contains(receipt, "No failure lines were detected") {
		t.Fatalf("a passing run must not report failure evidence:\n%s", receipt)
	}
	if strings.Contains(receipt, "TestRunFailsWithoutUpstream") {
		t.Fatalf("a passing test named ...Fails... was treated as a failure:\n%s", receipt)
	}
	if strings.Contains(receipt, "lint_test.go:162") {
		t.Fatalf("a skipped test's log line was treated as a failure:\n%s", receipt)
	}
}

func TestReceiptKeepsRealFailuresOverSubtestNoise(t *testing.T) {
	receipt := receiptFor(t, goTestVerboseOutput(true))
	for _, want := range []string{
		"Verdict:",
		"FAIL\tfailmod/pkg\t0.652s",
		"--- FAIL: TestRealFailure (0.00s)",
		"noise_test.go:20: expected EOF, got \"unexpected-token\"",
	} {
		if !strings.Contains(receipt, want) {
			t.Fatalf("receipt missing %q:\n%s", want, receipt)
		}
	}
	if strings.Contains(receipt, "checked scenario") {
		t.Fatalf("passing subtest log noise leaked into failure evidence:\n%s", receipt)
	}
}

func TestReceiptIsSubstantiallySmallerThanSource(t *testing.T) {
	output := goTestVerboseOutput(true)
	receipt := receiptFor(t, output)
	if len(receipt) > len(output)/8 {
		t.Fatalf("receipt is %d bytes for %d bytes of output; reduction is too weak", len(receipt), len(output))
	}
}

// summaryMessage is the index of the message carrying a compaction summary, or -1.
func summaryMessage(messages []llm.Message) int {
	for i, message := range messages {
		if message.Role == "user" && strings.Contains(message.Content, conversationSummaryOpen) {
			return i
		}
	}
	return -1
}

// assertAlternates fails if two user messages are adjacent, which
// strict-alternation chat templates reject.
func assertAlternates(t *testing.T, messages []llm.Message) {
	t.Helper()
	for i := 1; i < len(messages); i++ {
		if messages[i].Role == "user" && messages[i-1].Role == "user" {
			t.Fatalf("messages %d and %d are both user messages", i-1, i)
		}
	}
}
