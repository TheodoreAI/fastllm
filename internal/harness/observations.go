package harness

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"fastllm/internal/llm"
)

const (
	observationPackThreshold = 10 * 1024
	evidenceReduceThreshold  = 4 * 1024
	observationExcerptBytes  = 1024
)

type ToolOutcome struct {
	Raw         string
	ModelView   string
	DisplayView string
	Observation string
	SourceHash  string
}

type Observation struct {
	Ref       string    `json:"ref"`
	SessionID string    `json:"session_id"`
	Tool      string    `json:"tool"`
	Arguments string    `json:"arguments,omitempty"`
	Output    string    `json:"output"`
	Hash      string    `json:"hash"`
	Bytes     int       `json:"bytes"`
	Lines     int       `json:"lines"`
	CreatedAt time.Time `json:"created_at"`
}

type ObservationStore struct {
	Root      string
	mu        sync.RWMutex
	sessionID string
}

func DefaultObservationStore(sessionID string) (*ObservationStore, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	store := &ObservationStore{Root: filepath.Join(home, ".fastllm", "observations")}
	store.SetSession(sessionID)
	_ = store.Prune(time.Now().UTC().Add(-30 * 24 * time.Hour))
	return store, nil
}

func (s *ObservationStore) SetSession(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validSessionID(sessionID) {
		sessionID = "runtime"
	}
	s.sessionID = sessionID
}

func (s *ObservationStore) Put(tool, arguments, output string) (Observation, error) {
	digest := sha256.Sum256([]byte(output))
	hash := hex.EncodeToString(digest[:])
	ref := "obs_" + hash[:12]
	s.mu.RLock()
	sessionID := s.sessionID
	s.mu.RUnlock()
	observation := Observation{Ref: ref, SessionID: sessionID, Tool: tool, Arguments: arguments, Output: output, Hash: hash, Bytes: len(output), Lines: countLines(output), CreatedAt: time.Now().UTC()}
	dir := filepath.Join(s.Root, sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Observation{}, err
	}
	target := filepath.Join(dir, ref+".json.gz")
	if _, err := os.Stat(target); err == nil {
		return observation, nil
	}
	temp, err := os.CreateTemp(dir, ref+"-*.tmp")
	if err != nil {
		return Observation{}, err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return Observation{}, err
	}
	gz := gzip.NewWriter(temp)
	err = json.NewEncoder(gz).Encode(observation)
	if closeErr := gz.Close(); err == nil {
		err = closeErr
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return Observation{}, err
	}
	if err := os.Rename(tempName, target); err != nil {
		if _, statErr := os.Stat(target); statErr == nil {
			return observation, nil
		}
		return Observation{}, err
	}
	return observation, nil
}

func (s *ObservationStore) Get(ref string) (Observation, error) {
	if !validObservationRef(ref) {
		return Observation{}, fmt.Errorf("invalid observation reference %q", ref)
	}
	s.mu.RLock()
	sessionID := s.sessionID
	s.mu.RUnlock()
	file, err := os.Open(filepath.Join(s.Root, sessionID, ref+".json.gz"))
	if err != nil {
		return Observation{}, err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return Observation{}, err
	}
	defer gz.Close()
	var observation Observation
	if err := json.NewDecoder(gz).Decode(&observation); err != nil {
		return Observation{}, err
	}
	return observation, nil
}

func (s *ObservationStore) Slice(ref string, offset, limit int) (string, error) {
	observation, err := s.Get(ref)
	if err != nil {
		return "", err
	}
	lines := strings.Split(observation.Output, "\n")
	if offset < 0 {
		offset = 0
	}
	if offset > len(lines) {
		offset = len(lines)
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	end := offset + limit
	if end > len(lines) {
		end = len(lines)
	}
	return fmt.Sprintf("[Observation %s lines %d-%d of %d; sha256=%s]\n%s", ref, offset+1, end, len(lines), observation.Hash, strings.Join(lines[offset:end], "\n")), nil
}

func (s *ObservationStore) DeleteSession(sessionID string) error {
	if !validSessionID(sessionID) {
		return fmt.Errorf("invalid session ID %q", sessionID)
	}
	target := filepath.Join(s.Root, sessionID)
	relative, err := filepath.Rel(s.Root, target)
	if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
		return fmt.Errorf("unsafe observation path")
	}
	return os.RemoveAll(target)
}

