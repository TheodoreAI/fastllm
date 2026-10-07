package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/muesli/termenv"
)

// Theme is one named palette. Every colour in the UI comes from these roles:
// the lipgloss chrome through the tuiColor* vars, and printed text through the
// hue-named helpers in tui_style.go (ColorGray is Muted, ColorCyan is Accent,
// and so on), so call sites never name a theme.
type Theme struct {
	Name        string
	Description string

	Muted           string // labels, hints
	Border          string // input box and modal borders
	Frame           string // printed card frames and dividers
	Text            string // body text
	Value           string // emphasised values, prompts
	Accent          string // brand, titles, highlights
	Accent2         string // secondary accent
	Purple          string // agent tooling
	Ok              string // success, diff additions
	Warn            string // warnings, shell mode
	Error           string // errors, diff deletions
	Bg              string // terminal background, painted behind everything
	CardBg          string // pills, cards, status bar
	Track           string // gauge track
	BrandFg         string // text on the brand pill
	ElementBg       string // composer and selected surfaces; falls back to CardBg
	DiffBackgrounds *diffBackgrounds

	// ANSI16 prints text with the terminal's own 16 colours instead of the
	// palette, so it follows whatever scheme the terminal is set to.
	ANSI16 bool
	// Light marks a palette meant for a light terminal background.
	Light bool
}

const (
	defaultThemeName = "exascale-readable-dark"
	// defaultLightThemeName replaces the default when the terminal reports a
	// light background and the user has not chosen a theme.
	defaultLightThemeName = "github-light"
)

var zincPalette = Theme{
	Muted: "#71717A", Border: "#27272A", Frame: "#52525B", Text: "#D4D4D8", Value: "#FAFAFA",
	Accent: "#06B6D4", Accent2: "#38BDF8", Purple: "#A855F7",
	Ok: "#10B981", Warn: "#F59E0B", Error: "#EF4444",
	Bg: "#09090B", CardBg: "#18181B", Track: "#3F3F46", BrandFg: "#000000",
}

