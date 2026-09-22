package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Skill is a project-local set of instructions loaded from a SKILL.md file.
type Skill struct {
	Name        string
	Description string
	Path        string
	Content     string
}

type skillSource struct {
	path      string
	flatFiles bool
}

var projectSkillSources = []skillSource{
	{path: filepath.Join(".agents", "skills")},
	{path: filepath.Join(".claude", "skills")},
	{path: filepath.Join(".codex", "skills")},
	{path: filepath.Join(".opencode", "skills"), flatFiles: true},
	{path: filepath.Join(".agent", "skills")},
}

var globalSkillSources = []skillSource{
	{path: filepath.Join(".agents", "skills")},
	{path: filepath.Join(".claude", "skills")},
	{path: filepath.Join(".codex", "skills")},
	{path: filepath.Join(".config", "opencode", "skills"), flatFiles: true},
	{path: filepath.Join(".opencode", "skills"), flatFiles: true},
	{path: filepath.Join(".agent", "skills")},
}

// DiscoverWorkspaceSkills finds skills in the working directory and its parents,
// stopping at the repository root, then adds user-level skills. Project-local
// definitions win over global ones; within a scope, sources are ordered by the
// lists above and the nearest project definition wins.
func DiscoverWorkspaceSkills(workingDir string) []Skill {
	homeDir, _ := os.UserHomeDir()
	return discoverSkills(workingDir, homeDir)
}

func discoverSkills(workingDir, homeDir string) []Skill {
	absDir, err := filepath.Abs(workingDir)
	if err != nil {
		return nil
	}

	byName := make(map[string]Skill)
	for current := absDir; ; current = filepath.Dir(current) {
		for _, source := range projectSkillSources {
			discoverSkillSource(filepath.Join(current, source.path), source.flatFiles, byName)
		}

		if isRepoRoot(current) {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	if homeDir != "" {
		for _, source := range globalSkillSources {
			discoverSkillSource(filepath.Join(homeDir, source.path), source.flatFiles, byName)
		}
	}

	skills := make([]Skill, 0, len(byName))
	for _, skill := range byName {
		skills = append(skills, skill)
	}
	sort.Slice(skills, func(i, j int) bool {
		return strings.ToLower(skills[i].Name) < strings.ToLower(skills[j].Name)
	})
	return skills
}

func discoverSkillSource(root string, flatFiles bool, byName map[string]Skill) {
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if path != root && entry.Type()&os.ModeSymlink != 0 {
				return filepath.SkipDir
			}
			return nil
		}
		isBundle := entry.Name() == "SKILL.md"
		isFlat := flatFiles && filepath.Dir(path) == root && strings.EqualFold(filepath.Ext(path), ".md")
		if !isBundle && !isFlat {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 512*1024 {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil || strings.TrimSpace(string(content)) == "" {
			return nil
		}
		fallbackName := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if isBundle {
			fallbackName = filepath.Base(filepath.Dir(path))
		}
		name, description := skillMetadata(string(content), fallbackName)
		key := strings.ToLower(name)
		if _, exists := byName[key]; !exists {
			byName[key] = Skill{Name: name, Description: description, Path: path, Content: string(content)}
		}
		return nil
	})
}

func skillMetadata(content, fallbackName string) (string, string) {
	name := fallbackName
	description := ""
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return name, description
	}
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "---" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = unquoteSkillMetadata(strings.TrimSpace(value))
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "name":
			if value != "" {
				name = value
			}
		case "description":
			description = value
		}
	}
	return name, description
}

func unquoteSkillMetadata(value string) string {
	if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
		return strings.ReplaceAll(value[1:len(value)-1], "''", "'")
	}
	if unquoted, err := strconv.Unquote(value); err == nil {
		return unquoted
	}
	return value
}

func findSkill(skills []Skill, name string) (Skill, bool) {
	for _, skill := range skills {
		if strings.EqualFold(skill.Name, name) {
			return skill, true
		}
	}
	return Skill{}, false
}

func parseSkillInvocation(input string) (name, task string) {
	fields := strings.Fields(input)
	if len(fields) < 2 {
		return "", ""
	}
	return fields[1], strings.Join(fields[2:], " ")
}

func formatSkillList(skills []Skill) string {
	if len(skills) == 0 {
		return "No project or global skills found."
	}
	var b strings.Builder
	b.WriteString("Available Skills\n")
	for _, skill := range skills {
		description := skillSummary(skill.Description, 160)
		fmt.Fprintf(&b, "  %-24s %s\n", skill.Name, description)
	}
	b.WriteString("\nUse /skills NAME to inspect or /skills NAME TASK to run.")
	return b.String()
}

func formatSkillCatalogPrompt(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n# Available Skills\n")
	b.WriteString("The following skill metadata is available in this session. Full instructions are not loaded unless the user invokes `/skills NAME TASK`. If asked what skills are available, answer from this list.\n")
	for _, skill := range skills {
		description := skillSummary(skill.Description, 160)
		fmt.Fprintf(&b, "- %s: %s\n", skill.Name, description)
	}
	return strings.TrimRight(b.String(), "\n")
}

func skillSummary(description string, limit int) string {
	description = strings.Join(strings.Fields(description), " ")
	if description == "" {
		return "No description"
	}
	runes := []rune(description)
	if limit > 3 && len(runes) > limit {
		return string(runes[:limit-3]) + "..."
	}
	return description
}

func formatSkillDetails(skill Skill, workingDir string) string {
	path, err := filepath.Rel(workingDir, skill.Path)
	if err != nil {
		path = skill.Path
	}
	description := skill.Description
	if description == "" {
		description = "No description"
	}
	return fmt.Sprintf("Skill: %s\nDescription: %s\nSource: %s", skill.Name, description, path)
}

func formatSkillPrompt(skill Skill, workingDir string) string {
	path, err := filepath.Rel(workingDir, skill.Path)
	if err != nil {
		path = skill.Path
	}
	return fmt.Sprintf("\n\n# Active Skill: %s\nFollow these instructions for this task only. The skill source is %s.\n\n%s", skill.Name, path, strings.TrimSpace(skill.Content))
}
