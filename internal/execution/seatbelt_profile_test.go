package execution

import (
	"strings"
	"testing"
)

// These run on every platform: the profile is text, and its rules can be
// checked without macOS. seatbelt_darwin_test.go proves they hold on a Mac.

func TestSeatbeltProfileDeniesByDefault(t *testing.T) {
	profile := seatbeltProfile(2)
	if !strings.HasPrefix(profile, "(version 1)\n(deny default)") {
		t.Fatalf("profile must start by denying everything:\n%s", profile)
	}
	for _, line := range strings.Split(profile, "\n") {
		rule := strings.TrimSpace(line)
		if strings.HasPrefix(rule, ";") {
			continue // comments
		}
		if strings.Contains(rule, "network") {
			t.Fatalf("no rule may mention the network, found %q", rule)
		}
		if strings.Contains(rule, "(allow default)") || rule == "(allow file-write*)" || rule == "(allow file-read*)" {
			t.Fatalf("an unrestricted allow: %q", rule)
		}
	}
	if strings.Count(profile, "(") != strings.Count(profile, ")") {
		t.Fatal("unbalanced parentheses")
	}
}

func TestSeatbeltWritesOnlyWorkspaceAndScratch(t *testing.T) {
	profile := seatbeltProfile(1)
	start := strings.Index(profile, "(allow file-write*")
	end := strings.Index(profile[start:], "\n\n") + start // the rule ends at a blank line
	block := profile[start:end]
	for _, want := range []string{`(param "WORKSPACE")`, `(param "SCRATCH")`, `"/dev/null"`} {
		if !strings.Contains(block, want) {
			t.Fatalf("write block lacks %s:\n%s", want, block)
		}
	}
	for _, forbidden := range []string{"READ_", `"/usr"`, `"/Library"`, "/Users", "HOME"} {
		if strings.Contains(block, forbidden) {
			t.Fatalf("write block grants %s:\n%s", forbidden, block)
		}
	}
	// Reads: the home directory is never granted by the profile itself.
	if strings.Contains(profile, "/Users") || strings.Contains(profile, `"/private/var/folders"`) {
		t.Fatal("the profile must not grant the user's home or temp by name")
	}
}

// Paths travel as -D parameters: a hostile workspace name cannot add rules.
func TestSeatbeltPathsAreParametersNotProfileText(t *testing.T) {
	hostile := `/Users/x/proj")(allow default)(allow network*`
	args := seatbeltArgs(hostile, "/private/tmp/scratch", []string{"/opt/go"}, []string{"/bin/sh", "-c", "echo hi"})
	var profile string
	for i, a := range args {
		if a == "-p" {
			profile = args[i+1]
		}
	}
	if strings.Contains(profile, hostile) || strings.Contains(profile, "allow default") {
		t.Fatal("a path reached the profile text")
	}
	joined := strings.Join(args, "\x00")
	for _, want := range []string{"-D\x00WORKSPACE=" + hostile, "-D\x00SCRATCH=/private/tmp/scratch", "-D\x00READ_0=/opt/go"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args lack %q: %q", want, args)
		}
	}
	if tail := args[len(args)-3:]; tail[0] != "/bin/sh" || tail[2] != "echo hi" {
		t.Fatalf("the command must come last: %q", tail)
	}
	if !strings.Contains(profile, `(param "READ_0")`) || strings.Contains(profile, `(param "READ_1")`) {
		t.Fatal("one toolchain path means exactly one READ parameter")
	}
}
