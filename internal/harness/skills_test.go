package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fastllm/internal/llm"
)

func writeTestSkill(t *testing.T, root, source, dirName, content string) string {
	t.Helper()
	path := filepath.Join(root, source, "skills", dirName, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDiscoverWorkspaceSkillsDeduplicatesByNearestPrecedence(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "nested")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestSkill(t, root, ".claude", "review", "---\nname: review\ndescription: parent copy\n---\nparent")
	wantPath := writeTestSkill(t, child, ".agents", "review-local", "---\nname: review\ndescription: nearest copy\n---\nnearest")
	writeTestSkill(t, root, ".agents", "testing", "---\nname: testing\ndescription: 'Run focused tests'\n---\ntest")

	skills := discoverSkills(child, t.TempDir())
	if len(skills) != 2 {
		t.Fatalf("skills = %#v; want 2", skills)
	}
	review, ok := findSkill(skills, "REVIEW")
	if !ok || review.Path != wantPath || review.Description != "nearest copy" {
		t.Fatalf("review skill = %#v", review)
	}
	if skills[0].Name != "review" || skills[1].Name != "testing" {
		t.Fatalf("skills are not sorted: %#v", skills)
	}
}

func TestDiscoverSkillsSupportsCodexOpenCodeAndLegacyAGYSources(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestSkill(t, root, ".codex", "codex-project", "---\nname: codex-project\ndescription: Codex project skill\n---\nproject")
	writeTestSkill(t, root, ".opencode", "opencode-project", "---\nname: opencode-project\ndescription: OpenCode project skill\n---\nproject")
	writeTestSkill(t, home, ".codex", "codex-global", "---\nname: codex-global\ndescription: Codex global skill\n---\nglobal")
	writeTestSkill(t, home, ".agent", "agy-legacy", "---\nname: agy-legacy\ndescription: Legacy AGY skill\n---\nglobal")
	flatPath := filepath.Join(home, ".config", "opencode", "skills", "flat-open-code.md")
	if err := os.MkdirAll(filepath.Dir(flatPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(flatPath, []byte("---\nname: flat-open-code\ndescription: Flat OpenCode skill\n---\nflat"), 0o644); err != nil {
		t.Fatal(err)
	}

	skills := discoverSkills(root, home)
	for _, name := range []string{"codex-project", "opencode-project", "codex-global", "agy-legacy", "flat-open-code"} {
		if _, ok := findSkill(skills, name); !ok {
			t.Errorf("missing %s in %#v", name, skills)
		}
	}
}

func TestSkillInvocationAndPrompt(t *testing.T) {
	name, task := parseSkillInvocation("/skills clean-code refactor the parser")
	if name != "clean-code" || task != "refactor the parser" {
		t.Fatalf("parse = %q, %q", name, task)
	}
	skill := Skill{Name: "clean-code", Path: filepath.Join("project", ".agents", "skills", "clean-code", "SKILL.md"), Content: "Keep functions small."}
	prompt := formatSkillPrompt(skill, "project")
	if !strings.Contains(prompt, "Active Skill: clean-code") || !strings.Contains(prompt, "Keep functions small.") {
		t.Fatalf("prompt = %q", prompt)
	}
	catalog := formatSkillCatalogPrompt([]Skill{{Name: "clean-code", Description: "Keep code maintainable"}})
	if !strings.Contains(catalog, "clean-code: Keep code maintainable") || !strings.Contains(catalog, "instructions are not loaded") {
		t.Fatalf("catalog prompt = %q", catalog)
	}
}

func TestSkillsListAliasDoesNotStartAgentTurn(t *testing.T) {
	m := &teaModel{skills: []Skill{{Name: "review", Description: "Review code"}}}
	if cmd := m.handleAgentSubmit("/skills list"); cmd != nil {
		t.Fatal("/skills list returned an asynchronous command")
	}
	if m.isExecuting {
		t.Fatal("/skills list started an agent turn")
	}
	if !m.skillsModal {
		t.Fatal("/skills list did not open the skills modal")
	}
}

type skillCaptureLLM struct {
	messages chan []llm.Message
}

func (c *skillCaptureLLM) Chat(_ context.Context, _ string, messages []llm.Message, _ []llm.Tool, _ string) (llm.Message, error) {
	c.messages <- append([]llm.Message(nil), messages...)
	return llm.Message{Role: "assistant", Content: "done"}, nil
}

func TestTeaSkillInvocationAppliesInstructionsForTurn(t *testing.T) {
	client := &skillCaptureLLM{messages: make(chan []llm.Message, 1)}
	m := newBusyModel(t, client)
	m.skills = []Skill{{
		Name: "review", Description: "Review code", Path: filepath.Join(m.workingDir, ".agents", "skills", "review", "SKILL.md"),
		Content: "Check every returned error.",
	}}

	if cmd := m.handleAgentSubmit("/skills review inspect the parser"); cmd == nil {
		t.Fatal("skill invocation did not start a turn")
	}
	collectFinishEvents(t, m)

	messages := <-client.messages
	if !strings.Contains(messages[0].Content, "Active Skill: review") || !strings.Contains(messages[0].Content, "Check every returned error.") {
		t.Fatalf("system prompt does not contain skill instructions: %q", messages[0].Content)
	}
	if got := messages[len(messages)-1].Content; got != "inspect the parser" {
		t.Fatalf("task = %q", got)
	}
	if m.pendingPrompt != "inspect the parser" {
		t.Fatalf("pending prompt = %q", m.pendingPrompt)
	}
}
