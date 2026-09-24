package harness

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// CommandKind classifies how a shell command should be executed.
type CommandKind int

const (
	// CmdKindStandard is executed headlessly via runUserCommand with stdout/stderr captured inline.
	CmdKindStandard CommandKind = iota

	// CmdKindTUIEditor is an interactive terminal program (nano, vim, less) that requires raw TTY
	// and takes over the terminal via tea.ExecProcess.
	CmdKindTUIEditor

	// CmdKindGUIEditor is an external desktop application (notepad, code, open) that opens in an
	// independent OS window and should run detached without blocking FastLLM.
	CmdKindGUIEditor

	// CmdKindInteractive is an interactive terminal program or wizard (gh auth login, git commit, REPLs)
	// that requires raw TTY and takes over the terminal via tea.ExecProcess.
	CmdKindInteractive
)

// ShellCommandSpec encapsulates the parsed instructions for executing a shell command.
type ShellCommandSpec struct {
	Kind       CommandKind
	Cmd        *exec.Cmd
	BinaryName string
	TargetFile string
	Notice     string
}

var tuiBinaries = map[string]bool{
	"nano":  true,
	"vim":   true,
	"vi":    true,
	"nvim":  true,
	"micro": true,
	"helix": true,
	"hx":    true,
	"emacs": true,
	"pico":  true,
	"joe":   true,
	"ed":    true,
	"less":  true,
	"more":  true,
	"man":   true,
}

var guiBinaries = map[string]bool{
	"notepad":     true,
	"notepad.exe": true,
	"code":        true,
	"code.cmd":    true,
	"code.exe":    true,
	"subl":        true,
	"sublime":     true,
	"atom":        true,
	"gedit":       true,
	"kate":        true,
	"open":        true,
	"xdg-open":    true,
}

var interactiveBinaries = map[string]bool{
	"lazygit": true,
	"tig":     true,
	"htop":    true,
	"top":     true,
	"btop":    true,
	"glances": true,
	"ranger":  true,
	"k9s":     true,
	"fzf":     true,
	"peco":    true,
	"ssh":     true,
	"sftp":    true,
}

var subshellBinaries = map[string]bool{
	"bash":       true,
	"zsh":        true,
	"fish":       true,
	"sh":         true,
	"pwsh":       true,
	"powershell": true,
}

var replBinaries = map[string]bool{
	"python":    true,
	"python3":   true,
	"node":      true,
	"ipython":   true,
	"irb":       true,
	"psql":      true,
	"sqlite3":   true,
	"mysql":     true,
	"redis-cli": true,
	"mongosh":   true,
}

