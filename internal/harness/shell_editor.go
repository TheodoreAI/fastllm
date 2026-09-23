package harness

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

// ClassifyShellCommand inspects the command line to determine if it should be executed
// via an interactive TUI passthrough (nano/vim), an asynchronous GUI spawn (notepad/code),
// or the standard headless execution engine (cat/git/build commands).
func ClassifyShellCommand(cmdStr, workingDir string) ShellCommandSpec {
	fields := strings.Fields(cmdStr)
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

	// 1. Sudo wrapper for interactive commands (e.g. "sudo nano /etc/hosts")
	if binLower == "sudo" && len(fields) > 1 {
		subBin := strings.ToLower(filepath.Base(fields[1]))
		subBinLower := strings.TrimSuffix(subBin, ".exe")
		if tuiBinaries[subBinLower] {
			subTarget := ""
			if len(fields) > 2 {
				subTarget = fields[2]
			}
			cmd := exec.Command(fields[0], fields[1:]...)
			cmd.Dir = workingDir
			return ShellCommandSpec{
				Kind:       CmdKindTUIEditor,
				Cmd:        cmd,
				BinaryName: subBinLower,
				TargetFile: subTarget,
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

	// 6. Standard command (executed headlessly)
	return ShellCommandSpec{
		Kind:       CmdKindStandard,
		BinaryName: binLower,
		TargetFile: target,
	}
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
