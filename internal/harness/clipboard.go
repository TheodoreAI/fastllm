package harness

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"fastllm/internal/media"

	"github.com/atotto/clipboard"
)

// ClipboardMedia represents an image or document extracted from the system clipboard.
type ClipboardMedia struct {
	IsMedia  bool
	MimeType string
	Filename string
	Raw      []byte
	FilePath string
}

// ReadClipboardMedia checks the operating system clipboard for:
// 1. Binary image data (e.g. screenshot or copied image).
// 2. Copied file references from the OS file manager.
// 3. File paths pasted as text.
func ReadClipboardMedia() (ClipboardMedia, error) {
	switch runtime.GOOS {
	case "darwin":
		if cm, ok := readClipboardDarwin(); ok {
			return cm, nil
		}
	case "windows":
		if cm, ok := readClipboardWindows(); ok {
			return cm, nil
		}
	default:
		if cm, ok := readClipboardLinux(); ok {
			return cm, nil
		}
	}

	// Fall back to checking clipboard text content for file paths
	text, err := clipboard.ReadAll()
	if err == nil {
		clean := strings.TrimSpace(text)
		// Strip surrounding quotes if present
		clean = strings.Trim(clean, "\"'")
		if clean != "" {
			if info, err := os.Stat(clean); err == nil && !info.IsDir() {
				raw, err := os.ReadFile(clean)
				if err == nil {
					mime, ok := media.DetectMime(raw, filepath.Base(clean))
					if ok {
						return ClipboardMedia{
							IsMedia:  true,
							MimeType: mime,
							Filename: filepath.Base(clean),
							Raw:      raw,
							FilePath: clean,
						}, nil
					}
				}
			}
		}
	}

	return ClipboardMedia{IsMedia: false}, nil
}

func readClipboardDarwin() (ClipboardMedia, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 1. Check for image on clipboard and export to temp file
	scriptImg := `
	try
		set theData to the clipboard as «class PNGf»
		set tempFile to (path to temporary items as text) & "fastllm_clip.png"
		set fileRef to open for access file tempFile with write permission
		set eof fileRef to 0
		write theData to fileRef
		close access fileRef
		return POSIX path of file tempFile
	on error
		return ""
	end try`
	cmd := exec.CommandContext(ctx, "osascript", "-e", scriptImg)
	out, err := cmd.Output()
	if err == nil {
		path := strings.TrimSpace(string(out))
		if path != "" {
			data, err := os.ReadFile(path)
			_ = os.Remove(path)
			if err == nil && len(data) > 0 {
				return ClipboardMedia{
					IsMedia:  true,
					MimeType: media.MimePNG,
					Filename: "clipboard.png",
					Raw:      data,
				}, true
			}
		}
	}

	// 2. Check for copied file in Finder
	scriptFile := `
	try
		set fileList to clipboard as «class furl»
		return POSIX path of fileList
	on error
		return ""
	end try`
	cmd = exec.CommandContext(ctx, "osascript", "-e", scriptFile)
	out, err = cmd.Output()
	if err == nil {
		path := strings.TrimSpace(string(out))
		if path != "" {
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				data, err := os.ReadFile(path)
				if err == nil {
					mime, ok := media.DetectMime(data, filepath.Base(path))
					if ok {
						return ClipboardMedia{
							IsMedia:  true,
							MimeType: mime,
							Filename: filepath.Base(path),
							Raw:      data,
							FilePath: path,
						}, true
					}
				}
			}
		}
	}

	return ClipboardMedia{}, false
}

func readClipboardWindows() (ClipboardMedia, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	script := `
	Add-Type -AssemblyName System.Windows.Forms;
	if ([System.Windows.Forms.Clipboard]::ContainsImage()) {
		$img = [System.Windows.Forms.Clipboard]::GetImage();
		$tmp = [System.IO.Path]::GetTempFileName() + '.png';
		$img.Save($tmp, [System.Drawing.Imaging.ImageFormat]::Png);
		Write-Output "IMG:$tmp";
	} elseif ([System.Windows.Forms.Clipboard]::ContainsFileDropList()) {
		$files = [System.Windows.Forms.Clipboard]::GetFileDropList();
		if ($files.Count -gt 0) {
			Write-Output "FILE:$($files[0])";
		}
	}`
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	out, err := cmd.Output()
	if err != nil {
		return ClipboardMedia{}, false
	}
	res := strings.TrimSpace(string(out))
	if strings.HasPrefix(res, "IMG:") {
		path := strings.TrimPrefix(res, "IMG:")
		data, err := os.ReadFile(path)
		_ = os.Remove(path)
		if err == nil && len(data) > 0 {
			return ClipboardMedia{
				IsMedia:  true,
				MimeType: media.MimePNG,
				Filename: "clipboard.png",
				Raw:      data,
			}, true
		}
	} else if strings.HasPrefix(res, "FILE:") {
		path := strings.TrimPrefix(res, "FILE:")
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			data, err := os.ReadFile(path)
			if err == nil {
				mime, ok := media.DetectMime(data, filepath.Base(path))
				if ok {
					return ClipboardMedia{
						IsMedia:  true,
						MimeType: mime,
						Filename: filepath.Base(path),
						Raw:      data,
						FilePath: path,
					}, true
				}
			}
		}
	}
	return ClipboardMedia{}, false
}

func readClipboardLinux() (ClipboardMedia, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Try wl-paste first (Wayland)
	if _, err := exec.LookPath("wl-paste"); err == nil {
		cmd := exec.CommandContext(ctx, "wl-paste", "-t", "image/png")
		out, err := cmd.Output()
		if err == nil && len(out) > 0 {
			return ClipboardMedia{
				IsMedia:  true,
				MimeType: media.MimePNG,
				Filename: "clipboard.png",
				Raw:      out,
			}, true
		}
	}

	// Try xclip (X11)
	if _, err := exec.LookPath("xclip"); err == nil {
		cmd := exec.CommandContext(ctx, "xclip", "-selection", "clipboard", "-t", "image/png", "-out")
		out, err := cmd.Output()
		if err == nil && len(out) > 0 {
			return ClipboardMedia{
				IsMedia:  true,
				MimeType: media.MimePNG,
				Filename: "clipboard.png",
				Raw:      out,
			}, true
		}
	}

	return ClipboardMedia{}, false
}

// Plain-text clipboard access for the editor. Vars, so tests do not touch
// the machine's real clipboard.
var (
	writeClipboardText = clipboard.WriteAll
	readClipboardText  = clipboard.ReadAll
)
