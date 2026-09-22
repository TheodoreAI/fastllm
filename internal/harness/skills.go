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

var skillDirectories = []string{
	filepath.Join(".agents", "skills"),
	filepath.Join(".claude", "skills"),
}

// DiscoverWorkspaceSkills finds skills in the working directory and its parents,
// stopping at the repository root. Nearest definitions win, with .agents taking
// precedence over .claude when both define the same skill.
func DiscoverWorkspaceSkills(workingDir string) []Skill {
	absDir, err := filepath.Abs(workingDir)
	if err != nil {
		return nil
	}

	byName := make(map[string]Skill)
	for current := absDir; ; current = filepath.Dir(current) {
		for _, relRoot := range skillDirectories {
			root := filepath.Join(current, relRoot)
			entries, err := os.ReadDir(root)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				path := filepath.Join(root, entry.Name(), "SKILL.md")
				info, err := os.Lstat(path)
				if err != nil || !info.Mode().IsRegular() || info.Size() > 512*1024 {
					continue
				}
				content, err := os.ReadFile(path)
				if err != nil || strings.TrimSpace(string(content)) == "" {
					continue
				}
				name, description := skillMetadata(string(content), entry.Name())
				key := strings.ToLower(name)
				if _, exists := byName[key]; exists {
					continue
				}
				byName[key] = Skill{Name: name, Description: description, Path: path, Content: string(content)}
			}
		}

		if isRepoRoot(current) {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
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
		return "No project skills found under .agents/skills or .claude/skills."
	}
	var b strings.Builder
	b.WriteString("Project Skills\n")
	for _, skill := range skills {
		description := skill.Description
		if description == "" {
			description = "No description"
		}
		fmt.Fprintf(&b, "  %-24s %s\n", skill.Name, description)
	}
	b.WriteString("\nUse /skills <name> to inspect or /skills <name> <task> to run.")
	return b.String()
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
