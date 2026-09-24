package harness

import (
	"time"

	"fastllm/internal/llm"
)

// RunRequest configures a single autonomous task run in the harness.
type RunRequest struct {
	// Task is the prompt or instruction the model must execute.
	Task string `json:"task"`

	// WorkingDir is the directory the harness operates within (sandboxed root).
	// Defaults to the runner's default working directory if empty.
	WorkingDir string `json:"working_dir,omitempty"`

	// Model is the model identifier to use (e.g. "llama3.1", "gemma-4-31b-it").
	// Defaults to the runner's default chat model if empty.
	Model string `json:"model,omitempty"`

	// SystemPrompt is an optional system prompt override. If empty,
	// DefaultSystemPrompt is used.
	SystemPrompt string `json:"system_prompt,omitempty"`

	// PromptExtra adds instructions for this run only, such as an invoked skill.
	// It is placed near the end of the system prompt, after the stable sections.
	PromptExtra string `json:"-"`

	// MaxTurns is the maximum number of tool execution rounds allowed.
	// Defaults to 20 if <= 0.
	MaxTurns int `json:"max_turns,omitempty"`

	// AllowCommands controls whether the run_command tool is offered.
	// Defaults to true.
	AllowCommands bool `json:"allow_commands"`
	// CommandsConfigured distinguishes an explicit interactive "off" from the
	// zero value used by older non-interactive callers.
	CommandsConfigured bool `json:"-"`

	// Sandbox runs every command in the platform's isolated backend. It is
	// opt-in, and where no isolated backend exists the run fails rather than
	// running commands unsandboxed.
	Sandbox bool `json:"sandbox,omitempty"`

	// CommandTimeout is the maximum execution time for each run_command invocation.
	// Defaults to 60 seconds if <= 0.
	CommandTimeout time.Duration `json:"command_timeout,omitempty"`

	// ThinkLevel optionally sets the reasoning effort level (e.g. "low", "medium", "high")
	// for models/providers that support it.
	ThinkLevel string `json:"think_level,omitempty"`

	// ResumeSession restores an interactive session by ID, or "last" for the newest.
	ResumeSession string `json:"resume_session,omitempty"`

	// ConversationID optionally links the run to a persistent conversation thread in the database.
	ConversationID int64 `json:"conversation_id,omitempty"`

	// InitialMessages provides previous conversation turns for multi-turn context.
	InitialMessages []InitialMessage `json:"initial_messages,omitempty"`

	// AgentDepth and AgentInbox are internal orchestration state. They are not
	// part of the public API payload.
	AgentDepth     int            `json:"-"`
	AgentInbox     <-chan string  `json:"-"`
	PermissionMode PermissionMode `json:"permission_mode,omitempty"`
	// Authorize answers the monitor's Ask decisions (agent mode) by asking the
	// user, or by a session grant whose scope covers the request. Nil means
	// nobody can be asked, and every Ask is denied.
	Authorize func(ConsentRequest) bool `json:"-"`
	// Audit receives every monitor decision in this run and its children.
	Audit *AuditLog `json:"-"`
	// Taint records secret files this conversation has read (I10). Share one
	// across turns of a session; nil gets a fresh one per run.
	Taint *SessionTaint `json:"-"`
	// Budget limits what the run and its child agents may consume
	// (budget.go); zero fields take the defaults. meter is the shared count.
	Budget Budget       `json:"budget,omitempty"`
	meter  *budgetMeter `json:"-"`
	// Journal records model file writes so /undo can revert them; nil means
	// no undo. Share one across a session's runs.
	Journal *WriteJournal `json:"-"`

	// StreamTokens makes the run emit EventTokenDelta as assistant text arrives,
	// instead of only the finished answer at turn end. Opt-in: a token-rate event
	// stream is useful to a live UI and pure overhead to a batch caller, and it
	// has no effect when the configured client cannot stream.
	StreamTokens bool `json:"-"`

	// Capabilities defines an explicit capability list for the run (attenuation).
	// Nil preserves defaults (or inherits the parent's list when spawning).
	// An empty list grants no optional tools; workspace reads remain available.
	// Shell commands also require write and network capabilities.
	Capabilities []string `json:"capabilities"`

	// NetworkPolicy controls model tools: "none" disables web tools and shells;
	// "public" permits public-web tools, subject to Capabilities. LLM transport
	// is unaffected. Omitted child policies inherit the parent's restriction.
	NetworkPolicy string `json:"network_policy,omitempty"`
}

