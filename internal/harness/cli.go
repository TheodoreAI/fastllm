package harness

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"fastllm/internal/config"
	"fastllm/internal/llm"
)

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// RunCLI parses CLI flags and executes an autonomous harness task.
// Returns the exit code (0 for success, 1 for failure).
func RunCLI(args []string) int {
	initConsole()

	fs := flag.NewFlagSet("harness", flag.ContinueOnError)

	taskFlag := fs.String("task", "", "Task instruction for the agent to execute")
	dirFlag := fs.String("dir", ".", "Target working directory (sandboxed root)")
	modelFlag := fs.String("model", "", "Model name (defaults to config default_model or llama3.1)")
	urlFlag := fs.String("url", "", "LLM API base URL (defaults to model endpoint URL or http://localhost:11434/v1)")
	keyFlag := fs.String("key", "", "LLM API key (defaults to LLM_API_KEY)")
	maxTurnsFlag := fs.Int("max-turns", 20, "Maximum tool execution turns")
	timeoutFlag := fs.Int("timeout", 60, "Command timeout in seconds for run_command")
	systemFlag := fs.String("system", "", "Custom system prompt override")
	thinkFlag := fs.String("think", "", "Reasoning effort level (e.g. low, medium, high)")
	noCmdsFlag := fs.Bool("no-commands", false, "Disable the run_command tool")
	jsonFlag := fs.Bool("json", false, "Output only the final RunResult JSON")
	quietFlag := fs.Bool("quiet", false, "Suppress turn-by-turn progress output")
	obsFlag := fs.Bool("on-observations", false, "Archive large tool output and send the model an evidence receipt or packed handle instead of the full text")
	resumeFlag := fs.String("resume", "", "Resume an interactive session by ID, or 'last'")
	importFlag := fs.Bool("import-conversations", false, "Import conversations from the legacy web database into the TUI session store, then exit")

	if err := fs.Parse(args); err != nil {
		return 1
	}

	task := strings.TrimSpace(*taskFlag)
	if task == "" && fs.NArg() > 0 {
		task = strings.Join(fs.Args(), " ")
	}

	workDir, err := filepath.Abs(*dirFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving working dir %q: %v\n", *dirFlag, err)
		return 1
	}

	// Load settings from .fastllm/config.json or ~/.fastllm/config.json
	settings, _, _ := config.LoadSettings(workDir)

	model := strings.TrimSpace(*modelFlag)
	if model == "" {
		if env := os.Getenv("LLM_CHAT_MODEL"); env != "" {
			model = env
		} else if settings != nil && settings.DefaultModel != "" {
			model = settings.DefaultModel
		} else {
			model = "llama3.1"
		}
	}

	if *importFlag {
		return runConversationImport(workDir, model, *maxTurnsFlag, time.Duration(*timeoutFlag)*time.Second, !*noCmdsFlag, *thinkFlag)
	}

	baseURL := strings.TrimSpace(*urlFlag)
	apiKey := strings.TrimSpace(*keyFlag)

	// If model matches a configured endpoint, apply its URL and APIKey if not explicitly passed
	if settings != nil {
		if matched := settings.FindModel(model); matched != nil {
			if baseURL == "" && os.Getenv("LLM_BASE_URL") == "" && matched.URL != "" {
				baseURL = matched.URL
			}
			if apiKey == "" && os.Getenv("LLM_API_KEY") == "" && matched.ResolveAPIKey() != "" {
				apiKey = matched.ResolveAPIKey()
			}
		}
	}

	if baseURL == "" {
		baseURL = getenv("LLM_BASE_URL", "http://localhost:11434/v1")
	}
	if apiKey == "" {
		apiKey = getenv("LLM_API_KEY", "")
	}

	// Route cluster-style model names at whatever endpoint this machine has
	// configured for the "selfhosted" provider. There is deliberately no built-in
	// address: a forwarded port is meaningful only on the machine that forwarded it.
	selfHostedEndpoint := settings.FindByProvider("selfhosted")
	isSelfHosted := strings.HasPrefix(model, "selfhosted:") || strings.HasPrefix(model, "cluster:") ||
		(selfHostedEndpoint != nil && selfHostedEndpoint.ID == model)

	if isSelfHosted {
		provider := config.ResolveProvider(settings, "selfhosted")
		explicitURL := strings.TrimSpace(*urlFlag) != "" || os.Getenv("LLM_BASE_URL") != ""
		if !explicitURL && !provider.Configured() {
			// Falling through here would quietly dial the default local endpoint,
			// so a request for a self-hosted model would surface as "connection
			// refused" against Ollama rather than as the real problem.
			fmt.Fprintf(os.Stderr, "Error: no endpoint is configured for provider %q.\n", "selfhosted")
			fmt.Fprintf(os.Stderr, "Add a model entry with \"provider\": \"selfhosted\" to your fastllm config,\n")
			fmt.Fprintf(os.Stderr, "set SELFHOSTED_LLM_BASE_URL, or pass -url explicitly.\n")
			return 1
		}
		if !explicitURL {
			baseURL = provider.BaseURL
		}
		if apiKey == "" {
			apiKey = provider.APIKey
		}
		model = strings.TrimPrefix(strings.TrimPrefix(model, "selfhosted:"), "cluster:")
	}

	client := llm.New(baseURL, apiKey, model, "")
	matched := settings.FindModel(model)
	// "think" is Ollama's own extension; sending it to a server that does not know
	// it can 400 the whole request, so this is opt-in via config with port-based
	// detection as the fallback.
	client.SendThink = config.ShouldSendThink(matched, baseURL)
	if matched != nil {
		applyModelParameters(client, matched.Parameters)
	}

	// Wrap the direct client in a router so provider-tagged endpoints reach the
	// native Anthropic/Gemini clients. Those speak their own wire formats —
	// Gemini's thought signatures, for one — which an OpenAI-compatible request
	// cannot carry, so without this the agent loop breaks on the second turn of
	// any tool-using conversation.
	router := llm.NewRouter(client, CloudConfigFromSettings(settings))

	runner := NewRunner(router, workDir, model)
	runner.EnableObservations = *obsFlag
	// Route the startup model too, not just later /model switches.
	if matched != nil {
		if err := runner.SwitchModel(matched); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
		}
	}

	req := RunRequest{
		Task:           task,
		WorkingDir:     workDir,
		Model:          model,
		SystemPrompt:   *systemFlag,
		MaxTurns:       *maxTurnsFlag,
		AllowCommands:  !*noCmdsFlag,
		CommandTimeout: time.Duration(*timeoutFlag) * time.Second,
		ThinkLevel:     *thinkFlag,
		ResumeSession:  strings.TrimSpace(*resumeFlag),
	}

	// If no task was specified, launch the interactive TUI REPL
	if task == "" || task == "tui" || task == "interactive" {
		if err := runner.RunInteractive(req); err != nil {
			fmt.Fprintf(os.Stderr, "Interactive session error: %v\n", err)
			return 1
		}
		return 0
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Fprintln(os.Stderr, "\nReceived interrupt signal. Canceling task...")
		cancel()
	}()

	if !*jsonFlag && !*quietFlag {
		cmdStr := ColorGreen("enabled")
		if *noCmdsFlag {
			cmdStr = ColorYellow("disabled")
		}
		lines := []string{
			"",
			FormatKV("task", task, 10),
			FormatKV("directory", workDir, 10),
			FormatKV("model", model, 10),
			FormatKV("endpoint", baseURL, 10),
			FormatKV("commands", cmdStr, 10),
			"",
		}
		fmt.Println(FormatCard("fastllm "+SymDot+" autonomous agent task", lines, 74))
	}

	onEvent := func(ev Event) {
		if *jsonFlag || *quietFlag {
			return
		}
		switch ev.Type {
		case EventTurnStart:
			// Turn started
		case EventToolCall:
			if ev.ToolCall != nil {
				fmt.Println(FormatToolCall(ev.ToolCall.Name, summarizeArgs(ev.ToolCall.Arguments)))
			}
		case EventToolResult:
			if ev.ToolCall != nil {
				fmt.Println(FormatToolResult(ev.ToolCall.Name, ev.ToolCall.Result, 4))
			}
		case EventTurnComplete:
			if ev.Metrics != nil {
				fmt.Println(FormatTurnSummary(*ev.Metrics))
			}
		}
	}

	result, err := runner.Run(ctx, req, onEvent)

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		if err != nil || (result != nil && !result.Success) {
			return 1
		}
		return 0
	}

	if !*quietFlag {
		statusVal := ColorGreen(SymCheck + " SUCCESS")
		if result == nil || !result.Success {
			statusVal = ColorRed(SymCross + " FAILED")
		}

		var lines []string
		lines = append(lines, "")
		lines = append(lines, FormatKV("status", statusVal, 10))

		if result != nil {
			lines = append(lines, FormatKV("turns", fmt.Sprintf("%d", result.Turns), 10))
			lines = append(lines, FormatKV("duration", fmt.Sprintf("%.2fs", float64(result.DurationMS)/1000.0), 10))
			if result.Metrics != nil {
				costStr := "$0.0000"
				if result.Metrics.TotalCost > 0 {
					costStr = fmt.Sprintf("$%.4f", result.Metrics.TotalCost)
				}
				tokenSummary := fmt.Sprintf("%d total (%d prompt %s %d completion %s %s)",
					result.Metrics.TotalTokens, result.Metrics.TotalPromptTokens, SymDot,
					result.Metrics.TotalCompletionTokens, SymDot, costStr)
				lines = append(lines, FormatKV("tokens", tokenSummary, 10))
			}
			if result.Error != "" {
				lines = append(lines, FormatKV("error", ColorRed(result.Error), 10))
			}
			if result.FinalResponse != "" {
				lines = append(lines, "")
				lines = append(lines, ColorBrightWhite(StyleBold("Final Response:")))
				for _, respLine := range strings.Split(result.FinalResponse, "\n") {
					lines = append(lines, respLine)
				}
			}
		} else if err != nil {
			lines = append(lines, FormatKV("error", ColorRed(err.Error()), 10))
		}
		lines = append(lines, "")
		fmt.Println("\n" + FormatCard("Task Execution Summary", lines, 74))
	}

	if err != nil || (result != nil && !result.Success) {
		return 1
	}
	return 0
}

