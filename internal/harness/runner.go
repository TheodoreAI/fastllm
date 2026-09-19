package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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

	"fastllm/internal/files"
	"fastllm/internal/gitrepo"
	"fastllm/internal/llm"
	"fastllm/internal/webtools"
)

// LLMClient abstracts any LLM client (such as *llm.Router or *llm.Client)
// that can execute a chat turn with function calling.
type LLMClient interface {
	Chat(ctx context.Context, model string, messages []llm.Message, tools []llm.Tool, thinkLevel string) (llm.Message, error)
}

// Runner drives autonomous agent tasks against a sandboxed directory.
type Runner struct {
	LLM               LLMClient
	DefaultWorkingDir string
	DefaultModel      string
	DefaultMaxTurns   int
	AllowCommands     bool
	CommandTimeout    time.Duration
}

// NewRunner constructs a Runner with sensible defaults.
func NewRunner(llmClient LLMClient, defaultWorkingDir, defaultModel string) *Runner {
	if defaultWorkingDir == "" {
		defaultWorkingDir, _ = os.Getwd()
	}
	return &Runner{
		LLM:               llmClient,
		DefaultWorkingDir: defaultWorkingDir,
		DefaultModel:      defaultModel,
		DefaultMaxTurns:   20,
		AllowCommands:     true,
		CommandTimeout:    60 * time.Second,
	}
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
	if req.AllowCommands != allowCmds && req.Task != "" {
		allowCmds = req.AllowCommands
	}

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
		writeFileTool,
		editFileTool,
		patchFileTool,
		listFilesTool,
		searchFilesTool,
		webSearchTool,
		webFetchTool,
		finishTaskTool,
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

	for turn := 1; turn <= maxTurns; turn++ {
		emit(Event{Type: EventTurnStart, Turn: turn})

		// Compact context window if messages exceed budget
		messages, _ = CompactMessages(messages, DefaultCompactionConfig())

		turnStart := time.Now()
		promptTokens := countApproxTokens(messages)

		reply, err := chatWithRetry(ctx, r.LLM, model, messages, tools, req.ThinkLevel, nil)
		turnDuration := time.Since(turnStart)
		if err != nil {
			result.Error = fmt.Sprintf("LLM chat error on turn %d: %v", turn, err)
			result.Turns = turn
			result.DurationMS = time.Since(startTime).Milliseconds()
			emit(Event{Type: EventTaskFinished, Result: result, Error: result.Error})
			return result, err
		}

		compTokens := countApproxTokens([]llm.Message{reply})
		turnMetrics := ComputeTurnMetrics(turn, model, promptTokens, compTokens, turnDuration)
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

			var toolResult string
			switch call.Function.Name {
			case "read_file":
				var args struct {
					Path string `json:"path"`
				}
				_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
				toolResult = r.executeReadFile(fileReader, args.Path)

			case "write_file":
				var args struct {
					Path    string `json:"path"`
					Content string `json:"content"`
				}
				_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
				toolResult = r.executeWriteFile(fileReader, args.Path, args.Content)

			case "edit_file":
				var args struct {
					Path    string `json:"path"`
					Search  string `json:"search"`
					Replace string `json:"replace"`
				}
				_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
				toolResult = r.executeEditFile(fileReader, args.Path, args.Search, args.Replace)

			case "patch_file":
				var args struct {
					Path string `json:"path"`
					Diff string `json:"diff"`
				}
				_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
				toolResult = r.executePatchFile(fileReader, args.Path, args.Diff)

			case "list_files":
				var args struct {
					Path string `json:"path"`
				}
				_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
				toolResult = r.executeListFiles(ctx, absWorkingDir, args.Path)

			case "search_files":
				var args struct {
					Pattern string `json:"pattern"`
					Path    string `json:"path"`
				}
				_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
				toolResult = r.executeSearchFiles(ctx, absWorkingDir, args.Pattern, args.Path)

			case "web_search":
				var args struct {
					Query      string `json:"query"`
					MaxResults int    `json:"max_results"`
				}
				_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
				toolResult = webtools.SearchFormatted(ctx, args.Query, args.MaxResults)

			case "web_fetch":
				var args struct {
					URL      string `json:"url"`
					MaxBytes int    `json:"max_bytes"`
				}
				_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
				content, err := webtools.Fetch(ctx, args.URL, args.MaxBytes)
				if err != nil {
					toolResult = fmt.Sprintf("Error fetching %s: %v", args.URL, err)
				} else {
					toolResult = content
				}

			case "run_command":
				if !allowCmds {
					toolResult = "Error: run_command is disabled for this run."
				} else {
					var args struct {
						Command        string `json:"command"`
						TimeoutSeconds int    `json:"timeout_seconds"`
						Background     bool   `json:"background"`
					}
					_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
					if args.Background {
						proc, err := processMgr.Start(args.Command, absWorkingDir)
						if err != nil {
							toolResult = fmt.Sprintf("Error starting background process: %v", err)
						} else {
							toolResult = fmt.Sprintf("Process started in background with ID %s (PID %d). Check output with process_status.", proc.ID, proc.PID)
						}
					} else {
						perCallTimeout := cmdTimeout
						if args.TimeoutSeconds > 0 {
							perCallTimeout = time.Duration(args.TimeoutSeconds) * time.Second
						}
						toolResult = r.executeRunCommand(ctx, absWorkingDir, args.Command, perCallTimeout)
					}
				}

			case "process_status":
				if !allowCmds {
					toolResult = "Error: process management is disabled for this run."
				} else {
					var args struct {
						ProcessID string `json:"process_id"`
					}
					_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
					if args.ProcessID == "" {
						toolResult = processMgr.FormatProcessTable()
					} else {
						proc, out, err := processMgr.Status(args.ProcessID)
						if err != nil {
							toolResult = err.Error()
						} else {
							status := "RUNNING"
							if proc.Exited {
								status = fmt.Sprintf("EXITED (%d)", proc.ExitCode)
							}
							toolResult = fmt.Sprintf("Process %s (PID %d): %s\nCommand: %s\nOutput:\n%s",
								proc.ID, proc.PID, status, proc.Command, out)
						}
					}
				}

			case "kill_process":
				if !allowCmds {
					toolResult = "Error: process management is disabled for this run."
				} else {
					var args struct {
						ProcessID string `json:"process_id"`
					}
					_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
					if err := processMgr.Kill(args.ProcessID); err != nil {
						toolResult = fmt.Sprintf("Error killing process %s: %v", args.ProcessID, err)
					} else {
						toolResult = fmt.Sprintf("Process %s terminated.", args.ProcessID)
					}
				}

			case "finish_task":
				var args struct {
					Summary string `json:"summary"`
					Answer  string `json:"answer"`
				}
				_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
				summary := args.Summary
				if args.Answer != "" {
					summary = fmt.Sprintf("%s\n\nAnswer: %s", summary, args.Answer)
				}
				toolResult = "Task completed successfully."
				result.FinalResponse = summary
				result.Success = true
				taskFinished = true

			default:
				toolResult = fmt.Sprintf("Error: unknown tool %q", call.Function.Name)
			}

			tcRec.Result = toolResult
			turnRec.ToolCalls = append(turnRec.ToolCalls, tcRec)

			emit(Event{Type: EventToolResult, Turn: turn, ToolCall: &tcRec})

			messages = append(messages, llm.Message{
				Role:       "tool",
				Content:    toolResult,
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
			finalReply, err := chatWithRetry(ctx, r.LLM, model, messages, nil, req.ThinkLevel, nil)
			if err == nil && finalReply.Content != "" {
				result.FinalResponse = finalReply.Content
			}
			break
		}
	}

	result.DurationMS = time.Since(startTime).Milliseconds()
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
