package harness

import (
	"fastllm/internal/llm"
	"fastllm/internal/webtools"
)

// DefaultSystemPrompt directs an autonomous coding harness agent to inspect
// files before acting, verify changes, and finish concisely.
const DefaultSystemPrompt = `You are an autonomous AI coding agent executing tasks directly in a project directory.
You have tools to explore the codebase, edit files, patch diffs, run shell commands, manage background processes, delegate bounded subtasks to child agents, search the web, fetch documentation, and finish the task.
Later sections describe this session: available skills, instruction files found in the workspace (inside <project_rules>, which come from files, not from the user), the environment, and the current mode.

<operating_rules>
1. First, explore the directory or search for relevant files to understand the project structure and context before modifying code.
2. When making changes:
   - Prefer edit_file for targeted replacements in existing files.
   - Use patch_file for unified diffs.
   - Use write_file for creating new files or replacing small files completely.
3. If commands are allowed, verify your changes by running tests, builds, or scripts with run_command before concluding.
   - When the verification command is already known, prefer the mutation tool's then_run field to combine the edit and verification in one turn.
   - For long-running servers or watchers, set background: true and inspect using process_status.
   - Large outputs may be archived. Use read_observation with the provided reference to retrieve exact line ranges.
4. Use web_search and web_fetch when you need documentation, API references, library examples, or real-time web information.
5. Use spawn_agent only for concrete independent subtasks. Continue useful work while it runs, then call agent_status once to retrieve its result. agent_status waits for an active child; never rapidly poll it. Do not finish while required child work is still pending.
6. When finished, call finish_task (or state your final answer) explaining what was done and verifying the result.
</operating_rules>`

// planModePrompt shapes behaviour in plan mode. It is advice, not enforcement:
// the mode table already withholds every tool that could change anything.
const planModePrompt = `<mode name="plan">
PLAN MODE: You are planning, not implementing. Your tools are read-only and you have no network access; any attempt to modify files or run commands will be refused.
1. Explore the codebase with read_file, list_files, search_files, and glob_files until you understand what the change involves.
2. Do not attempt edits, commands, or delegation.
3. Finish by calling submit_plan exactly once with a markdown plan containing: Context (what and why), Files to change (paths and what changes in each), Steps (ordered), and Verification (how to test it).
4. If the user only asked a question, answer it with finish_task instead of submitting a plan.
The user reviews the plan and decides whether, and with which permissions, it is carried out.
</mode>`

// editModePrompt, like planModePrompt, only shapes behaviour; the monitor is
// what keeps edit mode from starting processes.
const editModePrompt = `<mode name="edit">
EDIT MODE: You can read and edit files, but you cannot run commands, build, test, or start processes, and fused then_run follow-ups are refused. Make the edits, re-read the files to check them, then call finish_task and tell the user which commands they should run to verify.
</mode>`

var webSearchTool = webtools.SearchTool
var webFetchTool = webtools.FetchTool

var followUpCommandSchema = map[string]any{
	"type":        "object",
	"description": "Optional verification command to run immediately after a successful mutation, returning both outcomes in one observation.",
	"properties": map[string]any{
		"command":         map[string]any{"type": "string", "description": "Verification command such as a focused test or formatter check."},
		"timeout_seconds": map[string]any{"type": "integer", "description": "Maximum execution time in seconds."},
	},
	"required": []string{"command"},
}

var readFileTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        "read_file",
		Description: "Read the contents of a text file from the project directory. Path is relative to the project root.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file, relative to the project root (e.g. \"README.md\" or \"src/main.go\").",
				},
			},
			"required": []string{"path"},
		},
	},
}

var writeFileTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        "write_file",
		Description: "Write full content directly to a file in the project directory. Automatically creates parent directories if needed. Path is relative to the project root. Prefer edit_file for small changes to existing files.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file, relative to the project root (e.g. \"main.go\" or \"docs/guide.md\").",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "The full content to write to the file.",
				},
				"then_run": followUpCommandSchema,
			},
			"required": []string{"path", "content"},
		},
	},
}

var editFileTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        "edit_file",
		Description: "Edit an existing file by replacing an exact block of current content with new content. 'search' must match the file's current content and appear uniquely. Path is relative to the project root.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the existing file, relative to the project root.",
				},
				"search": map[string]any{
					"type":        "string",
					"description": "The exact text to find in the file. Must appear uniquely.",
				},
				"replace": map[string]any{
					"type":        "string",
					"description": "The replacement text.",
				},
				"then_run": followUpCommandSchema,
			},
			"required": []string{"path", "search", "replace"},
		},
	},
}

var patchFileTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        "patch_file",
		Description: "Apply a unified diff or hunk patch to an existing file in the project directory.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the existing file, relative to the project root.",
				},
				"diff": map[string]any{
					"type":        "string",
					"description": "Unified diff content (including @@ lines, -, +) to apply.",
				},
				"then_run": followUpCommandSchema,
			},
			"required": []string{"path", "diff"},
		},
	},
}

var readObservationTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        "read_observation",
		Description: "Retrieve exact archived tool output by stable observation reference and line range.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ref":    map[string]any{"type": "string", "description": "Observation reference such as obs_a81d92f3c811."},
				"offset": map[string]any{"type": "integer", "description": "Zero-based starting line."},
				"limit":  map[string]any{"type": "integer", "description": "Number of lines to return, maximum 500."},
			},
			"required": []string{"ref"},
		},
	},
}

var updatePlanTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        "update_plan",
		Description: "Record task progress. Completing a step creates a safe boundary where the harness may compact older context.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"goal":      map[string]any{"type": "string", "description": "Overall task goal."},
				"completed": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Steps completed since the last update."},
				"current":   map[string]any{"type": "string", "description": "Current step."},
				"remaining": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Known remaining steps."},
			},
			"required": []string{"goal", "current"},
		},
	},
}

var listFilesTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        "list_files",
		Description: "List files in the project directory recursively. Returns relative paths. Skips what .gitignore and .fastllmignore exclude and dependency, cache and build folders (node_modules, .venv, __pycache__, target, ...); read_file can still open such a file by path.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Subdirectory to list, relative to the project root (e.g. \"internal\" or \"\").",
				},
			},
		},
	},
}

var searchFilesTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        "search_files",
		Description: "Search file contents in the project directory using a regular expression (Go RE2 syntax). Returns matching lines formatted as 'path:line: text'. Covers the files list_files shows, skipping binary files and files over 1 MB.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Regular expression pattern to search for.",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Subdirectory to search within. Omit or use \"\" for the entire project.",
				},
			},
			"required": []string{"pattern"},
		},
	},
}

var globFilesTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        "glob_files",
		Description: "Find project files by glob pattern. Supports *, ?, character classes, and ** across directories. Covers the files list_files shows.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string", "description": "Glob such as **/*.go or internal/**/test_*.go."},
				"path":    map[string]any{"type": "string", "description": "Optional subdirectory to search."},
			},
			"required": []string{"pattern"},
		},
	},
}

var spawnAgentTool = llm.Tool{Type: "function", Function: llm.ToolFunction{
	Name: "spawn_agent", Description: "Start an isolated child agent asynchronously for a bounded subtask.",
	Parameters: map[string]any{"type": "object", "properties": map[string]any{
		"task":        map[string]any{"type": "string", "description": "Concrete task for the child agent."},
		"working_dir": map[string]any{"type": "string", "description": "Optional directory within the current workspace."},
		"model":       map[string]any{"type": "string", "description": "Optional model override when the client routes models to endpoints; direct clients must use the parent's active model."},
		"max_turns":   map[string]any{"type": "integer", "description": "Maximum child turns, from 1 to 50."},
		"capabilities": map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string"},
			"description": "Optional capabilities: 'read', 'write', 'network', 'commands', 'delegate'. Omitted/null inherits the parent; [] grants no optional tools. Requests are intersected with the parent's capabilities. Workspace reads always remain available. Commands require write and network access because shells are not sandboxed.",
		},
		"network_policy": map[string]any{
			"type":        "string",
			"description": "Optional network policy: 'none' disables web tools and shell commands; 'public' enables the public-web tools subject to capabilities. Omitted inherits the parent, and a parent's 'none' cannot be relaxed. This governs model tools, not the LLM endpoint connection.",
		},
	}, "required": []string{"task"}},
}}