// ClassifyShellCommand inspects the command line to determine if it should be executed
// via an interactive TUI passthrough (nano/vim/gh auth login), an asynchronous GUI spawn (notepad/code),
// or the standard headless execution engine (cat/git/build commands).
func ClassifyShellCommand(cmdStr, workingDir string) ShellCommandSpec {
	trimmed := strings.TrimSpace(cmdStr)
	if trimmed == "" {
		return ShellCommandSpec{Kind: CmdKindStandard}
	}

	// Explicit override: ": <cmd>" or "run -i <cmd>" or "term <cmd>" forces interactive TTY passthrough
	forceInteractive := false
	forceStandard := false
	rawCmd := trimmed
	if strings.HasPrefix(trimmed, ":") {
		forceInteractive = true
		rawCmd = strings.TrimSpace(trimmed[1:])
	} else if strings.HasPrefix(trimmed, "run -i ") {
		forceInteractive = true
		rawCmd = strings.TrimSpace(trimmed[len("run -i "):])
	} else if strings.HasPrefix(trimmed, "term ") {
		forceInteractive = true
		rawCmd = strings.TrimSpace(trimmed[len("term "):])
	} else if strings.HasPrefix(trimmed, "run -b ") {
		forceStandard = true
		rawCmd = strings.TrimSpace(trimmed[len("run -b "):])
	} else if strings.HasPrefix(trimmed, "batch ") {
		forceStandard = true
		rawCmd = strings.TrimSpace(trimmed[len("batch "):])
	}

	fields := strings.Fields(rawCmd)
	if len(fields) == 0 {
		return ShellCommandSpec{Kind: CmdKindStandard}
	}

	rawBin := fields[0]
	bin := strings.ToLower(filepath.Base(rawBin))
	binLower := strings.TrimSuffix(bin, ".exe")

	args := fields[1:]
	target := ""
	if len(args) > 0 {
		target = args[0]
	}

	if forceInteractive {
		cmd := exec.Command(fields[0], fields[1:]...)
		cmd.Dir = workingDir
		return ShellCommandSpec{
			Kind:       CmdKindInteractive,
			Cmd:        cmd,
			BinaryName: binLower,
			TargetFile: target,
			Notice:     fmt.Sprintf("Interactive: %s", rawCmd),
		}
	}

	if forceStandard {
		return ShellCommandSpec{
			Kind:       CmdKindStandard,
			BinaryName: binLower,
			TargetFile: target,
		}
	}

	// 1. Sudo wrapper for interactive commands (e.g. "sudo nano /etc/hosts" or "sudo htop")
	if binLower == "sudo" && len(fields) > 1 {
		subSpec := ClassifyShellCommand(strings.Join(fields[1:], " "), workingDir)
		if subSpec.Kind == CmdKindTUIEditor || subSpec.Kind == CmdKindInteractive {
			cmd := exec.Command(fields[0], fields[1:]...)
			cmd.Dir = workingDir
			return ShellCommandSpec{
				Kind:       subSpec.Kind,
				Cmd:        cmd,
				BinaryName: subSpec.BinaryName,
				TargetFile: subSpec.TargetFile,
				Notice:     "sudo " + subSpec.BinaryName,
			}
		}
	}

	// 2. Generic 'edit' command (e.g. "edit main.go")
	if binLower == "edit" {
		return ResolveDefaultEditor(args, workingDir)
	}

	// 3. Notepad handling (native on Windows; mapped to system editor on macOS/Linux if notepad is missing)
	if binLower == "notepad" {
		if runtime.GOOS == "windows" {
			cmd := exec.Command("notepad.exe", args...)
			cmd.Dir = workingDir
			return ShellCommandSpec{
				Kind:       CmdKindGUIEditor,
				Cmd:        cmd,
				BinaryName: "notepad",
				TargetFile: target,
				Notice:     fmt.Sprintf("Opened %s in Notepad", target),
			}
		}
		// On non-Windows: check if notepad executable exists (e.g. Wine or alias)
		if _, err := exec.LookPath("notepad"); err == nil {
			cmd := exec.Command("notepad", args...)
			cmd.Dir = workingDir
			return ShellCommandSpec{
				Kind:       CmdKindGUIEditor,
				Cmd:        cmd,
				BinaryName: "notepad",
				TargetFile: target,
				Notice:     fmt.Sprintf("Opened %s in Notepad", target),
			}
		}
		// Fallback for macOS / Linux
		if runtime.GOOS == "darwin" {
			cmd := exec.Command("open", append([]string{"-e"}, args...)...)
			cmd.Dir = workingDir
			return ShellCommandSpec{
				Kind:       CmdKindGUIEditor,
				Cmd:        cmd,
				BinaryName: "TextEdit",
				TargetFile: target,
				Notice:     fmt.Sprintf("Opened %s in TextEdit (notepad mapped to macOS editor)", target),
			}
		}
		if _, err := exec.LookPath("xdg-open"); err == nil {
			cmd := exec.Command("xdg-open", args...)
			cmd.Dir = workingDir
			return ShellCommandSpec{
				Kind:       CmdKindGUIEditor,
				Cmd:        cmd,
				BinaryName: "xdg-open",
				TargetFile: target,
				Notice:     fmt.Sprintf("Opened %s with xdg-open", target),
			}
		}
		// Final fallback to default TUI editor
		return ResolveDefaultEditor(args, workingDir)
	}

	// 4. Recognized TUI Editors and Pagers
	if tuiBinaries[binLower] {
		cmd := exec.Command(fields[0], fields[1:]...)
		cmd.Dir = workingDir
		return ShellCommandSpec{
			Kind:       CmdKindTUIEditor,
			Cmd:        cmd,
			BinaryName: binLower,
			TargetFile: target,
		}
	}

	// 5. Recognized Desktop GUI Editors
	if guiBinaries[binLower] {
		cmd := exec.Command(fields[0], fields[1:]...)
		cmd.Dir = workingDir
		notice := fmt.Sprintf("Opened %s in %s", target, bin)
		if target == "" {
			notice = fmt.Sprintf("Launched %s", bin)
		}
		return ShellCommandSpec{
			Kind:       CmdKindGUIEditor,
			Cmd:        cmd,
			BinaryName: binLower,
			TargetFile: target,
			Notice:     notice,
		}
	}

	// 6. GitHub CLI (gh) classification
	if binLower == "gh" {
		return classifyGHCommand(fields, args, workingDir)
	}

	// 7. Git command classification
	if binLower == "git" {
		return classifyGitCommand(fields, args, workingDir)
	}

	// 8. Standalone interactive TUI tools (lazygit, htop, fzf, etc.)
	if interactiveBinaries[binLower] {
		cmd := exec.Command(fields[0], fields[1:]...)
		cmd.Dir = workingDir
		return ShellCommandSpec{
			Kind:       CmdKindInteractive,
			Cmd:        cmd,
			BinaryName: binLower,
			TargetFile: target,
			Notice:     binLower,
		}
	}

	// 9. Interactive subshells (bash, zsh, etc. when run without script)
	if isInteractiveSubshell(binLower, args) {
		cmd := exec.Command(fields[0], fields[1:]...)
		cmd.Dir = workingDir
		return ShellCommandSpec{
			Kind:       CmdKindInteractive,
			Cmd:        cmd,
			BinaryName: binLower,
			Notice:     "Subshell: " + binLower,
		}
	}

	// 10. Interactive language REPLs (python, node, etc. when run without script)
	if isInteractiveREPL(binLower, args) {
		cmd := exec.Command(fields[0], fields[1:]...)
		cmd.Dir = workingDir
		return ShellCommandSpec{
			Kind:       CmdKindInteractive,
			Cmd:        cmd,
			BinaryName: binLower,
			Notice:     "REPL: " + binLower,
		}
	}

	// 11. Standard command (executed headlessly)
	return ShellCommandSpec{
		Kind:       CmdKindStandard,
		BinaryName: binLower,
		TargetFile: target,
	}
}

