package harness

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"fastllm/internal/config"
	"fastllm/internal/files"
	"fastllm/internal/gitrepo"
	"fastllm/internal/llm"
)

// LLMClient abstracts any LLM client (such as *llm.Router or *llm.Client)
// that can execute a chat turn with function calling.
type LLMClient interface {
	Chat(ctx context.Context, model string, messages []llm.Message, tools []llm.Tool, thinkLevel string) (llm.Message, error)
}

// Runner drives autonomous agent tasks against a sandboxed directory.
type Runner struct {
	LLM LLMClient
	// routeModel is the prefixed name the router needs for the active
	// provider-tagged model; empty when the active model is a plain
	// OpenAI-compatible endpoint.
	routeModel        string
	DefaultWorkingDir string
	DefaultModel      string
	DefaultMaxTurns   int
	AllowCommands     bool
	CommandTimeout    time.Duration
	// EnableObservations turns on archiving, evidence receipts, and packing of
	// large tool output. Off by default: measured against a control run it
	// reliably cuts context but can cost extra turns on short tasks, so it is
	// opt-in until it is shown to pay for itself on long ones.
	EnableObservations bool
	agents             *AgentManager
}

// SwitchModel reconfigures the runner's direct LLM client for a configured
// endpoint. Keeping this operation on Runner ensures every interactive UI
// switches the model and its transport settings atomically.
func (r *Runner) SwitchModel(endpoint *config.ModelEndpoint) error {
	if endpoint == nil {
		return errors.New("model endpoint is not configured")
	}
	if active := r.agents.ActiveCount(); active > 0 {
		return fmt.Errorf("cannot switch model while %d child agent(s) are running", active)
	}

	// A provider-tagged endpoint is served by one of the router's native
	// clients, which builds its own URL and speaks its own wire format. Such an
	// endpoint legitimately has no URL of its own, so it must be routed before
	// the URL check below rejects it.
	if routed, ok := routedModelID(endpoint); ok {
		if _, isRouter := r.LLM.(*llm.Router); !isRouter {
			return fmt.Errorf("model %q needs the %s provider, which this client cannot reach", endpoint.ID, endpoint.Provider)
		}
		r.DefaultModel = endpoint.ID
		r.routeModel = routed
		return nil
	}
	r.routeModel = ""

	if strings.TrimSpace(endpoint.URL) == "" {
		return fmt.Errorf("model %q has no endpoint URL", endpoint.ID)
	}

	client, ok := localClient(r.LLM)
	if !ok {
		return fmt.Errorf("model switching is not supported by %T", r.LLM)
	}

	client.BaseURL = strings.TrimRight(strings.TrimSpace(endpoint.URL), "/")
	client.APIKey = endpoint.ResolveAPIKey()
	client.ChatModel = endpoint.ID
	client.SendThink = config.ShouldSendThink(endpoint, client.BaseURL)
	client.Temperature = nil
	client.TopP = nil
	client.MaxTokens = nil
	applyModelParameters(client, endpoint.Parameters)
	r.DefaultModel = endpoint.ID
	return nil
}