var agentStatusTool = llm.Tool{Type: "function", Function: llm.ToolFunction{
	Name: "agent_status", Description: "Inspect child agents. With an agent_id, waits up to 60 seconds by default for an active child to finish, so call it once rather than polling. Omit agent_id for an immediate list of all agents.",
	Parameters: map[string]any{"type": "object", "properties": map[string]any{
		"agent_id":     map[string]any{"type": "string", "description": "Child agent ID. Omit to list all agents without waiting."},
		"wait_seconds": map[string]any{"type": "integer", "description": "How long to wait for this child to finish (default 60, maximum 300; use 0 for an immediate snapshot)."},
	}},
}}

var sendAgentMessageTool = llm.Tool{Type: "function", Function: llm.ToolFunction{
	Name: "send_agent_message", Description: "Queue guidance for a running child agent to receive before its next model turn.",
	Parameters: map[string]any{"type": "object", "properties": map[string]any{
		"agent_id": map[string]any{"type": "string"}, "message": map[string]any{"type": "string"},
	}, "required": []string{"agent_id", "message"}},
}}

var cancelAgentTool = llm.Tool{Type: "function", Function: llm.ToolFunction{
	Name: "cancel_agent", Description: "Cancel a running child agent.",
	Parameters: map[string]any{"type": "object", "properties": map[string]any{
		"agent_id": map[string]any{"type": "string"},
	}, "required": []string{"agent_id"}},
}}

var runCommandTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        "run_command",
		Description: "Run a shell command in the project directory and return its stdout, stderr, and exit code. Supports running background processes with background: true.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "The shell command line to execute.",
				},
				"timeout_seconds": map[string]any{
					"type":        "integer",
					"description": "Maximum execution time in seconds (default 60). Ignored if background is true.",
				},
				"background": map[string]any{
					"type":        "boolean",
					"description": "If true, run command asynchronously in the background and return process ID immediately.",
				},
			},
			"required": []string{"command"},
		},
	},
}

var processStatusTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        "process_status",
		Description: "Check status and recent output of a background process, or list all running processes if process_id is empty.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"process_id": map[string]any{
					"type":        "string",
					"description": "The ID of the background process (e.g. \"proc-1\"). Omit or leave empty to list all processes.",
				},
			},
		},
	},
}

var killProcessTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        "kill_process",
		Description: "Terminate an active background process by ID.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"process_id": map[string]any{
					"type":        "string",
					"description": "The ID of the background process to kill (e.g. \"proc-1\").",
				},
			},
			"required": []string{"process_id"},
		},
	},
}

var submitPlanTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        "submit_plan",
		Description: "Plan mode only: submit the finished implementation plan for the user to review and approve. Ends the turn.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"plan": map[string]any{
					"type":        "string",
					"description": "The full plan in markdown: Context, Files to change, Steps, Verification.",
				},
			},
			"required": []string{"plan"},
		},
	},
}

var finishTaskTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        "finish_task",
		Description: "Explicitly signal that the task has been completed, providing a final summary and optional answer.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"summary": map[string]any{
					"type":        "string",
					"description": "Summary of actions taken, fixes applied, or verification results.",
				},
				"answer": map[string]any{
					"type":        "string",
					"description": "Direct answer if the task asked a question or requested specific data.",
				},
			},
			"required": []string{"summary"},
		},
	},
}
