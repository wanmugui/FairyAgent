package builtin

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// NewLocalEditFileTool edits a workspace file by replacing an exact text
// snippet. Inspired by dsh `tool-str-replace-editor`: it surfaces where the
// snippet matched (or would have matched) so the model can fix typos with a
// concrete line number rather than a bare "not found" message.
func NewLocalEditFileTool(schema ToolDef) Tool {
	return NewLocalEditFileToolWithConfig(schema, WritableFileToolConfig{})
}

func NewLocalEditFileToolWithConfig(schema ToolDef, settings WritableFileToolConfig) Tool {
	return newLocalStructuredTool("edit_file", schema, func(ctx context.Context, invocation ToolInvocation) (ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return ToolResult{}, err
		}
		args, err := decodeLocalToolArgs(invocation)
		if err != nil {
			return localErrorResult("edit_file", err), nil
		}
		filePath := localStringArg(args, "file_path", "path")
		oldText := firstNonEmpty(localStringArg(args, "old_text"), localStringArg(args, "old_string"))
		if filePath == "" {
			return localErrorResult("edit_file", fmt.Errorf("file_path is required")), nil
		}
		if oldText == "" {
			return localErrorResult("edit_file", fmt.Errorf("old_text is required")), nil
		}
		localContext, err := localToolContext(invocation)
		if err != nil {
			return localErrorResult("edit_file", err), nil
		}
		fullPath, err := resolveLocalWritablePath(localContext.Workspace, settings.MemoryRoot, filePath)
		if err != nil {
			return localErrorResult("edit_file", err), nil
		}
		// Hard read-before-write enforcement: the schema asks the model to
		// read the file first; without a check the model edits blind and
		// hallucinates old_text. Allow a small bypass: brand-new files
		// (not yet on disk) are handled by write_file, so error here.
		if !wasFileRead(resolveReadSessionKey(invocation.SessionFile), fullPath) {
			if _, statErr := os.Stat(fullPath); statErr == nil {
				return localErrorResult("edit_file", fmt.Errorf(
					"read-before-edit check failed: you must call read_file on %s earlier in this session before editing it (anti-hallucination guard; the file exists but you have not seen its current content)",
					filePath,
				)), nil
			}
		}
		data, err := os.ReadFile(fullPath)
		if err != nil {
			return localErrorResult("edit_file", fmt.Errorf("read error: %w", err)), nil
		}
		content := string(data)
		newText := firstNonEmpty(localStringArg(args, "new_text"), localStringArg(args, "new_string"))
		replaceAll := localBoolArg(args, "replace_all") || localBoolArg(args, "all_occurrences")

		// read_file presents CRLF files as LF to the model. Match against the
		// same normalized view, then map every hit back to raw byte ranges so
		// the file keeps its original line endings.
		normalizedContent, normalizedToRaw := normalizeCRLF(content)
		normalizedOldText := normalizeCRLFText(oldText)
		offsets := matchOffsets(normalizedContent, normalizedOldText)
		rawOffsets := mapNormalizedOffsets(offsets, normalizedToRaw)
		switch {
		case len(offsets) == 0:
			return localErrorResult("edit_file", fmt.Errorf(
				"old_text not found in %s\n%s",
				filePath,
				nearestMatchHint(normalizedContent, normalizedOldText),
			)), nil
		case len(offsets) > 1 && !replaceAll:
			lines := lineNumbersAt(content, rawOffsets)
			return localErrorResult("edit_file", fmt.Errorf(
				"old_text matched %d locations at lines %v in %s; pass replace_all=true to replace every occurrence, or extend old_text with more surrounding lines to make it unique",
				len(offsets), lines, filePath,
			)), nil
		}

		ranges := normalizedMatchRanges(offsets, len(normalizedOldText), normalizedToRaw)
		if !replaceAll {
			ranges = ranges[:1]
		}
		count := len(ranges)
		replacement := matchFileLineEndings(content, newText)
		updated := replaceRanges(content, ranges, replacement)

		if err := os.WriteFile(fullPath, []byte(updated), 0o644); err != nil {
			return localErrorResult("edit_file", fmt.Errorf("write error: %w", err)), nil
		}
		replacedAt := lineNumbersAt(content, rawOffsets)
		if !replaceAll && len(replacedAt) > 0 {
			replacedAt = replacedAt[:1]
		}
		resultPath := localRelativePath(localContext.Workspace, fullPath)
		if strings.HasPrefix(strings.ToLower(filePath), "memory://") {
			if relative, relErr := filepath.Rel(settings.MemoryRoot, fullPath); relErr == nil {
				resultPath = "memory://" + filepath.ToSlash(relative)
			}
		}
		return ToolResult{Value: map[string]any{
			"ok":           true,
			"path":         resultPath,
			"replacements": count,
			"replaced_at":  replacedAt,
		}}, nil
	})
}