func classifyGHCommand(fields []string, args []string, workingDir string) ShellCommandSpec {
	if len(args) == 0 {
		return ShellCommandSpec{Kind: CmdKindStandard, BinaryName: "gh"}
	}

	sub := strings.ToLower(args[0])

	// If any argument is --web or -w, it opens a browser or interactive flow
	for _, a := range args {
		if a == "--web" || a == "-w" {
			cmd := exec.Command(fields[0], fields[1:]...)
			cmd.Dir = workingDir
			return ShellCommandSpec{
				Kind:       CmdKindInteractive,
				Cmd:        cmd,
				BinaryName: "gh",
				Notice:     "gh (browser)",
			}
		}
	}

	switch sub {
	case "auth":
		if len(args) > 1 {
			authSub := strings.ToLower(args[1])
			if authSub == "login" || authSub == "refresh" || authSub == "setup-git" || authSub == "switch" {
				cmd := exec.Command(fields[0], fields[1:]...)
				cmd.Dir = workingDir
				return ShellCommandSpec{
					Kind:       CmdKindInteractive,
					Cmd:        cmd,
					BinaryName: "gh",
					Notice:     "gh auth " + authSub,
				}
			}
		}
	case "copilot":
		cmd := exec.Command(fields[0], fields[1:]...)
		cmd.Dir = workingDir
		return ShellCommandSpec{
			Kind:       CmdKindInteractive,
			Cmd:        cmd,
			BinaryName: "gh",
			Notice:     "gh copilot",
		}
	case "browse":
		cmd := exec.Command(fields[0], fields[1:]...)
		cmd.Dir = workingDir
		return ShellCommandSpec{
			Kind:       CmdKindInteractive,
			Cmd:        cmd,
			BinaryName: "gh",
			Notice:     "gh browse",
		}
	case "pr":
		if len(args) > 1 {
			prSub := strings.ToLower(args[1])
			if prSub == "create" {
				hasTitle := false
				hasBody := false
				hasFill := false
				for i := 2; i < len(args); i++ {
					arg := args[i]
					if arg == "-t" || arg == "--title" || strings.HasPrefix(arg, "--title=") {
						hasTitle = true
					}
					if arg == "-b" || arg == "--body" || strings.HasPrefix(arg, "--body=") || arg == "-F" || arg == "--body-file" || strings.HasPrefix(arg, "--body-file=") {
						hasBody = true
					}
					if arg == "--fill" || arg == "--fill-first" {
						hasFill = true
					}
				}
				if !hasFill && !(hasTitle && hasBody) {
					cmd := exec.Command(fields[0], fields[1:]...)
					cmd.Dir = workingDir
					return ShellCommandSpec{
						Kind:       CmdKindInteractive,
						Cmd:        cmd,
						BinaryName: "gh",
						Notice:     "gh pr create",
					}
				}
			} else if prSub == "checkout" {
				hasTarget := false
				for i := 2; i < len(args); i++ {
					if !strings.HasPrefix(args[i], "-") {
						hasTarget = true
						break
					}
				}
				if !hasTarget {
					cmd := exec.Command(fields[0], fields[1:]...)
					cmd.Dir = workingDir
					return ShellCommandSpec{
						Kind:       CmdKindInteractive,
						Cmd:        cmd,
						BinaryName: "gh",
						Notice:     "gh pr checkout",
					}
				}
			}
		}
	case "issue":
		if len(args) > 1 && strings.ToLower(args[1]) == "create" {
			hasTitle := false
			hasBody := false
			for i := 2; i < len(args); i++ {
				arg := args[i]
				if arg == "-t" || arg == "--title" || strings.HasPrefix(arg, "--title=") {
					hasTitle = true
				}
				if arg == "-b" || arg == "--body" || strings.HasPrefix(arg, "--body=") || arg == "-F" || arg == "--body-file" || strings.HasPrefix(arg, "--body-file=") {
					hasBody = true
				}
			}
			if !(hasTitle && hasBody) {
				cmd := exec.Command(fields[0], fields[1:]...)
				cmd.Dir = workingDir
				return ShellCommandSpec{
					Kind:       CmdKindInteractive,
					Cmd:        cmd,
					BinaryName: "gh",
					Notice:     "gh issue create",
				}
			}
		}
	case "repo":
		if len(args) > 1 {
			repoSub := strings.ToLower(args[1])
			if repoSub == "create" && len(args) == 2 {
				cmd := exec.Command(fields[0], fields[1:]...)
				cmd.Dir = workingDir
				return ShellCommandSpec{
					Kind:       CmdKindInteractive,
					Cmd:        cmd,
					BinaryName: "gh",
					Notice:     "gh repo create",
				}
			}
			if repoSub == "fork" {
				hasCloneFlag := false
				for _, a := range args[2:] {
					if strings.HasPrefix(a, "--clone") {
						hasCloneFlag = true
						break
					}
				}
				if !hasCloneFlag {
					cmd := exec.Command(fields[0], fields[1:]...)
					cmd.Dir = workingDir
					return ShellCommandSpec{
						Kind:       CmdKindInteractive,
						Cmd:        cmd,
						BinaryName: "gh",
						Notice:     "gh repo fork",
					}
				}
			}
		}
	}

	return ShellCommandSpec{
		Kind:       CmdKindStandard,
		BinaryName: "gh",
	}
}

