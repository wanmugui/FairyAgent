package builtin

import (
	"context"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func NewLocalReadFileTool(schema ToolDef) Tool {
	return NewLocalReadFileToolWithConfig(schema, ReadFileToolConfig{})
}

func NewLocalReadFileToolWithConfig(schema ToolDef, settings ReadFileToolConfig) Tool {
	return newLocalStructuredTool("read_file", schema, func(ctx context.Context, invocation ToolInvocation) (ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return ToolResult{}, err
		}
		args, err := decodeLocalToolArgs(invocation)
		if err != nil {
			return localErrorResult("read_file", err), nil
		}
		filePath := localStringArg(args, "file_path", "path")
		if filePath == "" {
			return localErrorResult("read_file", fmt.Errorf("file_path is required")), nil
		}
		localContext, err := localToolContext(invocation)
		if err != nil {
			return localErrorResult("read_file", err), nil
		}
		fullPath, root, err := resolveLocalReadablePathWithMemory(localContext.Workspace, settings.SkillsRoot, settings.MemoryRoot, filePath)
		if err != nil {
			return localErrorResult("read_file", err), nil
		}
		info, err := os.Stat(fullPath)
		if err != nil {
			if os.IsNotExist(err) {
				return localErrorResult("read_file", fmt.Errorf("file not found: %s", fullPath)), nil
			}
			return localErrorResult("read_file", fmt.Errorf("stat error: %w", err)), nil
		}
		if info.IsDir() {
			return localErrorResult("read_file", fmt.Errorf("path is a directory: %s", fullPath)), nil
		}
		if settings.MaxReadFileSizeBytes > 0 && info.Size() > settings.MaxReadFileSizeBytes {
			return localErrorResult("read_file", fmt.Errorf("file exceeds configured maximum size of %d bytes: %s", settings.MaxReadFileSizeBytes, fullPath)), nil
		}
		// Mark as read so edit_file's read-before-write check passes.
		markFileRead(resolveReadSessionKey(invocation.SessionFile), fullPath)
		resultPath := fullPath
		if root == "skills" || root == "memory" {
			resultPath = localReadableResultPathWithMemory(root, localContext.Workspace, settings.SkillsRoot, settings.MemoryRoot, fullPath)
		}
		// Multimodal dispatch: defer non-text targets to the appropriate
		// backend tool rather than dumping binary bytes. Inspired by dsh
		// `tool-fs/read-target.ts`, the model can then re-issue read_file with
		// the matching tool to actually parse the content.
		if dispatch := localReadFileDispatch(fullPath); dispatch != nil {
			value := map[string]any{
				"path":       resultPath,
				"encoding":   dispatch.Encoding,
				"line_count": 0,
				"content":    "",
				"truncated":  false,
				"bytes":      info.Size(),
				"deferred_to": map[string]any{
					"tool":        dispatch.Tool,
					"reason":      dispatch.Reason,
					"target_path": resultPath,
				},
				"hint": fmt.Sprintf("this file is %s; call %s to read it instead of read_file", dispatch.Reason, dispatch.Tool),
			}
			return ToolResult{Value: value}, nil
		}

		data, err := os.ReadFile(fullPath)
		if err != nil {
			return localErrorResult("read_file", fmt.Errorf("read error: %w", err)), nil
		}

		lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
		totalLines := len(lines)
		offset := localIntArg(args, "offset", 0)
		if offset < 0 {
			offset = 0
		}
		limit := localIntArg(args, "limit", 0)
		maxBytes := localIntArg(args, "max_bytes", 0)
		if limit <= 0 && maxBytes <= 0 {
			limit = 2000
		}
		if offset > totalLines {
			offset = totalLines
		}
		endLine := totalLines
		if limit > 0 && offset+limit < endLine {
			endLine = offset + limit
		}
		selected := lines[offset:endLine]
		numbered := make([]string, 0, len(selected))
		for index, line := range selected {
			numbered = append(numbered, fmt.Sprintf("%4d: %s", offset+index+1, line))
		}
		content := strings.Join(numbered, "\n")
		truncated := endLine < totalLines
		if maxBytes > 0 && len([]byte(content)) > maxBytes {
			bytes := []byte(content)[:maxBytes]
			for len(bytes) > 0 && !utf8.Valid(bytes) {
				bytes = bytes[:len(bytes)-1]
			}
			content = string(bytes)
			truncated = true
		}
		return ToolResult{Value: map[string]any{
			"path":       resultPath,
			"encoding":   "utf-8",
			"line_count": totalLines,
			"content":    content,
			"truncated":  truncated,
			"bytes":      len([]byte(content)),
		}}, nil
	})
}

type localReadDispatch struct {
	Tool     string
	Reason   string
	Encoding string
}

// localReadFileDispatch decides whether a path needs a non-text reader. It
// runs BEFORE the file is read, so the actual bytes never enter context. When
// it returns non-nil, read_file emits a `deferred_to` payload that tells the
// model which tool to call next (image_vqa for images, document_parser for
// PDFs/Office documents, the http `web_fetch` for HTML).
func localReadFileDispatch(fullPath string) *localReadDispatch {
	ext := strings.ToLower(filepath.Ext(fullPath))
	mimeType := mime.TypeByExtension(ext)

	switch {
	case isImageExt(ext):
		return &localReadDispatch{Tool: "image_vqa", Reason: "an image", Encoding: "binary"}
	case isDocumentExt(ext):
		// PDFs and Office documents are parsed locally by document_parser.
		return &localReadDispatch{Tool: "document_parser", Reason: "a structured document", Encoding: "binary"}
	case ext == ".html" || ext == ".htm":
		return &localReadDispatch{Tool: "web_fetch", Reason: "an HTML document", Encoding: "text/html"}
	case mimeType != "" && !strings.HasPrefix(mimeType, "text/") && !strings.HasPrefix(mimeType, "application/json"):
		// Unknown binary content — refuse to dump it into context.
		return &localReadDispatch{Tool: "bash", Reason: fmt.Sprintf("a non-text file (%s)", mimeType), Encoding: "binary"}
	}
	return nil
}

func isImageExt(ext string) bool {
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tiff", ".tif", ".heic":
		return true
	}
	return false
}

func isDocumentExt(ext string) bool {
	switch ext {
	case ".pdf", ".doc", ".docx", ".ppt", ".pptx", ".xls", ".xlsx":
		return true
	}
	return false
}