// matchOffsets returns the byte offsets where needle occurs in content. An
// empty needle yields no matches (avoiding pathological ReplaceAll behavior).
func matchOffsets(content, needle string) []int {
	if needle == "" {
		return nil
	}
	var offsets []int
	offset := 0
	for {
		idx := strings.Index(content[offset:], needle)
		if idx < 0 {
			return offsets
		}
		offsets = append(offsets, offset+idx)
		offset += idx + len(needle)
	}
}

type textRange struct {
	start int
	end   int
}

// normalizeCRLF returns a view with CRLF converted to LF and a byte-boundary
// map from the normalized view back to the original string. The map has one
// entry per normalized byte boundary plus the final boundary.
func normalizeCRLF(content string) (string, []int) {
	var normalized strings.Builder
	normalized.Grow(len(content))
	boundaries := make([]int, 0, len(content)+1)
	for index := 0; index < len(content); {
		boundaries = append(boundaries, index)
		if content[index] == '\r' && index+1 < len(content) && content[index+1] == '\n' {
			normalized.WriteByte('\n')
			index += 2
			continue
		}
		normalized.WriteByte(content[index])
		index++
	}
	boundaries = append(boundaries, len(content))
	return normalized.String(), boundaries
}

func normalizeCRLFText(text string) string {
	if !strings.Contains(text, "\r\n") {
		return text
	}
	return strings.ReplaceAll(text, "\r\n", "\n")
}

func mapNormalizedOffsets(offsets []int, boundaries []int) []int {
	mapped := make([]int, len(offsets))
	for index, offset := range offsets {
		mapped[index] = boundaries[offset]
	}
	return mapped
}

func normalizedMatchRanges(offsets []int, needleLength int, boundaries []int) []textRange {
	ranges := make([]textRange, 0, len(offsets))
	for _, offset := range offsets {
		ranges = append(ranges, textRange{
			start: boundaries[offset],
			end:   boundaries[offset+needleLength],
		})
	}
	return ranges
}

func matchFileLineEndings(content, replacement string) string {
	replacement = normalizeCRLFText(replacement)
	if !usesCRLFLineEndings(content) {
		return replacement
	}
	return strings.ReplaceAll(replacement, "\n", "\r\n")
}

func usesCRLFLineEndings(content string) bool {
	crlfCount := strings.Count(content, "\r\n")
	if crlfCount == 0 {
		return false
	}
	lfOnlyCount := strings.Count(content, "\n") - crlfCount
	return crlfCount >= lfOnlyCount
}

func replaceRanges(content string, ranges []textRange, replacement string) string {
	if len(ranges) == 0 {
		return content
	}
	var buf bytes.Buffer
	buf.Grow(len(content))
	cursor := 0
	for _, target := range ranges {
		buf.WriteString(content[cursor:target.start])
		buf.WriteString(replacement)
		cursor = target.end
	}
	buf.WriteString(content[cursor:])
	return buf.String()
}

// lineNumbersAt returns the 1-indexed line number for each byte offset.
func lineNumbersAt(content string, offsets []int) []int {
	lines := make([]int, len(offsets))
	line := 1
	cursor := 0
	for i, offset := range offsets {
		for cursor < offset {
			if content[cursor] == '\n' {
				line++
			}
			cursor++
		}
		lines[i] = line
	}
	return lines
}

// nearestMatchHint finds a small unique snippet (line containing the first
// occurrence of the longest token shared with needle) so the model can
// localize a typo without flooding the context. Empty when no token overlaps.
func nearestMatchHint(content, needle string) string {
	tokens := strings.Fields(needle)
	if len(tokens) == 0 {
		return ""
	}
	best := ""
	bestLine := 0
	for _, tok := range tokens {
		if len(tok) < 3 {
			continue
		}
		idx := strings.Index(content, tok)
		if idx < 0 {
			continue
		}
		line, text := lineAndTextAt(content, idx)
		if best == "" || len(tok) > len(best) {
			best = tok
			bestLine = line
			_ = text
		}
	}
	if best == "" {
		return ""
	}
	return fmt.Sprintf("(hint: similar token %q appears at line %d)", best, bestLine)
}

func lineAndTextAt(content string, offset int) (int, string) {
	line := 1
	lineStart := 0
	for i := 0; i < offset; i++ {
		if content[i] == '\n' {
			line++
			lineStart = i + 1
		}
	}
	lineEnd := strings.IndexByte(content[lineStart:], '\n')
	if lineEnd < 0 {
		lineEnd = len(content)
	} else {
		lineEnd += lineStart
	}
	return line, content[lineStart:lineEnd]
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
