package harness

import (
	"sort"
	"strings"
	"time"
)

// commandSections is the one list of slash commands. /help renders it, the
// TUI suggestion dropdown and the legacy REPL completion derive from it, so a
// command added here is documented and suggestible everywhere at once.
//
// Usage patterns carry meaning: "<x>" is a required argument, "[x]" optional,
// "/a, /b" lists aliases. Rows that are not slash commands (!<cmd>, Ctrl+B)
// appear in /help only.
type commandSection struct {
	Title string
	Rows  []commandRow
}

type commandRow struct {
	Usage string
	Desc  string
}

var commandSections = []commandSection{
	{"Session & Control", []commandRow{
		{"/help", "Display this command reference"},
		{"/status", "Inspect session token usage, latency, cost, and jobs"},
		{"/sessions", "Browse, filter, resume, rename, or delete sessions"},
		{"/theme [name]", "Pick a colour theme (nord, zinc, terminal, ...)"},
		{"/sessions list", "Print the saved sessions table"},
		{"/resume [id|last]", "Resume a saved session (no id opens the menu)"},
		{"/session [id]", "Show session details"},
		{"/rename <title>", "Rename the active session"},
		{"/delete-session <id>", "Delete an inactive saved session"},
		{"/new", "Save the current session and start a new one"},
		{"/conversations", "List conversations in the legacy web database"},
		{"/import <id|all>", "Import legacy web conversations as sessions"},
		{"/c, /clear", "Clear conversation context and declutter UI screen"},
		{"/cls", "Clear terminal screen without resetting context"},
		{"/compact", "Collapse older turns into a checkpoint without waiting for the budget"},
		{"/copy, /yank", "Copy last response to OS clipboard (/copy all for full log)"},
		{"/dir <path>", "Switch active working directory and reload workspace rules"},
		{"/trust", "Use this workspace's .fastllm config after reviewing it"},
		{"/untrust", "Stop using this workspace's .fastllm config"},
		{"/exit, /quit", "Exit the interactive session"},
	}},
	{"Runtime Settings", []commandRow{
		{"/set", "Show current session runtime settings"},
		{"/set turns <1-100>", "Set maximum agent tool turns per prompt"},
		{"/set timeout <sec>", "Set command timeout (1-3600 seconds)"},
		{"/set think <level>", "Set off, low, medium, or high reasoning"},
		{"/set commands <on|off>", "Enable or disable command/process tools"},
		{"/set sandbox <on|off>", "Run commands isolated from this machine, with no network"},
		{"/set permissions <mode>", "Set plan, agent, edit, or full (Shift+Tab cycles in the TUI)"},
		{"/set output <mode>", "Set compact or expanded tool results"},
		{"/set tokens <n|off>", "Token budget per prompt, child agents included"},
		{"/set cost <usd|off>", "Estimated cost budget per prompt"},
		{"/set duration <30m|off>", "Wall-clock budget per prompt (default 30m)"},
		{"/permissions [list]", "List active session capability grants"},
		{"/permissions revoke <id>", "Revoke a capability grant by ID or tool name"},
		{"/permissions clear", "Revoke all active session capability grants"},
		{"/audit [n]", "Show the last n permission decisions for this session"},
	}},
	{"Shell & Execution", []commandRow{
		{"/shell, /sh", "Toggle interactive Shell Mode (run host terminal commands)"},
		{"/sh <cmd>", "Execute a shell command in working dir (e.g. /sh ls -la)"},
		{"/edit <file>", "Open file in editor ($EDITOR, or nano/notepad)"},
		{"!<cmd>, $ <cmd>", "Execute command immediately (e.g. !git status, !go test)"},
	}},
	{"Models & Endpoints", []commandRow{
		{"/models, /model", "Open interactive model selector modal (Alt+M)"},
		{"/model <name>", "Switch active model (e.g. /model llama3.1)"},
		{"/models add <id> <url>", "Register a new inference endpoint in config.json"},
	}},
	{"Git & Checkpoints", []commandRow{
		{"/changes [file]", "Inspect changed files list & per-file diff modal (Alt+C / Ctrl+O)"},
		{"/diff [file]", "Syntax-highlighted diff of uncommitted changes (or inspect file)"},
		{"/discard [file|all]", "Discard uncommitted edits for a file or all files ('x' in diff modal)"},
		{"/undo [force]", "Revert the model's file changes from the last prompt (force: even files edited since)"},
		{"/rules", "Inspect discovered workspace instruction files"},
		{"/skills [list|name] [task]", "List, inspect, or run an installed skill"},
	}},
	{"Background Processes & Servers", []commandRow{
		{"<cmd> &", "Launch dev server in background in Shell Mode (e.g. npm run dev &)"},
		{"/bg <cmd>", "Launch command in background (or 'bg <cmd>' in Shell Mode)"},
		{"/logs <id> [lines]", "View recent logs of a background process (or 'logs <id>')"},
		{"/ps", "List running background processes & uptime (or 'ps')"},
		{"/kill <id|all>", "Terminate background process or all processes (e.g. /kill all)"},
		{"Ctrl+B", "Background an already-running shell command without killing it"},
	}},
	{"Web Tools", []commandRow{
		{"/search <query>", "Search the public web (DuckDuckGo / Brave)"},
		{"/fetch <url>", "Fetch and read web page content converted to Markdown"},
	}},
	{"Image Generation", []commandRow{
		{"/image <prompt>", "Generate an image and save it under generated-images/"},
	}},
}

