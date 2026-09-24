package harness

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"fastllm/internal/config"
	"fastllm/internal/execution"
	"fastllm/internal/files"
	"fastllm/internal/gitrepo"
	"fastllm/internal/llm"
	"fastllm/internal/media"
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
	// ContextBudgetChars sizes compaction against the active model's real context
	// window. Zero falls back to DefaultCompactionConfig, which keeps Runners
	// constructed without settings (tests, embedders) working unchanged.
	ContextBudgetChars int
	// contextWindow is the active model's window in tokens, reported to the
	// model in its environment section. Zero means unknown and is omitted.
	contextWindow int
	agents        *AgentManager
	executions    *execution.Manager
}

// compactionConfig returns the runner's budget, preferring a window-derived one.
func (r *Runner) compactionConfig() CompactionConfig {
	cfg := DefaultCompactionConfig()
	if r.ContextBudgetChars > 0 {
		cfg.MaxTotalChars = r.ContextBudgetChars
	}
	return cfg
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

	// Set before either return below, so a provider-routed endpoint gets its
	// budget too. The endpoint's own context_window wins over the model table.
	window := ResolveContextWindow(&config.Settings{Models: []config.ModelEndpoint{*endpoint}}, endpoint.ID)
	r.ContextBudgetChars = contextBudgetChars(window)
	r.contextWindow = window

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
	client.WireAPI = endpoint.WireAPI
	client.Headers = endpoint.ResolveHeaders()
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
		DefaultMaxTurns:   50,
		AllowCommands:     true,
		CommandTimeout:    60 * time.Second,
	}
	r.agents = NewAgentManager(r, 3, 2)
	r.executions = execution.NewManager()
	return r
}

