package harness

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"

	"fastllm/internal/gitrepo"
	"fastllm/internal/llm"
)

// PromptContext is everything the system prompt is built from. BuildSystemPrompt
// is the only place these are combined, so every entry point (one-shot run,
// line-mode REPL, TUI) sends the model the same structure.
//
// Sections render from most to least stable. Provider prefix caches (vLLM's
// automatic prefix caching, Anthropic prompt caching) reuse only an unchanged
// prefix, so text that changes per turn -- mode, sandbox, a one-turn skill --
// belongs at the end, where it invalidates as little as possible. The end is also
// where a model attends most reliably, which suits the mode constraints.
type PromptContext struct {
	// Base is the identity and operating rules. Empty means DefaultSystemPrompt.
	Base        string
	Skills      []Skill
	Rules       []RuleFile
	Environment Environment
	// TurnExtra holds instructions that apply to this turn only, such as an
	// invoked skill.
	TurnExtra string
	Sandbox   string
	Mode      string
}

// BuildSystemPrompt renders the sections of pc in cache order, skipping empty ones.
func BuildSystemPrompt(pc PromptContext) string {
	base := strings.TrimSpace(pc.Base)
	if base == "" {
		base = DefaultSystemPrompt
	}
	sections := []string{
		base,
		formatSkillCatalogPrompt(pc.Skills),
		FormatRulesForPrompt(pc.Rules),
		pc.Environment.format(),
		pc.TurnExtra,
		pc.Sandbox,
		pc.Mode,
	}
	var b strings.Builder
	for _, section := range sections {
		section = strings.TrimSpace(section)
		if section == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(section)
	}
	return b.String()
}

// Environment is the machine and repository state the model cannot see on its
// own. Without it a model guesses the shell (and writes bash on Windows) or the
// date (and reasons from its training cutoff).
type Environment struct {
	WorkingDir    string
	Platform      string
	Shell         string
	Date          string
	GitBranch     string
	GitDirty      int
	IsGitRepo     bool
	Model         string
	ContextWindow int
}

// CollectEnvironment gathers the environment for dir. Git state is best effort:
// a missing git binary or a slow repository leaves the fields empty rather than
// delaying the turn.
func CollectEnvironment(dir, model string, contextWindow int) Environment {
	env := Environment{
		WorkingDir:    dir,
		Platform:      runtime.GOOS + "/" + runtime.GOARCH,
		Shell:         commandShell(),
		Date:          time.Now().Format("2006-01-02"),
		Model:         model,
		ContextWindow: contextWindow,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	status, err := gitrepo.GetRepoStatus(ctx, dir)
	if err != nil || !status.IsRepo {
		return env
	}
	env.IsGitRepo = true
	env.GitBranch = status.Branch
	env.GitDirty = len(status.Files)
	return env
}

// commandShell names the shell run_command uses (see execution/local.go). On
// Windows that is Windows PowerShell 5.1, which lacks && and ||; saying so up
// front saves the model a failed command per session.
func commandShell() string {
	if runtime.GOOS == "windows" {
		return "Windows PowerShell 5.1 (use ';' to chain commands; '&&' and '||' are not supported)"
	}
	return "sh"
}

func (e Environment) format() string {
	if e.WorkingDir == "" && e.Platform == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("<environment>\n")
	field := func(name, value string) {
		if value != "" {
			fmt.Fprintf(&b, "%s: %s\n", name, value)
		}
	}
	field("working_directory", e.WorkingDir)
	field("platform", e.Platform)
	field("shell", e.Shell)
	field("date", e.Date)
	if e.IsGitRepo {
		state := "clean"
		if e.GitDirty > 0 {
			state = fmt.Sprintf("%d changed file(s)", e.GitDirty)
		}
		field("git", fmt.Sprintf("branch %s, %s", e.GitBranch, state))
	} else if e.WorkingDir != "" {
		field("git", "not a repository")
	}
	field("model", e.Model)
	if e.ContextWindow > 0 {
		field("context_window", fmt.Sprintf("%d tokens", e.ContextWindow))
	}
	b.WriteString("</environment>")
	return b.String()
}

// replayMessages converts prior turns into model messages. System messages are
// dropped (the system prompt is rebuilt every run), and tool traffic is kept
// only in matched pairs: an OpenAI-compatible endpoint rejects a tool result
// with no preceding call, and a call with no result, outright.
func replayMessages(prior []InitialMessage) []llm.Message {
	calls, results := map[string]bool{}, map[string]bool{}
	for _, m := range prior {
		switch m.Role {
		case "assistant":
			for _, call := range m.ToolCalls {
				calls[call.ID] = true
			}
		case "tool":
			results[m.ToolCallID] = true
		}
	}
	messages := make([]llm.Message, 0, len(prior))
	for _, m := range prior {
		switch m.Role {
		case "user":
			if strings.TrimSpace(m.Content) != "" {
				messages = append(messages, llm.Message{Role: m.Role, Content: m.Content})
			}
		case "assistant":
			var kept []llm.ToolCall
			for _, call := range m.ToolCalls {
				if results[call.ID] {
					kept = append(kept, call)
				}
			}
			if strings.TrimSpace(m.Content) != "" || len(kept) > 0 {
				messages = append(messages, llm.Message{Role: m.Role, Content: m.Content, ToolCalls: kept})
			}
		case "tool":
			if calls[m.ToolCallID] {
				messages = append(messages, llm.Message{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID})
			}
		}
	}
	return messages
}

// runTranscript is the conversation a run leaves behind for the next turn: every
// non-system message, plus the final answer, which the loop reports rather than
// appends.
func runTranscript(messages []llm.Message, finalResponse string) []llm.Message {
	transcript := make([]llm.Message, 0, len(messages)+1)
	for _, m := range messages {
		if m.Role != "system" {
			transcript = append(transcript, m)
		}
	}
	if strings.TrimSpace(finalResponse) != "" {
		transcript = append(transcript, llm.Message{Role: "assistant", Content: finalResponse})
	}
	return transcript
}