func (s *ObservationStore) Prune(before time.Time) error {
	entries, err := os.ReadDir(s.Root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	s.mu.RLock()
	active := s.sessionID
	s.mu.RUnlock()
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == active || !validSessionID(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err == nil && info.ModTime().Before(before) {
			if err := s.DeleteSession(entry.Name()); err != nil {
				return err
			}
		}
	}
	return nil
}

type ObservationStats struct {
	Archived            int   `json:"archived_observations"`
	Packed              int   `json:"packed_observations"`
	Reduced             int   `json:"reduced_observations"`
	RawBytes            int64 `json:"raw_observation_bytes"`
	ProjectedBytesSaved int64 `json:"projected_bytes_saved"`
}

type ObservationManager struct {
	Store *ObservationStore
	mu    sync.Mutex
	stats ObservationStats
}

func (m *ObservationManager) Process(tool, arguments, output string, turn int) ToolOutcome {
	outcome := ToolOutcome{Raw: output, ModelView: output, DisplayView: output}
	if m == nil || m.Store == nil || len(output) < evidenceReduceThreshold {
		return outcome
	}
	observation, err := m.Store.Put(tool, arguments, output)
	if err != nil {
		return outcome
	}
	m.mu.Lock()
	m.stats.Archived++
	m.stats.RawBytes += int64(len(output))
	m.mu.Unlock()
	outcome.Observation, outcome.SourceHash = observation.Ref, observation.Hash
	if receipt, ok := reduceEvidence(tool, arguments, observation); ok {
		outcome.ModelView = receipt
		m.mu.Lock()
		m.stats.Reduced++
		m.mu.Unlock()
		return outcome
	}
	if len(output) >= observationPackThreshold {
		outcome.ModelView = observationEnvelope(observation, turn, output)
		m.mu.Lock()
		m.stats.Packed++
		m.mu.Unlock()
	}
	return outcome
}

func (m *ObservationManager) Project(messages []llm.Message, currentTurn int) []llm.Message {
	projected := ProjectObservations(messages, currentTurn)
	if m == nil {
		return projected
	}
	saved := messageCharacterCount(messages) - messageCharacterCount(projected)
	if saved > 0 {
		m.mu.Lock()
		m.stats.ProjectedBytesSaved += int64(saved)
		m.mu.Unlock()
	}
	return projected
}

func (m *ObservationManager) Stats() ObservationStats {
	if m == nil {
		return ObservationStats{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stats
}

var observationHeader = regexp.MustCompile(`^\[fastllm-observation ref=(obs_[0-9a-f]{12}) turn=([0-9]+) size=([0-9]+) hash=([0-9a-f]{64})\]\n`)

func observationEnvelope(observation Observation, turn int, output string) string {
	return fmt.Sprintf("[fastllm-observation ref=%s turn=%d size=%d hash=%s]\n%s", observation.Ref, turn, observation.Bytes, observation.Hash, output)
}

func ProjectObservations(messages []llm.Message, currentTurn int) []llm.Message {
	projected := append([]llm.Message(nil), messages...)
	for index := range projected {
		message := &projected[index]
		if message.Role != "tool" {
			continue
		}
		match := observationHeader.FindStringSubmatch(message.Content)
		if len(match) != 5 {
			continue
		}
		createdTurn, _ := strconv.Atoi(match[2])
		if currentTurn-createdTurn <= 2 {
			continue
		}
		raw := strings.TrimPrefix(message.Content, match[0])
		message.Content = packedObservation(match[1], match[3], match[4], raw)
	}
	return projected
}

func packedObservation(ref, size, hash, raw string) string {
	head, tail := completeLineExcerpt(raw, observationExcerptBytes)
	return fmt.Sprintf("[Packed observation %s]\nSize: %s bytes\nSHA-256: %s\n\nHead:\n%s\n\nTail:\n%s\n\nUse read_observation with this reference and line offset/limit for exact content.", ref, size, hash, head, tail)
}

func completeLineExcerpt(text string, budget int) (string, string) {
	lines := strings.Split(text, "\n")
	var head, tail []string
	used := 0
	for _, line := range lines {
		if used+len(line)+1 > budget/2 {
			break
		}
		head = append(head, line)
		used += len(line) + 1
	}
	used = 0
	for index := len(lines) - 1; index >= 0; index-- {
		line := lines[index]
		if used+len(line)+1 > budget/2 {
			break
		}
		tail = append([]string{line}, tail...)
		used += len(line) + 1
	}
	return strings.Join(head, "\n"), strings.Join(tail, "\n")
}

const (
	maxFailureEvidenceLines = 30
	maxSummaryEvidenceLines = 12
)

// failureLinePatterns match lines that report an actual failure. They are anchored
// on line structure rather than searching for words anywhere in the line, so a
// passing test named TestRunFailsWithoutUpstream is not read as a failure.
var failureLinePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^\s*(---\s*|===\s*)?FAIL(ED)?\b`),
	regexp.MustCompile(`^\s*panic:`),
	regexp.MustCompile(`(?i)^\s*(error|fatal|exception)\b[:\s]`),
	regexp.MustCompile(`(?i)^\s*\d+\s+(failing|failed)\b`),
	regexp.MustCompile(`(?i)\bexit\s+(code|status)\s+[1-9]`),
}

// summaryLinePatterns match per-package and whole-run verdict lines. These carry
// the answer the caller actually asked for, so they get a reserved budget.
var summaryLinePatterns = []*regexp.Regexp{
	regexp.MustCompile(`^\s*(ok|FAIL|PASS|\?)\s`),
	regexp.MustCompile(`^\s*(PASS|FAIL|OK)\s*$`),
	regexp.MustCompile(`(?i)^\s*\d+\s+(passing|passed|failing|failed|tests?)\b`),
	regexp.MustCompile(`(?i)^\s*tests?:\s`),
	regexp.MustCompile(`(?i)^\s*(build|compilation)\s+(succeeded|failed)`),
	regexp.MustCompile(`(?i)\bexit\s+(code|status)\s+\d+`),
}

func matchesAny(patterns []*regexp.Regexp, line string) bool {
	for _, pattern := range patterns {
		if pattern.MatchString(line) {
			return true
		}
	}
	return false
}

// ambiguousDiagnosticLine matches a file:line diagnostic. Failing assertions use
// this shape, but so do t.Log and t.Skip messages inside tests that did not fail,
// so it is only failure evidence once attributed to a test that actually failed.
var ambiguousDiagnosticLine = regexp.MustCompile(`^\s*[^\s:]+\.[A-Za-z]+:\d+(:\d+)?:\s`)

var goTestRunLine = regexp.MustCompile(`^\s*===\s+RUN\s+(\S+)`)

var goTestVerdictLine = regexp.MustCompile(`^\s*---\s*(PASS|FAIL|SKIP):\s+(\S+)`)

// goTestVerdicts maps each test name to its final verdict. go test buffers subtest
// output, so a verdict can appear hundreds of lines after the diagnostics it
// covers; collecting them up front is the only reliable attribution.
func goTestVerdicts(lines []string) map[string]string {
	verdicts := make(map[string]string)
	for _, line := range lines {
		if match := goTestVerdictLine.FindStringSubmatch(line); match != nil {
			verdicts[match[2]] = match[1]
		}
	}
	return verdicts
}

func isFailureLine(line, currentTest string, verdicts map[string]string) bool {
	if matchesAny(failureLinePatterns, line) {
		return true
	}
	if !ambiguousDiagnosticLine.MatchString(line) {
		return false
	}
	// Unattributed diagnostics stay in, so non-Go tool output is not silently lost.
	switch verdicts[currentTest] {
	case "PASS", "SKIP":
		return false
	}
	return true
}

func reduceEvidence(tool, arguments string, observation Observation) (string, bool) {
	if len(observation.Output) < evidenceReduceThreshold || containsSuspectedSecret(observation.Output) {
		return "", false
	}
	var verified bool
	for _, command := range verificationCommands(tool, arguments) {
		if isVerificationCommand(command) {
			verified = true
			break
		}
	}
	if !verified {
		return "", false
	}

	lines := strings.Split(observation.Output, "\n")
	verdicts := goTestVerdicts(lines)
	criticalIndexes := make(map[int]bool)
	var summaries []string
	var currentTest string
	for index, line := range lines {
		if match := goTestRunLine.FindStringSubmatch(line); match != nil {
			currentTest = match[1]
		}
		switch {
		// A whole-run verdict line is the answer rather than evidence, so it is
		// claimed before the failure patterns can absorb it.
		case matchesAny(summaryLinePatterns, line):
			summaries = append(summaries, line)
		case isFailureLine(line, currentTest, verdicts):
			for nearby := index - 1; nearby <= index+2; nearby++ {
				if nearby >= 0 && nearby < len(lines) {
					criticalIndexes[nearby] = true
				}
			}
		}
	}

	var failures []string
	for index, line := range lines {
		if criticalIndexes[index] && strings.TrimSpace(line) != "" {
			failures = append(failures, line)
		}
	}
	truncatedFailures := len(failures) > maxFailureEvidenceLines
	if truncatedFailures {
		failures = failures[:maxFailureEvidenceLines]
	}
	// Verdict lines live at the end of a run, so keep the last ones when trimming.
	truncatedSummaries := len(summaries) > maxSummaryEvidenceLines
	if truncatedSummaries {
		summaries = summaries[len(summaries)-maxSummaryEvidenceLines:]
	}
	if len(failures) == 0 && len(summaries) == 0 {
		return "", false
	}

	for _, quote := range append(append([]string{}, failures...), summaries...) {
		if !strings.Contains(observation.Output, quote) {
			return "", false
		}
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "[Verified evidence receipt]\nSource: %s\nSHA-256: %s\nOriginal: %d bytes / %d lines\n", observation.Ref, observation.Hash, observation.Bytes, observation.Lines)
	if len(summaries) > 0 {
		builder.WriteString("\nVerdict:\n")
		builder.WriteString(strings.Join(summaries, "\n"))
		builder.WriteString("\n")
		if truncatedSummaries {
			builder.WriteString("[earlier verdict lines omitted]\n")
		}
	}
	if len(failures) > 0 {
		builder.WriteString("\nFailure evidence:\n")
		builder.WriteString(strings.Join(failures, "\n"))
		builder.WriteString("\n")
		if truncatedFailures {
			builder.WriteString("[further failure lines omitted]\n")
		}
	} else {
		builder.WriteString("\nNo failure lines were detected in the archived output.\n")
	}
	fmt.Fprintf(&builder, "\nUse read_observation(ref=%q, offset, limit) for the archived original.", observation.Ref)

	receipt := builder.String()
	return receipt, len(receipt) < len(observation.Output)
}

var suspectedSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password)\s*[:=]\s*[^\s]{8,}`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}\b`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
}

func containsSuspectedSecret(output string) bool {
	for _, pattern := range suspectedSecretPatterns {
		if pattern.MatchString(output) {
			return true
		}
	}
	return false
}

// verificationCommands returns the shell commands whose output should be treated as
// verification evidence: run_command's own command, plus any command fused onto a
// mutation through then_run. An empty result means this output is not evidence.
func verificationCommands(tool, arguments string) []string {
	var parsed struct {
		Command string           `json:"command"`
		ThenRun *FollowUpCommand `json:"then_run"`
	}
	if json.Unmarshal([]byte(arguments), &parsed) != nil {
		return nil
	}
	var commands []string
	switch tool {
	case "run_command":
		if strings.TrimSpace(parsed.Command) != "" {
			commands = append(commands, parsed.Command)
		}
	case "write_file", "edit_file", "patch_file":
	default:
		return nil
	}
	if parsed.ThenRun != nil && strings.TrimSpace(parsed.ThenRun.Command) != "" {
		commands = append(commands, parsed.ThenRun.Command)
	}
	return commands
}

// isVerificationCommand reports whether a command actually runs a test, build, or
// lint tool. It inspects the program invoked in each pipeline segment rather than
// searching the raw text, so a command that merely mentions a marker word (for
// example grep -rn "test" .) is not mistaken for a test run.
func isVerificationCommand(command string) bool {
	for _, segment := range splitCommandSegments(command) {
		fields := strings.Fields(segment)
		// Skip leading environment assignments such as CGO_ENABLED=0 go test ./...
		for len(fields) > 1 && strings.Contains(fields[0], "=") && !strings.HasPrefix(fields[0], "-") {
			fields = fields[1:]
		}
		if len(fields) == 0 {
			continue
		}
		if isVerificationProgram(normalizeProgram(fields[0]), fields[1:]) {
			return true
		}
	}
	return false
}

// verificationPrograms are binaries that only ever run a test, lint, or type check.
var verificationPrograms = map[string]bool{
	"pytest": true, "jest": true, "vitest": true, "tsc": true, "eslint": true,
	"golangci-lint": true, "mypy": true, "ruff": true, "rspec": true,
	"phpunit": true, "ctest": true, "tox": true, "nox": true, "gotestsum": true,
}

// subcommandPrograms are multi-purpose drivers where the operand decides whether
// the invocation is a verification run (go test yes, go run no).
var subcommandPrograms = map[string]bool{
	"go": true, "cargo": true, "npm": true, "pnpm": true, "yarn": true, "bun": true,
	"npx": true, "dotnet": true, "mvn": true, "gradle": true, "gradlew": true,
	"make": true, "just": true,
}

var verificationOperands = map[string]bool{
	"test": true, "tests": true, "build": true, "lint": true, "check": true,
	"typecheck": true, "type-check": true, "verify": true, "vet": true, "clippy": true,
}

func isVerificationProgram(program string, args []string) bool {
	if verificationPrograms[program] {
		return true
	}
	if !subcommandPrograms[program] {
		return false
	}
	// The operand is either a script name (npm run lint) or a bare tool the driver
	// is delegating to (npx eslint .).
	operand := firstOperand(args)
	return verificationOperands[operand] || verificationPrograms[operand]
}

// firstOperand returns the first meaningful operand, skipping flags and the script
// runner words that sit between a driver and its real target (npm run test).
func firstOperand(args []string) string {
	for _, arg := range args {
		candidate := strings.ToLower(strings.Trim(arg, `"'`))
		if candidate == "" || strings.HasPrefix(candidate, "-") {
			continue
		}
		if candidate == "run" || candidate == "run-script" || candidate == "exec" {
			continue
		}
		return candidate
	}
	return ""
}

func splitCommandSegments(command string) []string {
	replacer := strings.NewReplacer("&&", "\n", "||", "\n", "|", "\n", ";", "\n", "&", "\n")
	return strings.Split(replacer.Replace(command), "\n")
}

func normalizeProgram(token string) string {
	token = strings.Trim(token, `"'`)
	if index := strings.LastIndexAny(token, `/\`); index >= 0 {
		token = token[index+1:]
	}
	token = strings.ToLower(token)
	for _, suffix := range []string{".exe", ".cmd", ".bat", ".ps1"} {
		token = strings.TrimSuffix(token, suffix)
	}
	return token
}

func validObservationRef(ref string) bool {
	return regexp.MustCompile(`^obs_[0-9a-f]{12}$`).MatchString(ref)
}
func countLines(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}