// Close cancels and joins every child agent owned by this runner. Callers that
// construct a Runner own its lifetime and must close it when the request or
// interactive session ends.
func (r *Runner) Close() {
	if r != nil {
		r.agents.Close()
		if r.executions != nil {
			_ = r.executions.Close(context.Background())
		}
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
	if req.Taint == nil {
		req.Taint = NewSessionTaint()
	}
	if req.meter == nil {
		req.meter = newBudgetMeter(req.Budget)
	}
	// The time budget is a deadline on everything the run does, including
	// model calls, commands, and child agents, which inherit this context.
	if deadline := req.meter.deadline(); !deadline.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	networkPolicy, err := normalizeNetworkPolicy(req.NetworkPolicy)
	if err != nil {
		return nil, err
	}
	req.NetworkPolicy = networkPolicy

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
		maxTurns = 50
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
	// The mode is fixed for the whole run (I6), and an empty or unknown mode
	// is plan (I3): a caller that says nothing gets the least authority.
	req.PermissionMode = NormalizeMode(req.PermissionMode)
	req.WorkingDir = absWorkingDir
	req.Model = model
	req.AllowCommands = allowCmds
	req.CommandsConfigured = true
	policy := policyForRequest(req)
	allowCmds = policy.commands
	req.AllowCommands = allowCmds
	owner := r.executions
	if owner == nil {
		owner = execution.NewManager()
		defer owner.Close(context.Background())
	}
	opts, err := sandboxOptions(owner, execution.Options{Workspace: absWorkingDir, Policy: executionPolicy(req, allowCmds), Timeout: cmdTimeout, MaxOutputBytes: 64 * 1024}, req.Sandbox)
	if err != nil {
		return nil, err
	}
	scope, err := owner.Open(ctx, opts)
	if err != nil {
		return nil, err
	}
	defer scope.Close(context.Background())
	ctx = execution.WithScope(ctx, scope)
	absWorkingDir = scope.Workspace()
	req.WorkingDir = absWorkingDir

	// The one place the system prompt is assembled; callers contribute only the
	// base override and per-turn extras.
	contextWindow := r.contextWindow
	if contextWindow <= 0 {
		contextWindow = lookupKnownContextWindow(model)
	}
	systemPrompt := BuildSystemPrompt(PromptContext{
		Base:        req.SystemPrompt,
		Skills:      DiscoverWorkspaceSkills(absWorkingDir),
		Rules:       DiscoverWorkspaceRules(absWorkingDir),
		Environment: CollectEnvironment(absWorkingDir, model, contextWindow),
		TurnExtra:   req.PromptExtra,
		Sandbox:     sandboxPromptNote(scope),
		Mode:        modePrompt(req),
	})

	// A top-level run is one undo group; child agents write into their
	// parent's group, since /undo reverts the prompt as a whole.
	if req.AgentDepth == 0 {
		req.Journal.begin(req.Task)
	}

	// Initialize background process manager
	processMgr := NewProcessManager(scope)
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

	// Prepare sandboxed file reader with writes enabled or attenuated
	fileReader := files.New(absWorkingDir, policy.write)

	// Available tools: exactly what the monitor would not deny.
	tools := toolsForRequest(req, toolAvailability{
		observations: r.EnableObservations,
		delegation:   len(r.agents.Tools(req.AgentDepth)) > 0,
	})

	messages := []llm.Message{
		{Role: "system", Content: systemPrompt},
	}
	messages = append(messages, replayMessages(req.InitialMessages)...)
	messages = append(messages, llm.Message{Role: "user", Content: req.Task, Attachments: req.Attachments})

	sessionMetrics := &SessionMetrics{}

	result := &RunResult{
		Task:       req.Task,
		WorkingDir: absWorkingDir,
		Model:      model,
		History:    make([]TurnRecord, 0, maxTurns),
		Metrics:    sessionMetrics,
		// The mode the run was actually evaluated under, after fail-closed normalization.
		PermissionMode: req.PermissionMode,
	}

	var taskFinished bool
	var planBoundary bool

	for turn := 1; turn <= maxTurns; turn++ {
		if why := req.meter.exhausted(); why != "" {
			result.Turns = turn - 1
			result.Error = "stopped: " + why
			break
		}
		drainAgentInbox(&messages, req.AgentInbox)
		emit(Event{Type: EventTurnStart, Turn: turn})

		// Compact context window if messages exceed budget
		compactionConfig := planBoundaryCompactionConfig(r.compactionConfig(), messageCharacterCount(messages), planBoundary)
		messages, _ = OnlineCompactMessages(messages, compactionConfig)
		planBoundary = false

		turnStart := time.Now()
		promptTokens := countApproxTokens(messages)

		modelMessages := observations.Project(messages, turn)
		// The sink is per-turn: a discard must only retract the text streamed for
		// the turn being attempted, never an earlier turn's committed answer.
		var sink *streamSink
		if req.StreamTokens {
			streamedTurn := turn
			sink = &streamSink{
				Emit: func(fragment string) {
					emit(Event{Type: EventTokenDelta, Turn: streamedTurn, Response: fragment})
				},
				Discard: func() {
					emit(Event{Type: EventTokenDiscard, Turn: streamedTurn})
				},
			}
		}
		chatResult, err := chatWithRetryStreaming(ctx, r.LLM, routed, modelMessages, tools, req.ThinkLevel, nil, sink)
		turnDuration := time.Since(turnStart)
		if err != nil {
			result.Error = fmt.Sprintf("LLM chat error on turn %d: %v", turn, err)
			if why := req.meter.exhausted(); why != "" && ctx.Err() != nil {
				result.Error = "stopped: " + why
			}
			result.Turns = turn
			result.DurationMS = time.Since(startTime).Milliseconds()
			result.Transcript = runTranscript(messages, "")
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
		req.meter.addTurn(turnMetrics, billable)

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

			execution := r.executeTool(toolExecutionContext{
				ctx:              ctx,
				request:          req,
				fileReader:       fileReader,
				workingDir:       absWorkingDir,
				allowCommands:    allowCmds,
				commandTimeout:   cmdTimeout,
				processManager:   processMgr,
				observationStore: observationStore,
			}, call.Function.Name, call.Function.Arguments)
			toolResult := execution.output
			if execution.planBoundary {
				planBoundary = true
			}
			if execution.taskFinished {
				result.FinalResponse = execution.finalResponse
				result.Success = true
				taskFinished = true
			}
			if execution.proposedPlan != "" {
				result.ProposedPlan = execution.proposedPlan
				emit(Event{Type: EventPlanProposed, Turn: turn, Response: execution.proposedPlan})
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
			// A user-role notice, not a system one: providers with a single
			// system slot (Anthropic, Gemini) hoist a mid-conversation system
			// message to the top, where it would read as a standing instruction.
			messages = append(messages, llm.Message{
				Role:    "user",
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
	result.Transcript = runTranscript(messages, result.FinalResponse)
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
	lower := strings.ToLower(requestedPath)
	if strings.HasSuffix(lower, ".pdf") || strings.HasPrefix(content, "%PDF") {
		text, err := media.ExtractPDFText([]byte(content))
		if err == nil && strings.TrimSpace(text) != "" {
			return text
		}
	}
	if strings.HasSuffix(lower, ".png") || strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") || strings.HasSuffix(lower, ".gif") || strings.HasSuffix(lower, ".webp") {
		return fmt.Sprintf("[Image file: %s (%d bytes). To analyze this image visually, attach it in chat via /attach or paste it.]", requestedPath, len(content))
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
		// A search must not read what read_file would have to ask for (I10).
		if isSecretPath(f) {
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
	return r.executeCommand(ctx, root, command, timeout, false)
}
func (r *Runner) executeRunCommandLive(ctx context.Context, root, command string, timeout time.Duration) string {
	return r.executeCommand(ctx, root, command, timeout, true)
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