type FollowUpCommand struct {
	Command        string `json:"command"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// NewRunner constructs a Runner with sensible defaults.
func NewRunner(llmClient LLMClient, defaultWorkingDir, defaultModel string) *Runner {
	if defaultWorkingDir == "" {
		defaultWorkingDir, _ = os.Getwd()
	}
	r := &Runner{
		LLM:               llmClient,
		DefaultWorkingDir: defaultWorkingDir,
		DefaultModel:      defaultModel,
		DefaultMaxTurns:   20,
		AllowCommands:     true,
		CommandTimeout:    60 * time.Second,
	}
	r.agents = NewAgentManager(r, 3, 2)
	return r
}

// Close cancels and joins every child agent owned by this runner. Callers that
// construct a Runner own its lifetime and must close it when the request or
// interactive session ends.
func (r *Runner) Close() {
	if r != nil {
		r.agents.Close()
	}
}

// localClient reaches the direct OpenAI-compatible client, whether the runner
// holds one outright or holds a router that wraps one.
func localClient(client LLMClient) (*llm.Client, bool) {
	switch c := client.(type) {
	case *llm.Client:
		return c, true
	case *llm.Router:
		return c.Local, c.Local != nil
	}
	return nil, false
}

// Run executes an autonomous task to completion or until max turns are reached.
func (r *Runner) Run(ctx context.Context, req RunRequest, onEvent func(Event)) (*RunResult, error) {
	startTime := time.Now()

	workingDir := req.WorkingDir
	if workingDir == "" {
		workingDir = r.DefaultWorkingDir
	}
	absWorkingDir, err := filepath.Abs(workingDir)
	if err != nil {
		return nil, fmt.Errorf("resolve working dir %q: %w", workingDir, err)
	}

	model := req.Model
	if model == "" {
		model = r.DefaultModel
	}
	// Callers name the model the way the user sees it; the router dispatches on
	// a prefixed name. Translating here keeps the display name in sessions,
	// /status and the model picker while still reaching the native client.
	routed := model
	if r.routeModel != "" && model == r.DefaultModel {
		routed = r.routeModel
	}

	maxTurns := req.MaxTurns
	if maxTurns <= 0 {
		maxTurns = r.DefaultMaxTurns
	}
	if maxTurns <= 0 {
		maxTurns = 20
	}

	cmdTimeout := req.CommandTimeout
	if cmdTimeout <= 0 {
		cmdTimeout = r.CommandTimeout
	}
	if cmdTimeout <= 0 {
		cmdTimeout = 60 * time.Second
	}

	allowCmds := r.AllowCommands
	if req.CommandsConfigured {
		allowCmds = req.AllowCommands
	} else if req.AllowCommands {
		allowCmds = true
	}
	readOnly := req.PermissionMode == PermissionReadOnly || (req.AgentDepth > 0 && req.PermissionMode == PermissionAsk)
	if readOnly {
		allowCmds = false
	}
	req.WorkingDir = absWorkingDir
	req.Model = model
	req.AllowCommands = allowCmds

	systemPrompt := req.SystemPrompt
	if strings.TrimSpace(systemPrompt) == "" {
		systemPrompt = DefaultSystemPrompt
	}

	// Auto-discover workspace rules (AGENTS.md, CLAUDE.md, etc.)
	discoveredRules := DiscoverWorkspaceRules(absWorkingDir)
	if len(discoveredRules) > 0 {
		systemPrompt += FormatRulesForPrompt(discoveredRules)
	}

	// Initialize git checkpoint manager and capture initial state
	checkpointMgr := NewCheckpointManager(absWorkingDir)
	if checkpointMgr.IsGitRepo() {
		_, _ = checkpointMgr.CreateCheckpoint(ctx, "task-start: "+req.Task)
	}

	// Initialize background process manager
	processMgr := NewProcessManager()
	defer processMgr.KillAll()
	var observationStore *ObservationStore
	if r.EnableObservations {
		observationStore, _ = DefaultObservationStore(fmt.Sprintf("run-%d", time.Now().UnixNano()))
	}
	observations := &ObservationManager{Store: observationStore}

	emit := func(ev Event) {
		if onEvent != nil {
			onEvent(ev)
		}
	}

	// Prepare sandboxed file reader with writes enabled
	fileReader := files.New(absWorkingDir, true)

	// Available tools
	tools := []llm.Tool{
		readFileTool,
		listFilesTool,
		searchFilesTool,
		globFilesTool,
		webSearchTool,
		webFetchTool,
		updatePlanTool,
		finishTaskTool,
	}
	if !readOnly {
		tools = append(tools, writeFileTool, editFileTool, patchFileTool)
	}
	if readOnly {
		tools = append(tools, agentStatusTool, sendAgentMessageTool)
	} else {
		tools = append(tools, r.agents.Tools(req.AgentDepth)...)
	}
	if r.EnableObservations {
		tools = append(tools, readObservationTool)
	}
	if allowCmds {
		tools = append(tools, runCommandTool, processStatusTool, killProcessTool)
	}

	messages := []llm.Message{
		{Role: "system", Content: systemPrompt},
	}
	for _, m := range req.InitialMessages {
		if m.Role != "system" && strings.TrimSpace(m.Content) != "" {
			messages = append(messages, llm.Message{Role: m.Role, Content: m.Content})
		}
	}
	messages = append(messages, llm.Message{Role: "user", Content: req.Task})

	sessionMetrics := &SessionMetrics{}

	result := &RunResult{
		Task:       req.Task,
		WorkingDir: absWorkingDir,
		Model:      model,
		History:    make([]TurnRecord, 0, maxTurns),
		Metrics:    sessionMetrics,
	}

	var taskFinished bool
	var planBoundary bool

	for turn := 1; turn <= maxTurns; turn++ {
		drainAgentInbox(&messages, req.AgentInbox)
		emit(Event{Type: EventTurnStart, Turn: turn})

		// Compact context window if messages exceed budget
		compactionConfig := planBoundaryCompactionConfig(DefaultCompactionConfig(), messageCharacterCount(messages), planBoundary)
		messages, _ = OnlineCompactMessages(messages, compactionConfig)
		planBoundary = false

		turnStart := time.Now()
		promptTokens := countApproxTokens(messages)

		modelMessages := observations.Project(messages, turn)
		chatResult, err := chatWithRetry(ctx, r.LLM, routed, modelMessages, tools, req.ThinkLevel, nil)
		turnDuration := time.Since(turnStart)
		if err != nil {
			result.Error = fmt.Sprintf("LLM chat error on turn %d: %v", turn, err)
			result.Turns = turn
			result.DurationMS = time.Since(startTime).Milliseconds()
			emit(Event{Type: EventTaskFinished, Result: result, Error: result.Error})
			return result, err
		}
		reply := chatResult.Message

		compTokens := countApproxTokens([]llm.Message{reply})
		promptTokens, compTokens, _ = resolveTurnTokens(chatResult.Usage, chatResult.HasUsage, promptTokens, compTokens)
		// A provider-routed model always bills; otherwise it depends on whether
		// the direct client points at a public endpoint or at local hardware.
		billable := r.routeModel != ""
		if !billable {
			if direct, ok := localClient(r.LLM); ok {
				billable = billableEndpoint(direct.BaseURL)
			}
		}
		turnMetrics := ComputeTurnMetrics(turn, model, promptTokens, compTokens, turnDuration, billable)
		sessionMetrics.Add(turnMetrics)

		turnRec := TurnRecord{
			Turn:    turn,
			Metrics: &turnMetrics,
		}

		// If model produced no tool calls, it has finished with a textual answer.
		if len(reply.ToolCalls) == 0 {
			result.FinalResponse = reply.Content
			result.Success = true
			result.Turns = turn
			turnRec.Response = reply.Content
			result.History = append(result.History, turnRec)
			emit(Event{Type: EventTurnComplete, Turn: turn, Response: reply.Content, Metrics: &turnMetrics})
			taskFinished = true
			break
		}

		messages = append(messages, reply)

		// Execute all tool calls
		for _, call := range reply.ToolCalls {
			normalizedArgs, argErr := normalizeToolArguments(call.Function.Arguments)
			if argErr != nil {
				toolResult := argErr.Error()
				tcRec := ToolCallRecord{ID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments, Result: toolResult}
				emit(Event{Type: EventToolResult, Turn: turn, ToolCall: &tcRec})
				messages = append(messages, llm.Message{Role: "tool", Content: toolResult, ToolCallID: call.ID})
				continue
			}
			call.Function.Arguments = normalizedArgs
			tcRec := ToolCallRecord{
				ID:        call.ID,
				Name:      call.Function.Name,
				Arguments: call.Function.Arguments,
			}
			emit(Event{Type: EventToolCall, Turn: turn, ToolCall: &tcRec})

			var execution toolExecutionResult
			if req.Authorize != nil && requiresPermission(call.Function.Name) && !req.Authorize(call.Function.Name, summarizeArgs(call.Function.Arguments)) {
				execution.output = "Permission denied by user for " + call.Function.Name + "."
			} else {
				execution = r.executeTool(toolExecutionContext{
					ctx:              ctx,
					request:          req,
					fileReader:       fileReader,
					workingDir:       absWorkingDir,
					allowCommands:    allowCmds,
					commandTimeout:   cmdTimeout,
					processManager:   processMgr,
					observationStore: observationStore,
				}, call.Function.Name, call.Function.Arguments)
			}
			toolResult := execution.output
			if execution.planBoundary {
				planBoundary = true
			}
			if execution.taskFinished {
				result.FinalResponse = execution.finalResponse
				result.Success = true
				taskFinished = true
			}

			outcome := observations.Process(call.Function.Name, call.Function.Arguments, toolResult, turn)
			tcRec.Result = outcome.DisplayView
			turnRec.ToolCalls = append(turnRec.ToolCalls, tcRec)

			emit(Event{Type: EventToolResult, Turn: turn, ToolCall: &tcRec})

			messages = append(messages, llm.Message{
				Role:       "tool",
				Content:    outcome.ModelView,
				ToolCallID: call.ID,
			})
		}

		result.History = append(result.History, turnRec)
		emit(Event{Type: EventTurnComplete, Turn: turn, Metrics: &turnMetrics})

		if taskFinished {
			result.Turns = turn
			break
		}

		// Reached the turn limit without finish_task or a pure text response
		if turn == maxTurns {
			result.Turns = turn
			result.Error = "max turns limit reached before task finished"
			// Give model one final chance to state its final summary with no tools
			messages = append(messages, llm.Message{
				Role:    "system",
				Content: "Turn budget exhausted. Stop calling tools and provide your final response summarizing what was done and what remains.",
			})
			finalResult, err := chatWithRetry(ctx, r.LLM, routed, messages, nil, req.ThinkLevel, nil)
			if err == nil && finalResult.Message.Content != "" {
				result.FinalResponse = finalResult.Message.Content
			}
			break
		}
	}

	result.DurationMS = time.Since(startTime).Milliseconds()
	sessionMetrics.ObservationEfficiency = observations.Stats()
	emit(Event{Type: EventTaskFinished, Result: result, Error: result.Error})
	return result, nil
}

func (r *Runner) executeReadFile(fileReader *files.Reader, requestedPath string) string {
	if strings.TrimSpace(requestedPath) == "" {
		return "Error: path is required."
	}
	content, truncated, err := fileReader.Read(requestedPath)
	if err != nil {
		return "Error reading file: " + err.Error()
	}
	if truncated {
		content += "\n\n[truncated to 64KB]"
	}
	return content
}

func (r *Runner) executeWriteFile(fileReader *files.Reader, requestedPath, content string) string {
	if strings.TrimSpace(requestedPath) == "" {
		return "Error: path is required."
	}
	if len(content) > files.MaxWriteBytes*4 {
		return fmt.Sprintf("Error: content exceeds maximum size (%d bytes).", files.MaxWriteBytes*4)
	}
	if err := fileReader.Write(requestedPath, content); err != nil {
		return "Error writing file: " + err.Error()
	}
	return fmt.Sprintf("Successfully wrote %d bytes to %s.", len(content), requestedPath)
}

func (r *Runner) executeEditFile(fileReader *files.Reader, requestedPath, search, replace string) string {
	if strings.TrimSpace(requestedPath) == "" {
		return "Error: path is required."
	}
	if search == "" {
		return "Error: search string cannot be empty."
	}
	existing, exists, truncated, err := fileReader.ExistingContent(requestedPath)
	if err != nil {
		return "Error checking file: " + err.Error()
	}
	if !exists {
		return fmt.Sprintf("Error: file %q does not exist. Use write_file to create new files.", requestedPath)
	}
	if truncated {
		return fmt.Sprintf("Error: file %q is too large to safely edit with search/replace.", requestedPath)
	}

	newContent, err := ResilientReplace(existing, search, replace)
	if err != nil {
		return fmt.Sprintf("Error editing %s: %v", requestedPath, err)
	}

	if err := fileReader.Write(requestedPath, newContent); err != nil {
		return "Error applying edit: " + err.Error()
	}
	return fmt.Sprintf("Successfully edited %s.", requestedPath)
}

func (r *Runner) executePatchFile(fileReader *files.Reader, requestedPath, diffText string) string {
	if strings.TrimSpace(requestedPath) == "" {
		return "Error: path is required."
	}
	if strings.TrimSpace(diffText) == "" {
		return "Error: diff content cannot be empty."
	}
	existing, exists, truncated, err := fileReader.ExistingContent(requestedPath)
	if err != nil {
		return "Error checking file: " + err.Error()
	}
	if !exists {
		return fmt.Sprintf("Error: file %q does not exist. Use write_file to create new files.", requestedPath)
	}
	if truncated {
		return fmt.Sprintf("Error: file %q is too large to safely patch.", requestedPath)
	}

	filesDiff, err := ParseUnifiedDiff(diffText)
	if err != nil {
		return fmt.Sprintf("Error parsing unified diff: %v", err)
	}
	if len(filesDiff) == 0 || len(filesDiff[0].Hunks) == 0 {
		return "Error: no valid diff hunks found in provided diff."
	}

	newContent, err := ApplyPatch(existing, filesDiff[0].Hunks)
	if err != nil {
		return fmt.Sprintf("Error applying diff patch to %s: %v", requestedPath, err)
	}

	if err := fileReader.Write(requestedPath, newContent); err != nil {
		return "Error saving patched file: " + err.Error()
	}
	return fmt.Sprintf("Successfully patched %s (%d hunks applied).", requestedPath, len(filesDiff[0].Hunks))
}

func countApproxTokens(messages []llm.Message) int {
	chars := 0
	for _, m := range messages {
		chars += len(m.Content)
		for _, tc := range m.ToolCalls {
			chars += len(tc.Function.Name) + len(tc.Function.Arguments)
		}
	}
	if chars == 0 {
		return 1
	}
	toks := chars / 4
	if toks < 1 {
		toks = 1
	}
	return toks
}

func (r *Runner) executeListFiles(ctx context.Context, root, requestedPath string) string {
	all, err := gitrepo.ListFiles(ctx, root)
	if errors.Is(err, gitrepo.ErrNotARepo) {
		all, err = walkFiles(root)
	}
	if err != nil {
		return "Error listing files: " + err.Error()
	}

	prefix := path.Clean(strings.Trim(requestedPath, "/"))
	var matched []string
	for _, f := range all {
		if prefix == "" || prefix == "." || f == prefix || strings.HasPrefix(f, prefix+"/") {
			matched = append(matched, f)
		}
	}

	if len(matched) == 0 {
		if prefix == "" || prefix == "." {
			return "(no files found)"
		}
		return fmt.Sprintf("No files found under %q.", requestedPath)
	}

	const maxListEntries = 200
	truncated := len(matched) > maxListEntries
	if truncated {
		matched = matched[:maxListEntries]
	}
	result := strings.Join(matched, "\n")
	if truncated {
		result += fmt.Sprintf("\n\n[truncated to %d entries]", maxListEntries)
	}
	return result
}

func (r *Runner) executeSearchFiles(ctx context.Context, root, pattern, requestedPath string) string {
	if strings.TrimSpace(pattern) == "" {
		return "Error: pattern must not be empty."
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "Error: invalid regular expression: " + err.Error()
	}

	all, err := gitrepo.ListFiles(ctx, root)
	if errors.Is(err, gitrepo.ErrNotARepo) {
		all, err = walkFiles(root)
	}
	if err != nil {
		return "Error listing files: " + err.Error()
	}

	prefix := path.Clean(strings.Trim(requestedPath, "/"))
	var matches []string
	const maxMatches = 150
	const maxFileBytes = 1024 * 1024

scan:
	for _, f := range all {
		if prefix != "" && prefix != "." && f != prefix && !strings.HasPrefix(f, prefix+"/") {
			continue
		}
		fullPath := filepath.Join(root, f)
		info, err := os.Stat(fullPath)
		if err != nil || info.IsDir() || info.Size() > maxFileBytes {
			continue
		}
		file, err := os.Open(fullPath)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if re.MatchString(line) {
				matches = append(matches, fmt.Sprintf("%s:%d: %s", f, lineNum, strings.TrimSpace(line)))
				if len(matches) >= maxMatches {
					file.Close()
					break scan
				}
			}
		}
		file.Close()
	}

	if len(matches) == 0 {
		return "(no matches found)"
	}
	res := strings.Join(matches, "\n")
	if len(matches) >= maxMatches {
		res += fmt.Sprintf("\n\n[truncated to %d matches]", maxMatches)
	}
	return res
}

func (r *Runner) executeGlobFiles(ctx context.Context, root, patternValue, requestedPath string) string {
	patternValue = strings.TrimSpace(strings.ReplaceAll(patternValue, "\\", "/"))
	if patternValue == "" {
		return "Error: pattern must not be empty."
	}
	re, err := compileGlob(patternValue)
	if err != nil {
		return "Error: invalid glob pattern: " + err.Error()
	}
	all, err := gitrepo.ListFiles(ctx, root)
	if errors.Is(err, gitrepo.ErrNotARepo) {
		all, err = walkFiles(root)
	}
	if err != nil {
		return "Error listing files: " + err.Error()
	}

	prefix := path.Clean(strings.Trim(strings.ReplaceAll(requestedPath, "\\", "/"), "/"))
	var matches []string
	for _, file := range all {
		if prefix != "" && prefix != "." && file != prefix && !strings.HasPrefix(file, prefix+"/") {
			continue
		}
		candidate := file
		if prefix != "" && prefix != "." {
			candidate = strings.TrimPrefix(strings.TrimPrefix(file, prefix), "/")
		}
		if re.MatchString(candidate) {
			matches = append(matches, file)
		}
	}
	if len(matches) == 0 {
		return "(no files matched)"
	}
	const maxGlobEntries = 200
	truncated := len(matches) > maxGlobEntries
	if truncated {
		matches = matches[:maxGlobEntries]
	}
	result := strings.Join(matches, "\n")
	if truncated {
		result += fmt.Sprintf("\n\n[truncated to %d entries]", maxGlobEntries)
	}
	return result
}

func compileGlob(glob string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		switch glob[i] {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				i++
				if i+1 < len(glob) && glob[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '[':
			end := strings.IndexByte(glob[i+1:], ']')
			if end < 0 {
				return nil, fmt.Errorf("unterminated character class")
			}
			end += i + 1
			class := glob[i+1 : end]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			b.WriteByte('[')
			b.WriteString(class)
			b.WriteByte(']')
			i = end
		default:
			b.WriteString(regexp.QuoteMeta(string(glob[i])))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

func (r *Runner) executeRunCommand(ctx context.Context, root, command string, timeout time.Duration) string {
	if strings.TrimSpace(command) == "" {
		return "Error: command is required."
	}

	ctxTimeout, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctxTimeout, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", command)
	} else {
		cmd = exec.CommandContext(ctxTimeout, "sh", "-c", command)
	}
	cmd.Dir = root

	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf

	runErr := cmd.Run()

	output := outBuf.String()
	const maxOutputBytes = 64 * 1024
	if len(output) > maxOutputBytes {
		output = output[:maxOutputBytes] + "\n\n[output truncated to 64KB]"
	}

	if ctxTimeout.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("Command timed out after %v.\nOutput so far:\n%s", timeout, output)
	}

	exitCode := 0
	if runErr != nil {
		exitCode = 1
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		return fmt.Sprintf("Exit code: %d (Error: %v)\nOutput:\n%s", exitCode, runErr, output)
	}

	return fmt.Sprintf("Exit code: 0\nOutput:\n%s", output)
}

func (r *Runner) executeRunCommandLive(ctx context.Context, root, command string, timeout time.Duration) string {
	if strings.TrimSpace(command) == "" {
		return "Error: command is required."
	}
	ctxTimeout, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctxTimeout, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", command)
	} else {
		cmd = exec.CommandContext(ctxTimeout, "sh", "-c", command)
	}
	cmd.Dir = root
	var buffer bytes.Buffer
	writer := io.MultiWriter(os.Stdout, &buffer)
	cmd.Stdout, cmd.Stderr = writer, writer
	runErr := cmd.Run()
	output := buffer.String()
	if len(output) > 64*1024 {
		output = output[:64*1024] + "\n\n[output truncated to 64KB]"
	}
	if ctxTimeout.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("Command timed out after %v.\nOutput so far:\n%s", timeout, output)
	}
	if runErr != nil {
		exitCode := 1
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		return fmt.Sprintf("Exit code: %d (Error: %v)\nOutput:\n%s", exitCode, runErr, output)
	}
	return fmt.Sprintf("Exit code: 0\nOutput:\n%s", output)
}

func (r *Runner) executeFollowUp(ctx context.Context, root, mutationResult string, followUp *FollowUpCommand, defaultTimeout time.Duration, allowCommands, live bool) string {
	if followUp == nil {
		return mutationResult
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(mutationResult)), "error") {
		return mutationResult + "\n\n[Follow-up skipped because the mutation failed.]"
	}
	if !allowCommands {
		return mutationResult + "\n\n[Follow-up skipped because commands are disabled.]"
	}
	timeout := defaultTimeout
	if followUp.TimeoutSeconds > 0 {
		timeout = time.Duration(followUp.TimeoutSeconds) * time.Second
	}
	var commandResult string
	if live {
		commandResult = r.executeRunCommandLive(ctx, root, followUp.Command, timeout)
	} else {
		commandResult = r.executeRunCommand(ctx, root, followUp.Command, timeout)
	}
	return fmt.Sprintf("%s\n\n[Follow-up verification: %s]\n%s", mutationResult, followUp.Command, commandResult)
}

// formatPlanUpdate renders a plan snapshot as the tool result for update_plan.
// Completed steps are listed explicitly so they survive into a state checkpoint
// when older trajectory messages are compacted away.
func formatPlanUpdate(goal string, completed []string, current string, remaining []string) string {
	var builder strings.Builder
	builder.WriteString("[Plan Updated]\n")
	if trimmed := compactLine(goal, 200); trimmed != "" {
		builder.WriteString("Goal: " + trimmed + "\n")
	}
	writePlanSection(&builder, "Completed", completed)
	if trimmed := compactLine(current, 200); trimmed != "" {
		writeCheckpointSection(&builder, "Current", []string{trimmed})
	}
	writePlanSection(&builder, "Remaining", remaining)
	return strings.TrimSpace(builder.String())
}

func writePlanSection(builder *strings.Builder, title string, steps []string) {
	var rendered []string
	for _, step := range steps {
		if text := compactLine(step, 200); text != "" {
			rendered = append(rendered, text)
		}
	}
	writeCheckpointSection(builder, title, rendered)
}

func walkFiles(root string) ([]string, error) {
	var filesList []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == "dist" || name == "build" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		filesList = append(filesList, filepath.ToSlash(rel))
		return nil
	})
	return filesList, err
}
