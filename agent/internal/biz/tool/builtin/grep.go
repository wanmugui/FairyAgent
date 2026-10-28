package builtin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type grepHit struct {
	Path  string `json:"path"`
	Line  int    `json:"line"`
	Match string `json:"match"`
}

func NewLocalGrepTool(schema ToolDef) Tool {
	return newLocalStructuredTool("grep", schema, func(ctx context.Context, invocation ToolInvocation) (ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return ToolResult{}, err
		}
		args, err := decodeLocalToolArgs(invocation)
		if err != nil {
			return localErrorResult("grep", err), nil
		}
		pattern := localStringArg(args, "pattern")
		if pattern == "" {
			return localErrorResult("grep", fmt.Errorf("pattern is required")), nil
		}
		localContext, err := localToolContext(invocation)
		if err != nil {
			return localErrorResult("grep", err), nil
		}
		searchRoot := localStringArg(args, "path")
		if searchRoot == "" {
			searchRoot = "."
		}
		root, _, err := resolveLocalReadablePath(localContext.Workspace, "", searchRoot)
		if err != nil {
			return localErrorResult("grep", err), nil
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return localErrorResult("grep", fmt.Errorf("invalid regex: %w", err)), nil
		}
		maxResults := localIntArg(args, "max_results", 200)
		if maxResults <= 0 {
			maxResults = 200
		}
		globFilter := localStringArg(args, "glob")
		caseInsensitive := localBoolArg(args, "case_insensitive")

		hits, provider, err := runGrep(ctx, root, pattern, globFilter, caseInsensitive, maxResults)
		if err != nil {
			return localErrorResult("grep", err), nil
		}
		return ToolResult{Value: map[string]any{
			"ok":        true,
			"provider":  provider,
			"count":     len(hits),
			"hits":      hits,
			"truncated": len(hits) >= maxResults,
		}}, nil
	})
}

// runGrep prefers ripgrep when present (dsh's default backend); falls back to
// a Go-only recursive walker when rg is missing. The walker is not a full
// ripgrep implementation — it reads whole files line-by-line — but it covers
// the common case of searching a small repo without an external dependency.
func runGrep(ctx context.Context, root, pattern, globFilter string, caseInsensitive bool, max int) ([]grepHit, string, error) {
	if rgPath, err := exec.LookPath("rg"); err == nil {
		hits, err := runRipgrep(ctx, rgPath, root, pattern, globFilter, caseInsensitive, max)
		if err != nil {
			return nil, "ripgrep", err
		}
		return hits, "ripgrep", nil
	}
	hits, err := runFallbackGrep(ctx, root, pattern, globFilter, caseInsensitive, max)
	if err != nil {
		return nil, "fallback", err
	}
	return hits, "fallback", nil
}

func runRipgrep(ctx context.Context, rgPath, root, pattern, globFilter string, caseInsensitive bool, max int) ([]grepHit, error) {
	args := []string{
		"--no-heading",
		"--line-number",
		"--no-messages",
		"--json",
	}
	if caseInsensitive {
		args = append(args, "--ignore-case")
	} else {
		args = append(args, "--case-sensitive")
	}
	if globFilter != "" {
		args = append(args, "--glob", globFilter)
	}
	if max > 0 {
		args = append(args, "--max-count", fmt.Sprintf("%d", max))
	}
	args = append(args, pattern, ".")
	cmd := exec.CommandContext(ctx, rgPath, args...)
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		// ripgrep exits 1 when no matches; treat that as empty.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("ripgrep failed: %w", err)
	}
	hits := make([]grepHit, 0)
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var event struct {
			Type string `json:"type"`
			Data struct {
				Path struct {
					Text string `json:"text"`
				} `json:"path"`
				LineNumber int `json:"line_number"`
				Line       struct {
					Text string `json:"text"`
				} `json:"line"`
				Lines struct {
					Text string `json:"text"`
				} `json:"lines"`
			} `json:"data"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if event.Type != "match" {
			continue
		}
		lineText := event.Data.Line.Text
		if lineText == "" {
			lineText = event.Data.Lines.Text
		}
		path := filepath.ToSlash(event.Data.Path.Text)
		path = strings.TrimPrefix(path, "./")
		hits = append(hits, grepHit{
			Path:  path,
			Line:  event.Data.LineNumber,
			Match: strings.TrimRight(lineText, "\r"),
		})
		if len(hits) >= max {
			break
		}
	}
	return hits, nil
}

func runFallbackGrep(ctx context.Context, root, pattern, globFilter string, caseInsensitive bool, max int) ([]grepHit, error) {
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return nil, fmt.Errorf("compile regex: %w", err)
	}
	if !caseInsensitive {
		re, err = regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("compile regex: %w", err)
		}
	}
	hits := make([]grepHit, 0)
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.IsDir() {
			return nil
		}
		if globFilter != "" {
			matched, _ := filepath.Match(globFilter, filepath.Base(path))
			if !matched {
				return nil
			}
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			line := scanner.Text()
			if re.MatchString(line) {
				rel, _ := filepath.Rel(root, path)
				hits = append(hits, grepHit{
					Path:  filepath.ToSlash(rel),
					Line:  lineNo,
					Match: line,
				})
				if len(hits) >= max {
					return fs.SkipAll
				}
			}
		}
		return nil
	})
	if walkErr != nil && walkErr != fs.SkipAll {
		return nil, walkErr
	}
	return hits, nil
}