// builtinThemes is in picker order.
var builtinThemes = []Theme{
	{
		Name: "exascale-readable-dark", Description: "OpenCode Exascale with readable text and blue/amber diffs",
		Muted: "#A3B9BD", Border: "#617F86", Frame: "#3F5960", Text: "#E6F5F4", Value: "#E6F5F4",
		Accent: "#4FD6C4", Accent2: "#6AB0FF", Purple: "#C6A0F6",
		Ok: "#6AB0FF", Warn: "#F2B25C", Error: "#F2B25C",
		Bg: "#0F1D21", CardBg: "#16282D", ElementBg: "#1B3036", Track: "#3F5960", BrandFg: "#0F1D21",
		DiffBackgrounds: &diffBackgrounds{
			lineAdd: "\033[48;2;20;45;66m", lineDel: "\033[48;2;56;42;24m",
			wordAdd: "\033[48;2;32;62;88m\033[1m", wordDel: "\033[48;2;75;55;31m\033[1m",
		},
	},
	{
		Name: "nord", Description: "Arctic frost blues on polar night",
		Muted: "#616E88", Border: "#4C566A", Frame: "#4C566A", Text: "#D8DEE9", Value: "#ECEFF4",
		Accent: "#88C0D0", Accent2: "#81A1C1", Purple: "#B48EAD",
		Ok: "#A3BE8C", Warn: "#EBCB8B", Error: "#BF616A",
		Bg: "#242933", CardBg: "#2E3440", Track: "#434C5E", BrandFg: "#2E3440",
	},
	withName(zincPalette, "zinc", "The original zinc and cyan palette"),
	func() Theme {
		t := withName(zincPalette, "terminal", "Zinc chrome, text in your terminal's own colours")
		t.ANSI16 = true
		return t
	}(),
	{
		Name: "tokyo-night", Description: "Cool indigo with blue and purple accents",
		Muted: "#565F89", Border: "#3B4261", Frame: "#3B4261", Text: "#A9B1D6", Value: "#C0CAF5",
		Accent: "#7AA2F7", Accent2: "#BB9AF7", Purple: "#9D7CD8",
		Ok: "#9ECE6A", Warn: "#E0AF68", Error: "#F7768E",
		Bg: "#16161E", CardBg: "#1A1B26", Track: "#292E42", BrandFg: "#1A1B26",
	},
	{
		Name: "catppuccin", Description: "Mocha pastels, mauve and pink",
		Muted: "#7F849C", Border: "#45475A", Frame: "#45475A", Text: "#BAC2DE", Value: "#CDD6F4",
		Accent: "#CBA6F7", Accent2: "#F5C2E7", Purple: "#B4BEFE",
		Ok: "#A6E3A1", Warn: "#F9E2AF", Error: "#F38BA8",
		Bg: "#11111B", CardBg: "#1E1E2E", Track: "#313244", BrandFg: "#1E1E2E",
	},
	{
		Name: "gruvbox", Description: "Warm retro earth tones",
		Muted: "#928374", Border: "#504945", Frame: "#504945", Text: "#D5C4A1", Value: "#EBDBB2",
		Accent: "#FE8019", Accent2: "#83A598", Purple: "#D3869B",
		Ok: "#B8BB26", Warn: "#FABD2F", Error: "#FB4934",
		Bg: "#1D2021", CardBg: "#282828", Track: "#3C3836", BrandFg: "#282828",
	},
	{
		Name: "dracula", Description: "Iconic high-contrast vampire dark palette",
		Muted: "#6272A4", Border: "#44475A", Frame: "#44475A", Text: "#F8F8F2", Value: "#FFFFFF",
		Accent: "#BD93F9", Accent2: "#FF79C6", Purple: "#BD93F9",
		Ok: "#50FA7B", Warn: "#F1FA8C", Error: "#FF5555",
		Bg: "#1E1F29", CardBg: "#282A36", Track: "#44475A", BrandFg: "#282A36",
	},
	{
		Name: "solarized-dark", Description: "Precision teal and blue contrast palette",
		Muted: "#657B83", Border: "#073642", Frame: "#586E75", Text: "#839496", Value: "#93A1A1",
		Accent: "#268BD2", Accent2: "#2AA198", Purple: "#6C71C4",
		Ok: "#859900", Warn: "#B58900", Error: "#DC322F",
		Bg: "#00212B", CardBg: "#002B36", Track: "#073642", BrandFg: "#002B36",
	},
	{
		Name: "monokai", Description: "Iconic vibrant code editor palette",
		Muted: "#75715E", Border: "#3E3D32", Frame: "#49483E", Text: "#F8F8F2", Value: "#FFFFFF",
		Accent: "#66D9EF", Accent2: "#FD971F", Purple: "#AE81FF",
		Ok: "#A6E22E", Warn: "#E6DB74", Error: "#F92672",
		Bg: "#1E1F1C", CardBg: "#272822", Track: "#3E3D32", BrandFg: "#272822",
	},
	{
		Name: "rose-pine", Description: "Minimalist warm dark aesthetic with pine and rose",
		Muted: "#6E6A86", Border: "#26233A", Frame: "#403D52", Text: "#E0DEF4", Value: "#F0EEF8",
		Accent: "#EBBCBA", Accent2: "#31748F", Purple: "#C4A7E7",
		Ok: "#9CCFD8", Warn: "#F6C177", Error: "#EB6F92",
		Bg: "#14121E", CardBg: "#191724", Track: "#26233A", BrandFg: "#191724",
	},
	{
		Name: "one-dark", Description: "Atom and VS Code balanced slate and pastel palette",
		Muted: "#5C6370", Border: "#3E4451", Frame: "#4B5263", Text: "#ABB2BF", Value: "#DCDFE4",
		Accent: "#61AFEF", Accent2: "#56B6C2", Purple: "#C678DD",
		Ok: "#98C379", Warn: "#E5C07B", Error: "#E06C75",
		Bg: "#1E2227", CardBg: "#282C34", Track: "#3E4451", BrandFg: "#282C34",
	},
	{
		Name: "github-dark", Description: "GitHub dark mode with sleek borders and blue accents",
		Muted: "#8B949E", Border: "#21262D", Frame: "#30363D", Text: "#C9D1D9", Value: "#F0F6FC",
		Accent: "#58A6FF", Accent2: "#79C0FF", Purple: "#BC8CFF",
		Ok: "#3FB950", Warn: "#D29922", Error: "#F85149",
		Bg: "#010409", CardBg: "#0D1117", Track: "#21262D", BrandFg: "#0D1117",
	},
	{
		Name: "synthwave", Description: "80s cyberpunk neon pink, cyan and electric glow",
		Muted: "#7B5EA7", Border: "#341C5B", Frame: "#502888", Text: "#E2E8F0", Value: "#FFFFFF",
		Accent: "#FF007F", Accent2: "#00F0FF", Purple: "#8B00FF",
		Ok: "#05FFA1", Warn: "#FFE600", Error: "#FF3366",
		Bg: "#140B24", CardBg: "#1A102F", Track: "#341C5B", BrandFg: "#1A102F",
	},
	{
		Name: "github-light", Description: "GitHub light mode, for light terminals", Light: true,
		Muted: "#6E7781", Border: "#D0D7DE", Frame: "#D0D7DE", Text: "#24292F", Value: "#1F2328",
		Accent: "#0969DA", Accent2: "#1B7C83", Purple: "#8250DF",
		Ok: "#1A7F37", Warn: "#9A6700", Error: "#CF222E",
		Bg: "#F6F8FA", CardBg: "#EAEEF2", Track: "#D0D7DE", BrandFg: "#FFFFFF",
	},
	{
		Name: "catppuccin-latte", Description: "Latte pastels, for light terminals", Light: true,
		Muted: "#8C8FA1", Border: "#BCC0CC", Frame: "#BCC0CC", Text: "#5C5F77", Value: "#4C4F69",
		Accent: "#8839EF", Accent2: "#EA76CB", Purple: "#7287FD",
		Ok: "#40A02B", Warn: "#DF8E1D", Error: "#D20F39",
		Bg: "#DCE0E8", CardBg: "#E6E9EF", Track: "#CCD0DA", BrandFg: "#EFF1F5",
	},
	{
		Name: "solarized-light", Description: "Solarized on its cream base, for light terminals", Light: true,
		Muted: "#93A1A1", Border: "#93A1A1", Frame: "#93A1A1", Text: "#657B83", Value: "#586E75",
		Accent: "#268BD2", Accent2: "#2AA198", Purple: "#6C71C4",
		Ok: "#859900", Warn: "#B58900", Error: "#DC322F",
		Bg: "#FDF6E3", CardBg: "#EEE8D5", Track: "#E4DDC8", BrandFg: "#FDF6E3",
	},
}