func classifyGitCommand(fields []string, args []string, workingDir string) ShellCommandSpec {
	if len(args) == 0 {
		return ShellCommandSpec{Kind: CmdKindStandard, BinaryName: "git"}
	}

	sub := strings.ToLower(args[0])
	switch sub {
	case "commit":
		hasMsg := false
		forceEdit := false
		for _, a := range args[1:] {
			if a == "-m" || a == "--message" || strings.HasPrefix(a, "-m=") || strings.HasPrefix(a, "--message=") ||
				a == "-F" || a == "--file" || strings.HasPrefix(a, "-F=") || strings.HasPrefix(a, "--file=") ||
				a == "--no-edit" || strings.HasPrefix(a, "--fixup=") {
				hasMsg = true
			}
			if a == "-e" || a == "--edit" {
				forceEdit = true
			}
		}
		if !hasMsg || forceEdit {
			cmd := exec.Command(fields[0], fields[1:]...)
			cmd.Dir = workingDir
			return ShellCommandSpec{
				Kind:       CmdKindInteractive,
				Cmd:        cmd,
				BinaryName: "git",
				Notice:     "git commit",
			}
		}
	case "add":
		for _, a := range args[1:] {
			if a == "-p" || a == "--patch" || a == "-i" || a == "--interactive" {
				cmd := exec.Command(fields[0], fields[1:]...)
				cmd.Dir = workingDir
				return ShellCommandSpec{
					Kind:       CmdKindInteractive,
					Cmd:        cmd,
					BinaryName: "git",
					Notice:     "git add (interactive)",
				}
			}
		}
	case "rebase":
		for _, a := range args[1:] {
			if a == "-i" || a == "--interactive" {
				cmd := exec.Command(fields[0], fields[1:]...)
				cmd.Dir = workingDir
				return ShellCommandSpec{
					Kind:       CmdKindInteractive,
					Cmd:        cmd,
					BinaryName: "git",
					Notice:     "git rebase -i",
				}
			}
		}
	case "checkout", "reset", "restore":
		for _, a := range args[1:] {
			if a == "-p" || a == "--patch" {
				cmd := exec.Command(fields[0], fields[1:]...)
				cmd.Dir = workingDir
				return ShellCommandSpec{
					Kind:       CmdKindInteractive,
					Cmd:        cmd,
					BinaryName: "git",
					Notice:     "git " + sub + " --patch",
				}
			}
		}
	}

	return ShellCommandSpec{
		Kind:       CmdKindStandard,
		BinaryName: "git",
	}
}

