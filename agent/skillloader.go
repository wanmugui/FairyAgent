package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type Skill struct {
	Name    string
	Content string
}

// LoadSkills loads all skills from skills_dir, recursing into subdirectories.
// Each subdirectory is loaded as a skill (directory name = skill name).
func LoadSkills(skillsDir string) ([]Skill, error) {
	if skillsDir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil, nil
	}

	var skills []Skill
	for _, e := range entries {
		if e.IsDir() {
			// Load skill from subdirectory (dir name = skill name)
			subDir := filepath.Join(skillsDir, e.Name())
			subSkills, _ := loadSkillsFromDir(subDir, e.Name())
			skills = append(skills, subSkills...)
			continue
		}
		// Top-level .md files (backward compat)
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(skillsDir, name))
		if err != nil {
			continue
		}
		skillName := strings.TrimSuffix(name, ".md")
		content := strings.TrimSpace(string(data))
		if content != "" {
			skills = append(skills, Skill{Name: skillName, Content: content})
		}
	}
	return skills, nil
}

// loadSkillsFromDir loads skill .md files from a subdirectory.
func loadSkillsFromDir(dir string, dirName string) ([]Skill, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var skills []Skill
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			continue
		}
		// Use directory name as skill name
		skillName := dirName
		if !strings.EqualFold(name, "SKILL.md") {
			skillName = dirName + "." + strings.TrimSuffix(name, ".md")
		}
		skills = append(skills, Skill{Name: skillName, Content: content})
	}
	return skills, nil
}

// DiscoverSkillRegistry walks the skills root and builds a registry of
// SKILL.md / plugin.json / manifest.json entries without requiring the
// config file to list them.
func DiscoverSkillRegistry(skillsDir string) []SkillReg {
	root := strings.TrimSpace(skillsDir)
	if root == "" {
		return nil
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil
	}
	var registry []SkillReg
	seen := map[string]bool{}
	_ = filepath.WalkDir(absRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return nil
		}
		base := strings.ToLower(entry.Name())
		if base != "skill.md" && base != "plugin.json" && base != "manifest.json" {
			return nil
		}
		relative, relErr := filepath.Rel(absRoot, path)
		if relErr != nil {
			return nil
		}
		dirRel := filepath.ToSlash(filepath.Dir(relative))
		name := filepath.Base(filepath.Dir(relative))
		if dirRel == "." {
			name = strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		}
		if name == "" || seen[name] {
			return nil
		}
		seen[name] = true
		location := "local:///skills/" + filepath.ToSlash(relative)
		registry = append(registry, readSkillReg(path, name, location))
		return nil
	})
	sort.SliceStable(registry, func(i, j int) bool { return registry[i].Name < registry[j].Name })
	return registry
}

func mergeSkillRegistries(configured, discovered []SkillReg) []SkillReg {
	byName := make(map[string]SkillReg, len(discovered))
	for _, skill := range discovered {
		byName[strings.ToLower(skill.Name)] = skill
	}
	merged := make([]SkillReg, 0, len(configured)+len(discovered))
	seen := map[string]bool{}
	disabled := map[string]bool{}
	for _, skill := range configured {
		key := strings.ToLower(skill.Name)
		seen[key] = true
		if skill.Enabled != nil && !*skill.Enabled {
			disabled[key] = true
			continue
		}
		if disk, ok := byName[key]; ok {
			skill = mergeSkillMetadata(skill, disk)
		}
		merged = append(merged, skill)
	}
	for _, skill := range discovered {
		key := strings.ToLower(skill.Name)
		if seen[key] || disabled[key] {
			continue
		}
		seen[key] = true
		merged = append(merged, skill)
	}
	return merged
}

