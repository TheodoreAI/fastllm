package harness

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// isolateTheme restores the default theme and profile after the test and
// points preferences at a temp file so nothing touches ~/.fastllm.
func isolateTheme(t *testing.T) string {
	t.Helper()
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	prefs := filepath.Join(t.TempDir(), "preferences.json")
	previousPath, previousProfile := preferencesPath, colorProfile
	preferencesPath = func() string { return prefs }
	t.Cleanup(func() {
		preferencesPath, colorProfile = previousPath, previousProfile
		_ = ApplyTheme(defaultThemeName)
	})
	return prefs
}

var hexColor = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

func TestBuiltinThemesAreComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, th := range builtinThemes {
		if th.Name == "" || seen[th.Name] {
			t.Fatalf("theme name %q is empty or duplicated", th.Name)
		}
		seen[th.Name] = true
		for role, value := range map[string]string{
			"Muted": th.Muted, "Border": th.Border, "Frame": th.Frame, "Text": th.Text, "Value": th.Value,
			"Accent": th.Accent, "Accent2": th.Accent2, "Purple": th.Purple, "Ok": th.Ok, "Warn": th.Warn,
			"Error": th.Error, "DarkBg": th.DarkBg, "CardBg": th.CardBg, "Track": th.Track, "BrandFg": th.BrandFg,
		} {
			if !hexColor.MatchString(value) {
				t.Errorf("theme %s role %s = %q is not #RRGGBB", th.Name, role, value)
			}
		}
	}
	if _, ok := FindTheme(defaultThemeName); !ok {
		t.Fatal("default theme is not registered")
	}
}

func TestApplyThemeUpdatesChromeAndStyles(t *testing.T) {
	isolateTheme(t)
	if err := ApplyTheme("nord"); err != nil {
		t.Fatal(err)
	}
	if tuiColorCyan != lipgloss.Color("#88C0D0") {
		t.Fatalf("accent = %s", tuiColorCyan)
	}
	if styleBrand.GetBackground() != lipgloss.Color("#88C0D0") || styleBrand.GetForeground() != lipgloss.Color("#2E3440") {
		t.Fatal("styleBrand was not rebuilt from the theme")
	}
	if err := ApplyTheme("gruvbox"); err != nil {
		t.Fatal(err)
	}
	if styleDiffAdd.GetForeground() != lipgloss.Color("#B8BB26") {
		t.Fatal("styles did not follow a second theme switch")
	}
	if err := ApplyTheme("no-such-theme"); err == nil || !strings.Contains(err.Error(), "nord") {
		t.Fatalf("unknown theme error should list names, got %v", err)
	}
	if currentTheme.Name != "gruvbox" {
		t.Fatal("a failed ApplyTheme must leave the current theme alone")
	}
}

func TestPrintedTextColours(t *testing.T) {
	isolateTheme(t)

	_ = ApplyTheme("terminal")
	if got := ColorCyan("x"); got != ansiBrightCyan+"x"+ansiReset {
		t.Fatalf("terminal theme should use 16-colour codes, got %q", got)
	}

	colorProfile = func() termenv.Profile { return termenv.TrueColor }
	_ = ApplyTheme("nord")
	got := ColorCyan("x")
	if !strings.Contains(got, "38;2;136;192;208") {
		t.Fatalf("nord accent should be truecolour #88C0D0, got %q", got)
	}
	if StripANSI(got) != "x" || VisualLen(got) != 1 {
		t.Fatalf("truecolour output breaks width measurement: %q", got)
	}

	colorProfile = func() termenv.Profile { return termenv.Ascii }
	_ = ApplyTheme("nord")
	if got := ColorCyan("x"); got != ansiBrightCyan+"x"+ansiReset {
		t.Fatalf("non-terminal output should fall back to 16-colour codes, got %q", got)
	}
}

func TestWelcomeBannerAlignedUnderEveryTheme(t *testing.T) {
	isolateTheme(t)
	colorProfile = func() termenv.Profile { return termenv.TrueColor }
	for _, th := range builtinThemes {
		_ = ApplyTheme(th.Name)
		banner := FormatWelcomeBanner("C:\\Users\\mateo\\go\\fastllm", "muse-glimmer", "C:\\Users\\mateo\\.fastllm\\config.json", true, 0, true)
		lines := strings.Split(strings.TrimSpace(banner), "\n")
		want := VisualLen(lines[0])
		for i, line := range lines {
			if VisualLen(line) != want {
				t.Fatalf("%s: line %d width %d, want %d", th.Name, i, VisualLen(line), want)
			}
		}
	}
}

