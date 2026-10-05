package builtin

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

func NewLocalGlobTool(schema ToolDef) Tool {
	return NewLocalGlobToolWithConfig(schema, "")
}

func NewLocalGlobToolWithConfig(schema ToolDef, skillsRoot string) Tool {
	return NewLocalGlobToolWithConfigAndMemory(schema, skillsRoot, "")
}

func NewLocalGlobToolWithConfigAndMemory(schema ToolDef, skillsRoot, memoryRoot string) Tool {
	return newLocalStructuredTool("glob", schema, func(ctx context.Context, invocation ToolInvocation) (ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return ToolResult{}, err
		}
		args, err := decodeLocalToolArgs(invocation)
		if err != nil {
			return localErrorResult("glob", err), nil
		}
		pattern := localStringArg(args, "pattern")
		if pattern == "" {
			return localErrorResult("glob", fmt.Errorf("pattern is required")), nil
		}
		localContext, err := localToolContext(invocation)
		if err != nil {
			return localErrorResult("glob", err), nil
		}
		searchRoot := localStringArg(args, "path")
		if searchRoot == "" {
			searchRoot = "."
		}
		root, rootKind, err := resolveLocalReadablePathWithMemory(localContext.Workspace, skillsRoot, memoryRoot, searchRoot)
		if err != nil {
			return localErrorResult("glob", err), nil
		}
		matches, err := localGlobMatches(root, pattern)
		if err != nil {
			return localErrorResult("glob", err), nil
		}
		for index, match := range matches {
			matches[index] = localReadableResultPathWithMemory(rootKind, localContext.Workspace, skillsRoot, memoryRoot, match)
		}
		result := map[string]any{
			"ok":      true,
			"matches": matches,
			"count":   len(matches),
		}
		if len(matches) == 0 {
			result["hint"] = "0 matches. Do NOT give up or ask the user yet: retry with a broader/case-insensitive pattern, try .js/.jsx/.ts/.tsx/.vue variants (use {a,b}), switch the search root (absolute path, e.g. the project root), or use grep on likely directories."
		}
		return ToolResult{Value: result}, nil
	})
}

func localGlobMatches(root, pattern string) ([]string, error) {
	pattern = strings.TrimSpace(pattern)
	for _, prefix := range []string{"local://", "memory://", "knowledge://"} {
		if strings.HasPrefix(strings.ToLower(pattern), prefix) {
			pattern = pattern[len(prefix):]
			break
		}
	}
	pattern = filepath.ToSlash(pattern)
	if err := validateRelativeGlobPattern(pattern); err != nil {
		return nil, err
	}
	// filepath.Match does not support {a,b} alternation; expand it first.
	patterns := expandGlobBraces(pattern)
	all := make([]string, 0)
	for _, p := range patterns {
		ms, err := globMatchOne(root, p, false)
		if err != nil {
			return nil, err
		}
		all = append(all, ms...)
	}
	// Windows/mixed-case tolerance: retry case-insensitively when nothing
	// matched (e.g. "sidebar*" vs "Sidebar.jsx").
	if len(all) == 0 {
		for _, p := range patterns {
			ms, err := globMatchOne(root, p, true)
			if err != nil {
				return nil, err
			}
			all = append(all, ms...)
		}
	}
	sort.Strings(all)
	return uniqueStrings(all), nil
}

func globMatchOne(root, pattern string, ci bool) ([]string, error) {
	matches := make([]string, 0)
	if strings.Contains(pattern, "**") {
		wildcardIndex := strings.Index(pattern, "**")
		prefix := strings.Trim(pattern[:wildcardIndex], "/")
		suffix := strings.Trim(pattern[wildcardIndex+2:], "/")
		walkRoot := root
		if prefix != "" {
			var err error
			walkRoot, err = resolveLocalWorkspacePath(root, prefix)
			if err != nil {
				return nil, err
			}
		}
		if err := filepath.WalkDir(walkRoot, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			relative, err := filepath.Rel(walkRoot, path)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			if suffix == "" || matchGlobPattern(suffix, relative, ci) {
				matches = append(matches, path)
			}
			return nil
		}); err != nil {
			return nil, fmt.Errorf("walk glob root: %w", err)
		}
		return matches, nil
	}
	globPattern := filepath.Join(root, filepath.FromSlash(pattern))
	globbed, err := filepath.Glob(globPattern)
	if err != nil {
		return nil, fmt.Errorf("invalid glob pattern: %w", err)
	}
	for _, path := range globbed {
		if _, err := resolveLocalWorkspacePath(root, path); err != nil {
			return nil, err
		}
		matches = append(matches, path)
	}
	if len(matches) == 0 && ci {
		// Case-insensitive fallback for plain patterns: walk and match
		// basenames so e.g. "*.jsx" still finds "Sidebar.jsx" anywhere.
		basePat := filepath.Base(filepath.FromSlash(pattern))
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			if matchGlobPattern(basePat, entry.Name(), true) {
				matches = append(matches, path)
			}
			return nil
		})
	}
	return matches, nil
}

// matchGlobPattern reports whether path matches pattern, optionally
// case-insensitively, checking the full relative path and the basename.
func matchGlobPattern(pattern, path string, ci bool) bool {
	if ci {
		pattern = strings.ToLower(pattern)
		path = strings.ToLower(path)
	}
	pat := filepath.FromSlash(pattern)
	p := filepath.FromSlash(path)
	if matched, _ := filepath.Match(pat, p); matched {
		return true
	}
	base := filepath.Base(p)
	if base != p {
		if matched, _ := filepath.Match(pat, base); matched {
			return true
		}
	}
	return false
}

// expandGlobBraces expands {a,b,c} alternation groups into concrete
// patterns. filepath.Match does not support brace alternation, so e.g.
// "*.{js,jsx,ts}" becomes three patterns. Nested groups are recursive.
func expandGlobBraces(pattern string) []string {
	start := strings.IndexByte(pattern, '{')
	if start < 0 {
		return []string{pattern}
	}
	depth := 0
	end := -1
	for i := start; i < len(pattern); i++ {
		switch pattern[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return []string{pattern}
	}
	inner := pattern[start+1 : end]
	var alts []string
	var cur strings.Builder
	d := 0
	for _, ch := range inner {
		switch ch {
		case '{':
			d++
			cur.WriteRune(ch)
		case '}':
			d--
			cur.WriteRune(ch)
		case ',':
			if d == 0 {
				alts = append(alts, cur.String())
				cur.Reset()
			} else {
				cur.WriteRune(ch)
			}
		default:
			cur.WriteRune(ch)
		}
	}
	alts = append(alts, cur.String())
	prefix := pattern[:start]
	suffix := pattern[end+1:]
	var out []string
	for _, alt := range alts {
		for _, sub := range expandGlobBraces(prefix + alt + suffix) {
			out = append(out, sub)
		}
	}
	return out
}

func validateRelativeGlobPattern(pattern string) error {
	if filepath.IsAbs(filepath.FromSlash(pattern)) {
		return fmt.Errorf("glob pattern must be relative to the requested path")
	}
	for _, part := range strings.Split(pattern, "/") {
		if part == ".." {
			return fmt.Errorf("glob pattern must not leave the requested path")
		}
	}
	return nil
}

func uniqueStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}