func mergeSkillMetadata(base, extra SkillReg) SkillReg {
	if strings.TrimSpace(base.Description) == "" {
		base.Description = extra.Description
	}
	if strings.TrimSpace(base.Location) == "" {
		base.Location = extra.Location
	}
	if len(base.Tags) == 0 {
		base.Tags = append([]string(nil), extra.Tags...)
	}
	if len(base.Triggers) == 0 {
		base.Triggers = append([]string(nil), extra.Triggers...)
	}
	if !base.Always {
		base.Always = extra.Always
	}
	if base.Priority == 0 {
		base.Priority = extra.Priority
	}
	return base
}

func readSkillReg(path, name, location string) SkillReg {
	reg := SkillReg{Name: name, Location: location}
	data, err := os.ReadFile(path)
	if err != nil {
		return reg
	}
	content := strings.TrimPrefix(string(data), "\uFEFF")
	if strings.HasSuffix(strings.ToLower(path), ".json") {
		var meta map[string]any
		if json.Unmarshal([]byte(content), &meta) == nil {
			reg.Description = stringMapValue(meta, "description")
			reg.Tags = anyStringList(meta["tags"])
			reg.Triggers = anyStringList(meta["triggers"])
			reg.Always, _ = meta["always"].(bool)
			if priority, ok := meta["priority"].(float64); ok {
				reg.Priority = int(priority)
			}
		}
		return reg
	}
	front := frontmatterBlock(content)
	reg.Description = frontmatterScalar(front, "description")
	reg.Tags = frontmatterList(front, "tags")
	reg.Triggers = frontmatterList(front, "triggers")
	reg.Always = frontmatterBool(front, "always")
	reg.Priority = frontmatterInt(front, "priority")
	if reg.Description == "" {
		reg.Description = fallbackSkillDescription(content)
	}
	return reg
}

func skillDescription(path string) string {
	return readSkillReg(path, "", "").Description
}

func frontmatterBlock(content string) string {
	text := strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return ""
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return strings.Join(lines[1:i], "\n")
		}
	}
	return ""
}

func frontmatterScalar(front, key string) string {
	for _, line := range strings.Split(front, "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), key) {
			continue
		}
		return trimYAMLValue(value)
	}
	return ""
}

func frontmatterBool(front, key string) bool {
	value := strings.ToLower(frontmatterScalar(front, key))
	return value == "true" || value == "yes" || value == "1"
}

func frontmatterInt(front, key string) int {
	value, _ := strconv.Atoi(frontmatterScalar(front, key))
	return value
}

func frontmatterList(front, key string) []string {
	lines := strings.Split(front, "\n")
	for i, line := range lines {
		name, value, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), key) {
			continue
		}
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
			return splitYAMLList(strings.TrimSuffix(strings.TrimPrefix(value, "["), "]"))
		}
		if value != "" {
			return splitYAMLList(value)
		}
		var out []string
		for j := i + 1; j < len(lines); j++ {
			item := strings.TrimSpace(lines[j])
			if item == "" {
				continue
			}
			if !strings.HasPrefix(item, "-") {
				break
			}
			if value := trimYAMLValue(strings.TrimSpace(strings.TrimPrefix(item, "-"))); value != "" {
				out = append(out, value)
			}
		}
		return out
	}
	return nil
}

func splitYAMLList(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if item := trimYAMLValue(part); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func trimYAMLValue(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"'`)
	return strings.TrimSpace(value)
}

func fallbackSkillDescription(content string) string {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "---") {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			return strings.TrimSpace(strings.TrimLeft(trimmed, "# "))
		}
		if strings.Contains(trimmed, ":") && !strings.HasPrefix(trimmed, "-") {
			continue
		}
		return trimmed
	}
	return ""
}

func stringMapValue(meta map[string]any, key string) string {
	if value, ok := meta[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func anyStringList(value any) []string {
	switch list := value.(type) {
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, strings.TrimSpace(text))
			}
		}
		return out
	case []string:
		return append([]string(nil), list...)
	case string:
		if text := strings.TrimSpace(list); text != "" {
			return []string{text}
		}
	}
	return nil
}
