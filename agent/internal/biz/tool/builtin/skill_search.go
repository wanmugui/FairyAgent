package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"agentloop/agent/internal/biz/tool/shared"
	"sort"
	"strings"
	"unicode"
)

type skillEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Location    string `json:"location"`
	Snippet     string `json:"snippet"`
	Score       int    `json:"score"`
}

func NewLocalSkillSearchTool(schema ToolDef, skillsRoot string) Tool {
	return newLocalStructuredTool("skill_search", schema, func(ctx context.Context, invocation ToolInvocation) (ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return ToolResult{}, err
		}
		args, err := decodeLocalToolArgs(invocation)
		if err != nil {
			return localErrorResult("skill_search", err), nil
		}
		query := strings.TrimSpace(localStringArg(args, "query"))
		limit := localIntArg(args, "limit", 10)
		if limit <= 0 {
			limit = 10
		}
		if strings.TrimSpace(skillsRoot) == "" {
			return localErrorResult("skill_search", fmt.Errorf("skills directory is not configured")), nil
		}

		entries := discoverSkillEntries(skillsRoot)
		hits := make([]skillEntry, 0, len(entries))
		for _, entry := range entries {
			score := skillSearchScore(query, entry.Name, entry.Description, entry.Location)
			if query != "" && score <= 0 {
				continue
			}
			entry.Score = score
			hits = append(hits, entry)
		}
		sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
		if len(hits) > limit {
			hits = hits[:limit]
		}
		return ToolResult{Value: map[string]any{
			"query":   query,
			"results": hits,
			"count":   len(hits),
		}}, nil
	})
}

func discoverSkillEntries(skillsRoot string) []skillEntry {
	roots := shared.SplitRoots(skillsRoot)
	var entries []skillEntry
	seen := map[string]bool{}
	for _, root := range roots {
		absRoot, err := filepath.Abs(root)
		if err != nil {
			continue
		}
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
			// The configured root keeps the logical local:///skills/... form.
			// Roots outside it must carry a real absolute location, otherwise the
			// agent is handed a path that does not exist.
			location := "local:///skills/" + filepath.ToSlash(relative)
			if root != roots[0] {
				location = "local://" + filepath.ToSlash(path)
			}
			description, snippet := readSkillMeta(path)
			entries = append(entries, skillEntry{Name: name, Description: description, Location: location, Snippet: snippet})
			return nil
		})
	}
	return entries
}

func readSkillMeta(path string) (string, string) {
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		return "", ""
	}
	content := string(data)
	if strings.HasSuffix(strings.ToLower(path), ".json") {
		var meta map[string]any
		if json.Unmarshal(data, &meta) == nil {
			desc, _ := meta["description"].(string)
			return strings.TrimSpace(desc), skillSnippet(content)
		}
	}
	lines := strings.Split(content, "\n")
	// SKILL.md frontmatter is YAML, not prose: only the description value belongs
	// in the search text. Concatenating every "key: value" row made name/tags/
	// triggers noise that polluted skillSearchScore for every local skill.
	if front, ok := skillFrontmatterLines(lines); ok {
		for _, line := range front {
			value, found := strings.CutPrefix(strings.TrimSpace(line), "description:")
			if !found {
				continue
			}
			desc := strings.TrimSpace(value)
			desc = strings.Trim(desc, `"'`)
			if desc != "" {
				return desc, skillSnippet(content)
			}
		}
	}
	var description strings.Builder
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if description.Len() > 0 {
				break
			}
			continue
		}
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "---") {
			continue
		}
		description.WriteString(trimmed)
		description.WriteString(" ")
		if description.Len() > 400 {
			break
		}
	}
	desc := strings.TrimSpace(description.String())
	if desc == "" {
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				desc = strings.TrimSpace(strings.TrimLeft(trimmed, "# "))
				break
			}
		}
	}
	return desc, skillSnippet(content)
}

// skillFrontmatterLines returns the lines inside a leading "---" fenced YAML
// block. ok is false when the file has no frontmatter, so callers fall back to
// scanning the body.
func skillFrontmatterLines(lines []string) ([]string, bool) {
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, false
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return lines[1:i], true
		}
	}
	return nil, false
}

func skillSnippet(content string) string {
	if len(content) > 240 {
		return content[:240]
	}
	return content
}

func skillSearchScore(query, name, description, location string) int {
	score := 0
	lowerName := strings.ToLower(name)
	lowerDesc := strings.ToLower(description)
	lowerLoc := strings.ToLower(location)
	if query != "" {
		if strings.Contains(lowerDesc, query) {
			score += 6
		}
		if strings.Contains(lowerName, query) {
			score += 5
		}
		if strings.Contains(lowerLoc, query) {
			score += 2
		}
	}
	for _, term := range strings.FieldsFunc(query, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		term = strings.ToLower(term)
		if term == "" {
			continue
		}
		if strings.Contains(lowerName, term) {
			score += 3
		}
		score += strings.Count(lowerDesc, term)
	}
	return score
}
