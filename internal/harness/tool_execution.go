package harness

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fastllm/internal/files"
	"fastllm/internal/webtools"
)

type toolExecutionContext struct {
	ctx               context.Context
	request           RunRequest
	fileReader        *files.Reader
	workingDir        string
	allowCommands     bool
	commandTimeout    time.Duration
	processManager    *ProcessManager
	observationStore  *ObservationStore
	liveCommandOutput bool
}

type toolExecutionResult struct {
	output        string
	planBoundary  bool
	taskFinished  bool
	finalResponse string
	proposedPlan  string
}

func decodeArguments[T any](raw string) (T, error) {
	var args T
	if err := decodeToolArguments(raw, &args); err != nil {
		return args, err
	}
	return args, nil
}

func argumentFailure(err error) toolExecutionResult {
	return toolExecutionResult{output: "Error: " + err.Error()}
}

func (r *Runner) executeTool(execCtx toolExecutionContext, name, rawArgs string) toolExecutionResult {
	if err := execCtx.ctx.Err(); err != nil {
		return argumentFailure(err)
	}
	// Every call passes the monitor first, whether or not the tool was offered:
	// a model can name any tool it likes.
	if ok, refusal := admit(execCtx.request, name, rawArgs); !ok {
		return toolExecutionResult{output: refusal}
	}
	// Authorization may block waiting for the user while Escape cancels the run.
	if err := execCtx.ctx.Err(); err != nil {
		return argumentFailure(err)
	}
	// Permitted is not the same as affordable: the budget is charged next.
	if refusal := execCtx.request.meter.charge(name, rawArgs); refusal != "" {
		return toolExecutionResult{output: refusal}
	}
	// Journal the file first, so /undo can put it back (journal.go).
	if toolClasses[name] == classWrite && execCtx.request.Journal != nil {
		if requested, target := journalTarget(execCtx.fileReader, rawArgs); target != "" {
			execCtx.request.Journal.before(target, requested)
			defer execCtx.request.Journal.after(target)
		}
	}
	policy := policyForRequest(execCtx.request)
	execCtx.allowCommands = execCtx.allowCommands && policy.commands
	switch name {
	case "read_file":
		args, err := decodeArguments[struct {
			Path string `json:"path"`
		}](rawArgs)
		if err != nil {
			return argumentFailure(err)
		}
		return toolExecutionResult{output: r.executeReadFile(execCtx.fileReader, args.Path)}

	case "write_file":
		args, err := decodeArguments[struct {
			Path    string           `json:"path"`
			Content string           `json:"content"`
			ThenRun *FollowUpCommand `json:"then_run"`
		}](rawArgs)
		if err != nil {
			return argumentFailure(err)
		}
		output := r.executeWriteFile(execCtx.fileReader, args.Path, args.Content)
		output = r.executeFollowUp(execCtx.ctx, execCtx.workingDir, output, args.ThenRun, execCtx.commandTimeout, execCtx.allowCommands, execCtx.liveCommandOutput)
		return toolExecutionResult{output: output}

	case "edit_file":
		args, err := decodeArguments[struct {
			Path    string           `json:"path"`
			Search  string           `json:"search"`
			Replace string           `json:"replace"`
			ThenRun *FollowUpCommand `json:"then_run"`
		}](rawArgs)
		if err != nil {
			return argumentFailure(err)
		}
		output := r.executeEditFile(execCtx.fileReader, args.Path, args.Search, args.Replace)
		output = r.executeFollowUp(execCtx.ctx, execCtx.workingDir, output, args.ThenRun, execCtx.commandTimeout, execCtx.allowCommands, execCtx.liveCommandOutput)
		return toolExecutionResult{output: output}

	case "patch_file":
		args, err := decodeArguments[struct {
			Path    string           `json:"path"`
			Diff    string           `json:"diff"`
			ThenRun *FollowUpCommand `json:"then_run"`
		}](rawArgs)
		if err != nil {
			return argumentFailure(err)
		}
		output := r.executePatchFile(execCtx.fileReader, args.Path, args.Diff)
		output = r.executeFollowUp(execCtx.ctx, execCtx.workingDir, output, args.ThenRun, execCtx.commandTimeout, execCtx.allowCommands, execCtx.liveCommandOutput)
		return toolExecutionResult{output: output}

	case "list_files":
		args, err := decodeArguments[struct {
			Path string `json:"path"`
		}](rawArgs)
		if err != nil {
			return argumentFailure(err)
		}
		return toolExecutionResult{output: r.executeListFiles(execCtx.ctx, execCtx.workingDir, args.Path)}

	case "search_files":
		args, err := decodeArguments[struct {
			Pattern string `json:"pattern"`
			Path    string `json:"path"`
		}](rawArgs)
		if err != nil {
			return argumentFailure(err)
		}
		return toolExecutionResult{output: r.executeSearchFiles(execCtx.ctx, execCtx.workingDir, args.Pattern, args.Path)}

	case "glob_files":
		args, err := decodeArguments[struct {
			Pattern string `json:"pattern"`
			Path    string `json:"path"`
		}](rawArgs)
		if err != nil {
			return argumentFailure(err)
		}
		return toolExecutionResult{output: r.executeGlobFiles(execCtx.ctx, execCtx.workingDir, args.Pattern, args.Path)}

	case "spawn_agent":
		if !policy.delegate {
			return toolExecutionResult{output: "Agent delegation is disabled by capability policy."}
		}
		return toolExecutionResult{output: r.executeAgentTool(execCtx.ctx, execCtx.request, name, rawArgs)}

	case "agent_status", "send_agent_message", "cancel_agent":
		return toolExecutionResult{output: r.executeAgentTool(execCtx.ctx, execCtx.request, name, rawArgs)}

	case "web_search":
		if !policy.network {
			return toolExecutionResult{output: "Network access is denied by policy for this agent."}
		}
		args, err := decodeArguments[struct {
			Query      string `json:"query"`
			MaxResults int    `json:"max_results"`
		}](rawArgs)
		if err != nil {
			return argumentFailure(err)
		}
		return toolExecutionResult{output: webtools.SearchFormatted(execCtx.ctx, args.Query, args.MaxResults)}

	case "web_fetch":
		if !policy.network {
			return toolExecutionResult{output: "Network access is denied by policy for this agent."}
		}
		args, err := decodeArguments[struct {
			URL      string `json:"url"`
			MaxBytes int    `json:"max_bytes"`
		}](rawArgs)
		if err != nil {
			return argumentFailure(err)
		}
		content, fetchErr := webtools.Fetch(execCtx.ctx, args.URL, args.MaxBytes)
		if fetchErr != nil {
			return toolExecutionResult{output: fmt.Sprintf("Error fetching %s: %v", args.URL, fetchErr)}
		}
		return toolExecutionResult{output: content}

	case "read_observation":
		args, err := decodeArguments[struct {
			Ref    string `json:"ref"`
			Offset int    `json:"offset"`
			Limit  int    `json:"limit"`
		}](rawArgs)
		if err != nil {
			return argumentFailure(err)
		}
		if execCtx.observationStore == nil {
			return toolExecutionResult{output: "Error: observation storage is unavailable."}
		}
		content, readErr := execCtx.observationStore.Slice(args.Ref, args.Offset, args.Limit)
		if readErr != nil {
			return toolExecutionResult{output: "Error reading observation: " + readErr.Error()}
		}
		return toolExecutionResult{output: content}

	case "update_plan":
		args, err := decodeArguments[struct {
			Goal      string   `json:"goal"`
			Completed []string `json:"completed"`
			Current   string   `json:"current"`
			Remaining []string `json:"remaining"`
		}](rawArgs)
		if err != nil {
			return argumentFailure(err)
		}
		return toolExecutionResult{
			output:       formatPlanUpdate(args.Goal, args.Completed, args.Current, args.Remaining),
			planBoundary: len(args.Completed) > 0,
		}

	case "run_command":
		if !execCtx.allowCommands {
			return toolExecutionResult{output: "Error: run_command is disabled for this run."}
		}
		args, err := decodeArguments[struct {
			Command        string `json:"command"`
			TimeoutSeconds int    `json:"timeout_seconds"`
			Background     bool   `json:"background"`
		}](rawArgs)
		if err != nil {
			return argumentFailure(err)
		}
		if args.Background {
			proc, startErr := execCtx.processManager.Start(args.Command, execCtx.workingDir)
			if startErr != nil {
				return toolExecutionResult{output: fmt.Sprintf("Error starting background process: %v", startErr)}
			}
			return toolExecutionResult{output: fmt.Sprintf("Process started in background with ID %s (PID %d). Check output with process_status.", proc.ID, proc.PID)}
		}
		perCallTimeout := execCtx.commandTimeout
		if args.TimeoutSeconds > 0 {
			perCallTimeout = time.Duration(args.TimeoutSeconds) * time.Second
		}
		if execCtx.liveCommandOutput {
			return toolExecutionResult{output: r.executeRunCommandLive(execCtx.ctx, execCtx.workingDir, args.Command, perCallTimeout)}
		}
		return toolExecutionResult{output: r.executeRunCommand(execCtx.ctx, execCtx.workingDir, args.Command, perCallTimeout)}

	case "process_status":
		if !execCtx.allowCommands {
			return toolExecutionResult{output: "Error: process management is disabled for this run."}
		}
		args, err := decodeArguments[struct {
			ProcessID string `json:"process_id"`
		}](rawArgs)
		if err != nil {
			return argumentFailure(err)
		}
		if args.ProcessID == "" {
			return toolExecutionResult{output: execCtx.processManager.FormatProcessTable()}
		}
		proc, output, statusErr := execCtx.processManager.Status(args.ProcessID)
		if statusErr != nil {
			return toolExecutionResult{output: statusErr.Error()}
		}
		status := "RUNNING"
		if proc.Exited {
			status = fmt.Sprintf("EXITED (%d)", proc.ExitCode)
		}
		return toolExecutionResult{output: fmt.Sprintf("Process %s (PID %d): %s\nCommand: %s\nOutput:\n%s", proc.ID, proc.PID, status, proc.Command, output)}

	case "kill_process":
		if !execCtx.allowCommands {
			return toolExecutionResult{output: "Error: process management is disabled for this run."}
		}
		args, err := decodeArguments[struct {
			ProcessID string `json:"process_id"`
		}](rawArgs)
		if err != nil {
			return argumentFailure(err)
		}
		if err := execCtx.processManager.Kill(args.ProcessID); err != nil {
			return toolExecutionResult{output: fmt.Sprintf("Error killing process %s: %v", args.ProcessID, err)}
		}
		return toolExecutionResult{output: fmt.Sprintf("Process %s terminated.", args.ProcessID)}

	case "submit_plan":
		args, err := decodeArguments[struct {
			Plan string `json:"plan"`
		}](rawArgs)
		if err != nil {
			return argumentFailure(err)
		}
		plan := strings.TrimSpace(args.Plan)
		if plan == "" {
			return toolExecutionResult{output: "Error: plan is empty. Call submit_plan with the full markdown plan."}
		}
		return toolExecutionResult{output: "Plan submitted for the user's review.", taskFinished: true, finalResponse: plan, proposedPlan: plan}

	case "finish_task":
		args, err := decodeArguments[struct {
			Summary string `json:"summary"`
			Answer  string `json:"answer"`
		}](rawArgs)
		if err != nil {
			return argumentFailure(err)
		}
		summary := args.Summary
		if args.Answer != "" {
			summary = fmt.Sprintf("%s\n\nAnswer: %s", summary, args.Answer)
		}
		return toolExecutionResult{output: "Task completed successfully.", taskFinished: true, finalResponse: summary}

	default:
		return toolExecutionResult{output: fmt.Sprintf("Error: unknown tool %q", name)}
	}
}