// InitialMessage represents a prior conversation turn.
type InitialMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ToolCalls and ToolCallID carry prior tool use, so a replayed turn still
	// shows the model which files it read and which commands it ran.
	ToolCalls  []llm.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

// ToolCallRecord records a single tool invocation and its returned output.
type ToolCallRecord struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Result    string `json:"result"`
}

// TurnRecord captures what happened during one model-turn.
type TurnRecord struct {
	Turn      int              `json:"turn"`
	ToolCalls []ToolCallRecord `json:"tool_calls,omitempty"`
	Response  string           `json:"response,omitempty"`
	Metrics   *TurnMetrics     `json:"metrics,omitempty"`
}

// RunResult is the final output of an autonomous harness task run.
type RunResult struct {
	Success       bool            `json:"success"`
	Task          string          `json:"task"`
	WorkingDir    string          `json:"working_dir"`
	Model         string          `json:"model"`
	Turns         int             `json:"turns"`
	FinalResponse string          `json:"final_response"`
	History       []TurnRecord    `json:"history"`
	DurationMS    int64           `json:"duration_ms"`
	Metrics       *SessionMetrics `json:"metrics,omitempty"`
	Error         string          `json:"error,omitempty"`
	// ProposedPlan is set when a plan-mode run ends with submit_plan. It is a
	// proposal only: carrying it out needs the user to choose a mode.
	ProposedPlan string `json:"proposed_plan,omitempty"`
	// PermissionMode is the mode the run was actually evaluated under.
	PermissionMode PermissionMode `json:"permission_mode,omitempty"`
	// Transcript is the conversation after the run, without the system prompt:
	// replayed history, the task, tool calls and results (as compacted during
	// the run), and the final answer. A multi-turn caller replays it next turn.
	Transcript []llm.Message `json:"-"`
}

// EventType distinguishes streamable harness progress events.
type EventType string

const (
	EventConversation EventType = "conversation"
	EventTurnStart    EventType = "turn_start"
	EventToolCall     EventType = "tool_call"
	EventToolResult   EventType = "tool_result"
	EventTurnComplete EventType = "turn_complete"
	EventTaskFinished EventType = "task_finished"
	// EventTokenDelta carries assistant text as it streams in, before the turn
	// completes. Response holds the incremental fragment, not the whole answer.
	EventTokenDelta EventType = "token_delta"
	// EventTokenDiscard tells the consumer to drop every EventTokenDelta shown
	// for the current turn: the text was a tool call written as prose, or the
	// stream failed and is about to be retried from the beginning.
	EventTokenDiscard EventType = "token_discard"
	// EventPlanProposed carries a plan-mode run's submit_plan text in Response.
	EventPlanProposed EventType = "plan_proposed"
)

// Event is emitted in real time during a harness run.
type Event struct {
	Type           EventType       `json:"type"`
	ConversationID int64           `json:"conversation_id,omitempty"`
	Turn           int             `json:"turn,omitempty"`
	ToolCall       *ToolCallRecord `json:"tool_call,omitempty"`
	Response       string          `json:"response,omitempty"`
	Metrics        *TurnMetrics    `json:"metrics,omitempty"`
	Result         *RunResult      `json:"result,omitempty"`
	Error          string          `json:"error,omitempty"`
}
