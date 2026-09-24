package harness

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestSlashCommandListDerivation(t *testing.T) {
	byName := map[string]slashCommand{}
	for _, cmd := range slashCommandList() {
		if _, dup := byName[cmd.Name]; dup {
			t.Fatalf("%s derived twice", cmd.Name)
		}
		byName[cmd.Name] = cmd
	}
	if c := byName["/c"]; len(c.Aliases) != 1 || c.Aliases[0] != "/clear" || c.NeedsArg {
		t.Fatalf("/c, /clear = %+v", c)
	}
	if byName["/rename"].NeedsArg != true || byName["/resume"].NeedsArg != false {
		t.Fatal("NeedsArg must follow <required> vs [optional]")
	}
	// First row wins: "/set" (no argument) describes /set, not "/set turns <1-100>".
	if byName["/set"].Usage != "/set" {
		t.Fatalf("/set usage = %q", byName["/set"].Usage)
	}
	for _, name := range []string{"!<cmd>,", "<cmd>", "Ctrl+B"} {
		if _, ok := byName[name]; ok {
			t.Fatalf("%s is help-only and must not be a command", name)
		}
	}
}

// TestHandledCommandsAreInTheTable guards against the drift this table was
// introduced to fix: every slash command the TUI handles must be listed, so
// it shows in /help and in the suggestions.
func TestHandledCommandsAreInTheTable(t *testing.T) {
	known := map[string]bool{}
	for _, name := range slashCommandNames() {
		known[name] = true
	}
	caseRe := regexp.MustCompile(`case ("/[a-z-]+"(?:, "/[a-z-]+")*):`)
	nameRe := regexp.MustCompile(`"(/[a-z-]+)"`)
	for _, file := range []string{"tui_tea.go", "tui_session.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range caseRe.FindAllStringSubmatch(string(src), -1) {
			for _, n := range nameRe.FindAllStringSubmatch(c[1], -1) {
				if !known[n[1]] {
					t.Errorf("%s handles %s but it is missing from commandSections", file, n[1])
				}
			}
		}
	}
}

func TestMatchCommandsRanking(t *testing.T) {
	got := matchCommands("/sess")
	if len(got) < 2 || got[0].Insert != "/sessions" {
		t.Fatalf("prefix match should come first: %+v", got)
	}
	var sawDelete bool
	for _, s := range got {
		if s.Insert == "/delete-session" {
			sawDelete = true
		}
	}
	if !sawDelete {
		t.Fatal("/sess should also find /delete-session by substring")
	}
	if got := matchCommands("/yan"); len(got) != 1 || got[0].Insert != "/yank" || got[0].Command != "/copy" {
		t.Fatalf("an alias prefix should insert the alias: %+v", got)
	}
	if all := matchCommands("/"); len(all) != len(slashCommandList()) {
		t.Fatalf("a bare / should list every command, got %d", len(all))
	}
	if strings.Join(slashCommands, ",") != strings.Join(slashCommandNames(), ",") {
		t.Fatal("legacy completion must derive from the table")
	}
}
