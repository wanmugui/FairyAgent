package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

type memoryFile struct {
	Path    string
	Content string
}

type memoryHit struct {
	Path          string `json:"path"`
	Title         string `json:"title"`
	Snippet       string `json:"snippet"`
	Score         int    `json:"score"`
	Key           string `json:"key,omitempty"`
	Kind          string `json:"kind,omitempty"`
	Target        string `json:"target,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	InteractionID string `json:"interaction_id,omitempty"`
	SegmentID     string `json:"segment_id,omitempty"`
	Status        string `json:"status,omitempty"`
}

func NewLocalMemorySearchTool(schema ToolDef, memoryRoot string) Tool {
	return newLocalStructuredTool("memory_search", schema, func(ctx context.Context, invocation ToolInvocation) (ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return ToolResult{}, err
		}
		args, err := decodeLocalToolArgs(invocation)
		if err != nil {
			return localErrorResult("memory_search", err), nil
		}
		query := strings.TrimSpace(localStringArg(args, "query"))
		limit := localIntArg(args, "limit", 8)
		if limit <= 0 {
			limit = 8
		}
		if strings.TrimSpace(memoryRoot) == "" {
			return localErrorResult("memory_search", fmt.Errorf("memory directory is not configured")), nil
		}

		var files []memoryFile
		_ = filepath.WalkDir(memoryRoot, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() {
				return nil
			}
			relative, relErr := filepath.Rel(memoryRoot, path)
			if relErr != nil {
				return nil
			}
			relativeSlash := filepath.ToSlash(relative)
			// Segmented memory is retrieved through its KEY index below. Do not
			// let a raw index volume leak invalidated/tombstoned lines through
			// the generic file scanner.
			if relativeSlash == "segmented" || strings.HasPrefix(relativeSlash, "segmented/") {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			if ext != ".md" && ext != ".txt" && ext != ".jsonl" {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil
			}
			content := string(data)
			if len(content) > 8000 {
				content = content[:8000]
			}
			files = append(files, memoryFile{Path: "memory://" + filepath.ToSlash(relative), Content: content})
			return nil
		})

		hits := loadSegmentedMemoryIndexHits(memoryRoot, query, limit)
		seenHits := make(map[string]struct{}, len(hits))
		for _, hit := range hits {
			seenHits[hit.Path+"\x00"+hit.Title] = struct{}{}
		}
		for _, file := range files {
			score := memorySearchScore(query, file.Path, file.Content)
			if score <= 0 {
				continue
			}
			hit := memoryHit{
				Path:    file.Path,
				Title:   memoryTitle(file.Path, file.Content),
				Snippet: memorySnippet(file.Content),
				Score:   score,
			}
			if _, exists := seenHits[hit.Path+"\x00"+hit.Title]; exists {
				continue
			}
			hits = append(hits, hit)
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

type segmentedMemoryIndexLine struct {
	Key           string `json:"key"`
	Target        string `json:"target"`
	Type          string `json:"type"`
	Kind          string `json:"kind"`
	KeyStatus     string `json:"key_status"`
	SessionID     string `json:"session_id"`
	InteractionID string `json:"interaction_id"`
	SegmentID     string `json:"segment_id"`
	TurnID        string `json:"turn_id"`
	Status        string `json:"status"`
}

func loadSegmentedMemoryIndexHits(memoryRoot, query string, limit int) []memoryHit {
	indexRoot := filepath.Join(memoryRoot, "segmented", "index")
	if strings.TrimSpace(query) == "" {
		return nil
	}
	var hits []memoryHit
	_ = filepath.WalkDir(indexRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || strings.ToLower(filepath.Ext(path)) != ".jsonl" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for _, raw := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			var line segmentedMemoryIndexLine
			if json.Unmarshal([]byte(raw), &line) != nil || strings.TrimSpace(line.Key) == "" || strings.TrimSpace(line.Target) == "" {
				continue
			}
			if status := strings.TrimSpace(line.Status); status != "" && !strings.EqualFold(status, "active") {
				continue
			}
			score := memorySearchScore(query, line.Target, line.Key) + 20
			if score <= 20 {
				continue
			}
			interactionID := strings.TrimSpace(line.InteractionID)
			if interactionID == "" {
				interactionID = strings.TrimSpace(line.TurnID)
			}
			hits = append(hits, memoryHit{
				Path:          line.Target,
				Title:         line.Key,
				Snippet:       "KEY: " + line.Key + " -> " + line.Target,
				Score:         score,
				Key:           line.Key,
				Kind:          line.Kind,
				Target:        line.Target,
				SessionID:     line.SessionID,
				InteractionID: interactionID,
				SegmentID:     line.SegmentID,
				Status:        "active",
			})
		}
		return nil
	})
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

func memorySearchScore(query, path, content string) int {
	score := 0
	if query != "" && strings.Contains(content, query) {
		score += 5
	}
	if query != "" && strings.Contains(path, query) {
		score += 3
	}
	lowerContent := strings.ToLower(content)
	lowerPath := strings.ToLower(path)
	for _, term := range strings.FieldsFunc(query, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		term = strings.ToLower(term)
		if term == "" {
			continue
		}
		score += strings.Count(lowerContent, term)
		if strings.Contains(lowerPath, term) {
			score += 2
		}
	}
	return score
}

func memoryTitle(path, content string) string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
		}
	}
	return filepath.Base(path)
}

func memorySnippet(content string) string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if len(trimmed) > 200 {
			trimmed = trimmed[:200]
		}
		return trimmed
	}
	if len(content) > 200 {
		return content[:200]
	}
	return content
}