func withName(t Theme, name, description string) Theme {
	t.Name, t.Description = name, description
	return t
}

// colorRole indexes the escape sequences the printed-text helpers use.
type colorRole int

const (
	roleMuted colorRole = iota
	roleFrame
	roleText
	roleValue
	roleAccent
	roleAccent2
	rolePurple
	roleOk
	roleWarn
	roleError
	roleCount
)

// ansi16Codes is the original fixed mapping, used by the terminal theme and
// whenever output is not a colour-capable terminal.
var ansi16Codes = [roleCount]string{
	roleMuted: ansiGray, roleFrame: ansiGray, roleText: ansiWhite, roleValue: ansiBrightWhite,
	roleAccent: ansiBrightCyan, roleAccent2: ansiBrightBlue, rolePurple: ansiBrightPurple,
	roleOk: ansiBrightGreen, roleWarn: ansiBrightYellow, roleError: ansiBrightRed,
}

var (
	currentTheme Theme
	themeSeqs    [roleCount]string
	// themeFromPreference is set when the active theme came from the saved
	// preference, which the terminal's background never overrides.
	themeFromPreference bool
	// colorProfile reports what the terminal can display; tests override it.
	colorProfile = termenv.ColorProfile
)

func init() {
	_ = ApplyTheme(defaultThemeName)
}

// FindTheme looks a theme up by name, case-insensitively.
func FindTheme(name string) (Theme, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, t := range builtinThemes {
		if t.Name == name {
			return t, true
		}
	}
	return Theme{}, false
}