func summarizeArgs(rawJSON string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(rawJSON), &m); err != nil {
		if len(rawJSON) > 60 {
			return rawJSON[:60] + "..."
		}
		return rawJSON
	}
	parts := make([]string, 0, len(m))
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := m[k]
		if k == "content" || k == "replace" {
			str, _ := v.(string)
			parts = append(parts, fmt.Sprintf("%s=[%d bytes]", k, len(str)))
		} else {
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		}
	}
	return strings.Join(parts, " ")
}

// runConversationImport drains the legacy web database into the TUI session
// store. It is a migration path, not a sync: imports are idempotent, so running
// it again after using the web UI brings across only what is new.
func runConversationImport(workDir, model string, maxTurns int, timeout time.Duration, allowCommands bool, thinkLevel string) int {
	if OpenLegacyConversations == nil {
		fmt.Fprintln(os.Stderr, "This build cannot read the legacy database.")
		return 1
	}
	src, closeSrc, err := OpenLegacyConversations()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot open the legacy database: %v\n", err)
		return 1
	}
	if closeSrc != nil {
		defer func() { _ = closeSrc() }()
	}

	store, err := DefaultSessionStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot open the session store: %v\n", err)
		return 1
	}

	runtime := InteractiveRuntime{
		MaxTurns:       maxTurns,
		CommandTimeout: timeout,
		ThinkLevel:     thinkLevel,
		AllowCommands:  allowCommands,
		PermissionMode: PermissionAsk,
	}
	report, err := ImportLegacyConversations(src, store, workDir, model, runtime)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Import failed after %s: %v\n", report.String(), err)
		return 1
	}
	fmt.Printf("%s Conversation import: %s\n", SymCheck, report.String())
	if len(report.Imported) > 0 {
		fmt.Println(ColorGray("  Open them with /sessions and /resume <id> in the TUI."))
	}
	return 0
}