// slashCommand is one suggestible command, derived from its first help row.
type slashCommand struct {
	Name     string
	Aliases  []string
	Usage    string
	Desc     string
	NeedsArg bool
}

// extraAliases are accepted spellings kept out of /help to keep its rows short;
// they still match in suggestions and completion.
var extraAliases = map[string][]string{
	"/theme":       {"/themes"},
	"/edit":        {"/nano", "/vim"},
	"/discard":     {"/revert"},
	"/permissions": {"/permission"},
}

// destructiveCommands never run from a suggestion: Enter only completes them,
// so a half-typed prefix like "/u" cannot roll back the working tree.
var destructiveCommands = map[string]bool{
	"/undo": true, "/c": true, "/exit": true, "/discard": true, "/delete-session": true, "/kill": true,
}

// isDestructive checks a typed name or any of its aliases.
func isDestructive(name string) bool {
	if cmd, ok := findSlashCommand(name); ok {
		return destructiveCommands[cmd.Name]
	}
	return destructiveCommands[strings.ToLower(name)]
}

// slashCommandList derives the commands from commandSections in help order.
// When several rows start with the same command, the first describes it.
func slashCommandList() []slashCommand {
	var out []slashCommand
	seen := map[string]bool{}
	for _, section := range commandSections {
		for _, row := range section.Rows {
			if !strings.HasPrefix(row.Usage, "/") {
				continue
			}
			head, rest, _ := strings.Cut(row.Usage, " ")
			names := []string{head}
			if strings.Contains(row.Usage, ", /") {
				// "/c, /clear" and "/copy, /yank": every comma-separated
				// token is a name, and the row takes no arguments.
				names = nil
				for _, name := range strings.Split(row.Usage, ",") {
					names = append(names, strings.Fields(name)[0])
				}
				rest = ""
			}
			if seen[names[0]] {
				continue
			}
			names = append(names, extraAliases[names[0]]...)
			for _, name := range names {
				seen[name] = true
			}
			out = append(out, slashCommand{
				Name: names[0], Aliases: names[1:], Usage: row.Usage, Desc: row.Desc,
				NeedsArg: strings.Contains(rest, "<"),
			})
		}
	}
	return out
}

// slashCommandNames lists every command name and alias, for plain completion.
func slashCommandNames() []string {
	var names []string
	for _, cmd := range slashCommandList() {
		names = append(names, cmd.Name)
		names = append(names, cmd.Aliases...)
	}
	sort.Strings(names)
	return names
}

func findSlashCommand(name string) (slashCommand, bool) {
	name = strings.ToLower(name)
	for _, cmd := range slashCommandList() {
		if cmd.Name == name {
			return cmd, true
		}
		for _, alias := range cmd.Aliases {
			if alias == name {
				return cmd, true
			}
		}
	}
	return slashCommand{}, false
}

// suggestion is one dropdown row. Insert replaces the word being typed;
// NeedsMore means the line is still incomplete after inserting it.
type suggestion struct {
	Label     string
	Desc      string
	Insert    string
	NeedsMore bool
	Command   string // the command this row belongs to
}