func isInteractiveSubshell(binLower string, args []string) bool {
	if !subshellBinaries[binLower] {
		return false
	}
	if len(args) == 0 {
		return true
	}
	if len(args) == 1 && args[0] == "-i" {
		return true
	}
	return false
}

func isInteractiveREPL(binLower string, args []string) bool {
	if !replBinaries[binLower] {
		return false
	}
	if len(args) == 0 {
		return true
	}
	if len(args) == 1 && args[0] == "-i" {
		return true
	}
	return false
}

// EnrichInteractiveOutcome inspects the result of an interactive CLI tool and returns
// a concise summary to display in the TUI terminal box.
func EnrichInteractiveOutcome(cmdStr string, spec ShellCommandSpec, workingDir string, err error) string {
	if err != nil {
		return fmt.Sprintf("Session finished with error: %v", err)
	}

	fields := strings.Fields(cmdStr)
	if len(fields) == 0 {
		return "Session finished"
	}

	bin := strings.ToLower(filepath.Base(fields[0]))
	bin = strings.TrimSuffix(bin, ".exe")

	if bin == "git" && len(fields) > 1 && fields[1] == "commit" {
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", "log", "-1", "--oneline")
		cmd.Dir = workingDir
		out, logErr := cmd.Output()
		if logErr == nil && len(out) > 0 {
			return fmt.Sprintf("✓ Commit created: %s", strings.TrimSpace(string(out)))
		}
		return "✓ Commit completed"
	}

	if bin == "gh" && len(fields) > 2 && fields[1] == "pr" && fields[2] == "create" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "gh", "pr", "view", "--json", "number,title,url", "--template", "PR #{{.number}}: {{.title}}\n  {{.url}}")
		cmd.Dir = workingDir
		out, prErr := cmd.Output()
		if prErr == nil && len(out) > 0 {
			return fmt.Sprintf("✓ Created %s", strings.TrimSpace(string(out)))
		}
		return "✓ Completed gh pr create"
	}

	if bin == "gh" && len(fields) > 2 && fields[1] == "auth" && fields[2] == "login" {
		return "✓ GitHub CLI authentication completed"
	}

	if spec.Notice != "" {
		return fmt.Sprintf("Completed session: %s", spec.Notice)
	}
	return fmt.Sprintf("Completed interactive session (%s)", spec.BinaryName)
}