func themeNames() []string {
	names := make([]string, len(builtinThemes))
	for i, t := range builtinThemes {
		names[i] = t.Name
	}
	return names
}

// ApplyTheme makes name the active palette for everything rendered from now
// on. Text already printed keeps the colours it was printed with.
func ApplyTheme(name string) error {
	t, ok := FindTheme(name)
	if !ok {
		return fmt.Errorf("unknown theme %q (available: %s)", name, strings.Join(themeNames(), ", "))
	}
	currentTheme = t

	tuiColorCyan = lipgloss.Color(t.Accent)
	tuiColorBlue = lipgloss.Color(t.Accent2)
	tuiColorGreen = lipgloss.Color(t.Ok)
	tuiColorYellow = lipgloss.Color(t.Warn)
	tuiColorRed = lipgloss.Color(t.Error)
	tuiColorPurple = lipgloss.Color(t.Purple)
	tuiColorMuted = lipgloss.Color(t.Muted)
	tuiColorBg = lipgloss.Color(t.Bg)
	tuiColorCardBg = lipgloss.Color(t.CardBg)
	tuiColorBorder = lipgloss.Color(t.Border)
	tuiColorTrack = lipgloss.Color(t.Track)
	tuiColorWhite = lipgloss.Color(t.Value)
	tuiColorBrandFg = lipgloss.Color(t.BrandFg)
	buildStyles()
	diffBg = darkDiffBackgrounds
	if t.Light {
		diffBg = lightDiffBackgrounds
	}
	if t.DiffBackgrounds != nil {
		diffBg = *t.DiffBackgrounds
	}

	hexes := [roleCount]string{
		roleMuted: t.Muted, roleFrame: t.Frame, roleText: t.Text, roleValue: t.Value,
		roleAccent: t.Accent, roleAccent2: t.Accent2, rolePurple: t.Purple,
		roleOk: t.Ok, roleWarn: t.Warn, roleError: t.Error,
	}
	profile := colorProfile()
	// The editor's selection sits on the border colour, as the files
	// browser's selected row does.
	selectionBg = defaultSelectionBg
	if !t.ANSI16 && profile != termenv.Ascii {
		if c := profile.Color(t.Border); c != nil {
			if seq := c.Sequence(true); seq != "" {
				selectionBg = "\033[" + seq + "m"
			}
		}
	}
	for role := colorRole(0); role < roleCount; role++ {
		themeSeqs[role] = ansi16Codes[role]
		if t.ANSI16 || profile == termenv.Ascii {
			continue
		}
		if c := profile.Color(hexes[role]); c != nil {
			if seq := c.Sequence(false); seq != "" {
				themeSeqs[role] = "\033[" + seq + "m"
			}
		}
	}
	return nil
}

// themePreferences is ~/.fastllm/preferences.json. UI preferences live apart
// from config.json, which can be per-project and is rewritten wholesale by
// config.SaveSettings.
type themePreferences struct {
	Theme string `json:"theme"`
}

// preferencesPath is a var so tests can point it at a temp directory.
var preferencesPath = func() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".fastllm", "preferences.json")
}

// LoadThemePreference applies the saved theme, falling back to the default
// when none is saved or the saved name is unknown.
func LoadThemePreference() {
	name := defaultThemeName
	themeFromPreference = false
	if path := preferencesPath(); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			var prefs themePreferences
			if json.Unmarshal(data, &prefs) == nil {
				if _, ok := FindTheme(prefs.Theme); ok {
					name = prefs.Theme
					themeFromPreference = true
				}
			}
		}
	}
	_ = ApplyTheme(name)
}

// SaveThemePreference records name as the theme to load at startup.
func SaveThemePreference(name string) error {
	path := preferencesPath()
	if path == "" {
		return fmt.Errorf("cannot locate the home directory")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(themePreferences{Theme: name}, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, "preferences-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if _, err := temp.Write(append(data, '\n')); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	// Windows does not replace an existing destination with os.Rename.
	if err := os.Rename(tempName, path); err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			return err
		}
		return os.Rename(tempName, path)
	}
	return nil
}