func TestThemePreferenceRoundTrip(t *testing.T) {
	prefs := isolateTheme(t)

	LoadThemePreference() // no file yet
	if currentTheme.Name != defaultThemeName {
		t.Fatalf("missing file should give the default, got %s", currentTheme.Name)
	}
	if err := SaveThemePreference("gruvbox"); err != nil {
		t.Fatal(err)
	}
	if err := SaveThemePreference("catppuccin"); err != nil { // replaces an existing file
		t.Fatal(err)
	}
	LoadThemePreference()
	if currentTheme.Name != "catppuccin" {
		t.Fatalf("loaded %s, want catppuccin", currentTheme.Name)
	}

	if err := os.WriteFile(prefs, []byte(`{"theme":"bogus"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	LoadThemePreference()
	if currentTheme.Name != defaultThemeName {
		t.Fatalf("unknown saved theme should give the default, got %s", currentTheme.Name)
	}
}

func TestThemeModalPreviewCancelAndSave(t *testing.T) {
	prefs := isolateTheme(t)
	_ = ApplyTheme("nord")
	m := &teaModel{input: textarea.New()}

	m.handleThemeSlash([]string{"/theme"})
	if m.themeModal == nil {
		t.Fatal("/theme should open the picker")
	}
	m.handleThemeModalKey(tea.KeyMsg{Type: tea.KeyDown})
	if currentTheme.Name != builtinThemes[1].Name {
		t.Fatalf("moving the cursor should preview %s, got %s", builtinThemes[1].Name, currentTheme.Name)
	}
	m.handleThemeModalKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.themeModal != nil || currentTheme.Name != "nord" {
		t.Fatalf("Esc should close and restore nord, got %s", currentTheme.Name)
	}
	if _, err := os.Stat(prefs); err == nil {
		t.Fatal("cancelling must not save a preference")
	}

	m.handleThemeSlash([]string{"/theme"})
	m.handleThemeModalKey(tea.KeyMsg{Type: tea.KeyEnd})
	m.handleThemeModalKey(tea.KeyMsg{Type: tea.KeyEnter})
	last := builtinThemes[len(builtinThemes)-1].Name
	if m.themeModal != nil || currentTheme.Name != last {
		t.Fatalf("Enter should keep %s, got %s", last, currentTheme.Name)
	}
	if data, err := os.ReadFile(prefs); err != nil || !strings.Contains(string(data), last) {
		t.Fatalf("Enter should save %s: %q %v", last, data, err)
	}
}

func TestThemeSlashWithName(t *testing.T) {
	isolateTheme(t)
	m := &teaModel{input: textarea.New()}
	m.handleThemeSlash([]string{"/theme", "Tokyo-Night"})
	if currentTheme.Name != "tokyo-night" || m.themeModal != nil {
		t.Fatalf("/theme <name> should switch directly, got %s", currentTheme.Name)
	}
	m.handleThemeSlash([]string{"/theme", "nope"})
	if currentTheme.Name != "tokyo-night" || !strings.Contains(StripANSI(m.historyText.String()), "unknown theme") {
		t.Fatal("an unknown name should report an error and keep the theme")
	}
}

func TestThemeModalRenders(t *testing.T) {
	isolateTheme(t)
	m := &teaModel{input: textarea.New(), width: 100, height: 40}
	m.openThemeModal()
	out := StripANSI(m.renderThemeModal())
	for _, th := range builtinThemes {
		if !strings.Contains(out, th.Name) {
			t.Fatalf("picker missing %s:\n%s", th.Name, out)
		}
	}
}

func TestThemeModalWindowedScrolling(t *testing.T) {
	isolateTheme(t)
	// Constrained height: 16 rows cannot fit all 13 themes at 2 rows each
	m := &teaModel{input: textarea.New(), width: 80, height: 16}
	m.openThemeModal()

	// Cursor at 0: should show down indicator, no up indicator
	outTop := StripANSI(m.renderThemeModal())
	if !strings.Contains(outTop, "▼") || !strings.Contains(outTop, "more below") {
		t.Fatalf("top of list should show downward scroll indicator, got:\n%s", outTop)
	}
	if strings.Contains(outTop, "▲") || strings.Contains(outTop, "more above") {
		t.Fatalf("top of list should NOT show upward scroll indicator, got:\n%s", outTop)
	}

	// Move cursor to bottom
	m.handleThemeModalKey(tea.KeyMsg{Type: tea.KeyEnd})
	outBottom := StripANSI(m.renderThemeModal())
	if !strings.Contains(outBottom, "▲") || !strings.Contains(outBottom, "more above") {
		t.Fatalf("bottom of list should show upward scroll indicator, got:\n%s", outBottom)
	}
	if strings.Contains(outBottom, "▼") || strings.Contains(outBottom, "more below") {
		t.Fatalf("bottom of list should NOT show downward scroll indicator, got:\n%s", outBottom)
	}
}

func TestAllNewThemesApplyProperly(t *testing.T) {
	isolateTheme(t)
	newThemes := []string{
		"dracula",
		"solarized-dark",
		"monokai",
		"rose-pine",
		"one-dark",
		"github-dark",
		"synthwave",
	}
	for _, name := range newThemes {
		th, ok := FindTheme(name)
		if !ok {
			t.Fatalf("theme %q not found in builtinThemes", name)
		}
		if err := ApplyTheme(name); err != nil {
			t.Fatalf("failed to apply %q: %v", name, err)
		}
		if currentTheme.Name != name {
			t.Fatalf("currentTheme = %q; want %q", currentTheme.Name, name)
		}
		if tuiColorCyan != lipgloss.Color(th.Accent) {
			t.Fatalf("theme %q: accent color mismatch %s != %s", name, tuiColorCyan, th.Accent)
		}
		swatches := themeSwatches(th)
		if !strings.Contains(swatches, "██") {
			t.Fatalf("theme %q swatches invalid: %q", name, swatches)
		}
	}
}