// ResolveDefaultEditor chooses an editor based on environment variables ($EDITOR / $VISUAL)
// or platform defaults (notepad.exe on Windows, nano/vim on Unix).
func ResolveDefaultEditor(args []string, workingDir string) ShellCommandSpec {
	target := ""
	if len(args) > 0 {
		target = args[0]
	}

	editorEnv := strings.TrimSpace(os.Getenv("EDITOR"))
	if editorEnv == "" {
		editorEnv = strings.TrimSpace(os.Getenv("VISUAL"))
	}

	if editorEnv != "" {
		fields := strings.Fields(editorEnv)
		bin := filepath.Base(fields[0])
		binLower := strings.ToLower(strings.TrimSuffix(bin, ".exe"))
		allArgs := append(fields[1:], args...)
		cmd := exec.Command(fields[0], allArgs...)
		cmd.Dir = workingDir

		if guiBinaries[binLower] {
			return ShellCommandSpec{
				Kind:       CmdKindGUIEditor,
				Cmd:        cmd,
				BinaryName: binLower,
				TargetFile: target,
				Notice:     fmt.Sprintf("Opened %s in %s", target, bin),
			}
		}
		return ShellCommandSpec{
			Kind:       CmdKindTUIEditor,
			Cmd:        cmd,
			BinaryName: binLower,
			TargetFile: target,
		}
	}

	// OS-specific default if $EDITOR is not configured
	if runtime.GOOS == "windows" {
		cmd := exec.Command("notepad.exe", args...)
		cmd.Dir = workingDir
		return ShellCommandSpec{
			Kind:       CmdKindGUIEditor,
			Cmd:        cmd,
			BinaryName: "notepad",
			TargetFile: target,
			Notice:     fmt.Sprintf("Opened %s in Notepad", target),
		}
	}

	editorBin := "nano"
	if _, err := exec.LookPath("nano"); err != nil {
		if _, err := exec.LookPath("vim"); err == nil {
			editorBin = "vim"
		} else {
			editorBin = "vi"
		}
	}

	cmd := exec.Command(editorBin, args...)
	cmd.Dir = workingDir
	return ShellCommandSpec{
		Kind:       CmdKindTUIEditor,
		Cmd:        cmd,
		BinaryName: editorBin,
		TargetFile: target,
	}
}

// LaunchGUIEditor starts an external GUI application in the background and reaps it
// upon completion so that FastLLM is not blocked.
func LaunchGUIEditor(cmd *exec.Cmd) error {
	if cmd == nil {
		return fmt.Errorf("no command specified")
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		_ = cmd.Wait()
	}()
	return nil
}