// matchCommands ranks commands for a typed "/prefix": prefix matches on the
// name or an alias first, then substring matches, each in help order.
func matchCommands(typed string) []suggestion {
	typed = strings.ToLower(typed)
	needle := strings.TrimPrefix(typed, "/")
	var prefix, substring []suggestion
	for _, cmd := range slashCommandList() {
		row := suggestion{Label: cmd.Usage, Desc: cmd.Desc, Insert: cmd.Name, NeedsMore: cmd.NeedsArg, Command: cmd.Name}
		names := append([]string{cmd.Name}, cmd.Aliases...)
		matched := false
		for _, name := range names {
			if strings.HasPrefix(name, typed) {
				row.Insert = name
				prefix = append(prefix, row)
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		for _, name := range names {
			if strings.Contains(name, needle) {
				substring = append(substring, row)
				break
			}
		}
	}
	return append(prefix, substring...)
}

// argumentValues lists the known values for the next argument of cmd, given
// the arguments already completed. known is false when cmd takes free text.
func (m *teaModel) argumentValues(cmd string, done []string) (values []suggestion, known bool) {
	word := func(label, desc string, needsMore bool) suggestion {
		return suggestion{Label: label, Desc: desc, Insert: label, NeedsMore: needsMore, Command: cmd}
	}
	words := func(desc string, labels ...string) []suggestion {
		out := make([]suggestion, len(labels))
		for i, label := range labels {
			out[i] = word(label, desc, false)
		}
		return out
	}
	sessions := func() []suggestion {
		if m.sessionStore == nil {
			return nil
		}
		list, err := m.sessionStore.List()
		if err != nil {
			return nil
		}
		now := time.Now()
		var out []suggestion
		for _, s := range list {
			out = append(out, suggestion{
				Label: s.Title, Desc: relativeAge(now, s.UpdatedAt) + " · " + s.ID,
				Insert: s.ID, Command: cmd,
			})
		}
		return out
	}
	models := func() []suggestion {
		var out []suggestion
		if m.settings != nil {
			for _, mdl := range m.settings.Models {
				out = append(out, word(mdl.ID, mdl.Name, false))
			}
		}
		return out
	}
	processes := func() []suggestion {
		var out []suggestion
		if m.processMgr != nil {
			for _, p := range m.processMgr.List() {
				if !p.Exited {
					out = append(out, word(p.ID, p.Command, false))
				}
			}
		}
		return out
	}

	if len(done) == 0 {
		switch cmd {
		case "/model":
			return models(), true
		case "/models":
			return append(models(), word("add", "Register a new endpoint: add <id> <url>", true)), true
		case "/theme", "/themes":
			var out []suggestion
			for _, t := range builtinThemes {
				out = append(out, word(t.Name, t.Description, false))
			}
			return out, true
		case "/set":
			return []suggestion{
				word("turns", "Maximum agent tool turns per prompt (1-100)", true),
				word("timeout", "Command timeout in seconds (1-3600)", true),
				word("think", "Reasoning level", true),
				word("commands", "Enable or disable command tools", true),
				word("sandbox", "Isolate commands from this machine", true),
				word("permissions", "Permission mode", true),
				word("output", "Tool result detail", true),
			}, true
		case "/resume":
			return append([]suggestion{word("last", "Most recently updated session", false)}, sessions()...), true
		case "/session", "/delete-session":
			return sessions(), true
		case "/skills":
			out := []suggestion{word("list", "List installed skills", false)}
			for _, s := range m.skills {
				out = append(out, word(s.Name, skillSummary(s.Description, 120), false))
			}
			return out, true
		case "/permissions", "/permission":
			return []suggestion{
				word("list", "List active capability grants", false),
				word("revoke", "Revoke a grant by ID or tool name", true),
				word("clear", "Revoke all grants", false),
			}, true
		case "/kill":
			return append(processes(), word("all", "Terminate every background process", false)), true
		case "/logs":
			return processes(), true
		case "/sessions":
			return []suggestion{word("list", "Print the saved sessions table", false)}, true
		case "/import":
			return []suggestion{word("all", "Import every legacy conversation", false)}, true
		case "/discard":
			out := []suggestion{word("all", "Discard every uncommitted change", false)}
			for _, f := range m.changes.files {
				out = append(out, word(f.Path, "uncommitted changes", false))
			}
			return out, true
		}
	}
	if cmd == "/set" && len(done) == 1 {
		switch strings.ToLower(done[0]) {
		case "think":
			return words("reasoning level", "off", "low", "medium", "high"), true
		case "commands", "sandbox":
			return words("", "on", "off"), true
		case "permissions", "permission":
			return words("permission mode", "plan", "agent", "edit", "full"), true
		case "output":
			return words("tool result detail", "compact", "expanded"), true
		}
	}
	return nil, false
}

// filterValues keeps values whose label or insert text contains partial,
// prefix matches first.
func filterValues(values []suggestion, partial string) []suggestion {
	partial = strings.ToLower(partial)
	if partial == "" {
		return values
	}
	var prefix, substring []suggestion
	for _, v := range values {
		label, insert := strings.ToLower(v.Label), strings.ToLower(v.Insert)
		switch {
		case strings.HasPrefix(label, partial) || strings.HasPrefix(insert, partial):
			prefix = append(prefix, v)
		case strings.Contains(label, partial) || strings.Contains(insert, partial):
			substring = append(substring, v)
		}
	}
	return append(prefix, substring...)
}
