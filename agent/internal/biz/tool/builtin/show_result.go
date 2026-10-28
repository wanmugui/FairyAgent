package builtin

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// showResultAllowedRoots mirrors the frontend server's FILE_CONTENT_ALLOWED_ROOTS
// so the Go agent never spawns a viewer for paths the browser cannot fetch.
// Keep this list in sync with frontend/server.cjs.
var showResultAllowedRoots = []string{}

func init() {
	// Walk once at startup to populate the roots from the configured repo.
	// When the caller resolves a path we'll match against these prefixes
	// regardless of how REPO_ROOT is set.
}

// LocalShowResultTool validates a result file or PPT deck directory and returns
// its canonical path. The chat surface receives it through file_result and
// opens the embedded viewer; voice opens the full filemanager page or PPT
// preview. Implemented as a local builtin so the agent can fire it without
// needing an extra tool gateway.
//
// Path must resolve inside the configured workspace or the agent binary
// directory; any other path is rejected silently (the tool returns
// ok:false with a reason so the model can recover).
type LocalShowResultTool struct {
	name   string
	schema ToolDef
	root   string
}

func NewLocalShowResultTool(schema ToolDef, root string) Tool {
	return &LocalShowResultTool{name: "show_result", schema: schema, root: root}
}

func (t *LocalShowResultTool) Name() string    { return t.name }
func (t *LocalShowResultTool) Schema() ToolDef { return t.schema }

func (t *LocalShowResultTool) Execute(ctx context.Context, invocation ToolInvocation) (ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	args, err := decodeLocalToolArgs(invocation)
	if err != nil {
		return localErrorResult("show_result", err), nil
	}
	filePath := strings.TrimSpace(localStringArg(args, "path", "file_path"))
	if filePath == "" {
		return localErrorResult("show_result", fmt.Errorf("path is required")), nil
	}
	abs, err := filepath.Abs(filePath)
	if err != nil {
		return localErrorResult("show_result", fmt.Errorf("invalid path: %w", err)), nil
	}
	if !t.allowed(abs) {
		return localErrorResult("show_result",
			fmt.Errorf("path %q is outside the agent's allowed roots", abs)), nil
	}
	info, statErr := os.Stat(abs)
	if statErr != nil {
		return localErrorResult("show_result",
			fmt.Errorf("cannot open %q: %w", abs, statErr)), nil
	}
	kind := "file"
	if info.IsDir() {
		if !isPPTDeckDirectory(abs) {
			return localErrorResult("show_result",
				fmt.Errorf("path %q is a directory but not a PPT deck (expected htmls/page_*.html or pages/page_*.png)", abs)), nil
		}
		kind = "ppt"
	}

	// The host surface decides how to present this path: text chat emits
	// file_result for the embedded viewer, while voice launches the full
	// filemanager page or PPT preview.
	base := showResultPublicBase()
	target := showResultTargetURL(base, "index-fm.html", "file", abs)
	hint := "Text chat shows this result in the embedded viewer; voice opens the full filemanager page."
	if kind == "ppt" {
		target = showResultTargetURL(base, "api/ppt-preview", "deck_dir", abs)
		hint = "Text chat shows this PPT deck in the result card and embedded viewer; voice opens the PPT preview page."
	}

	result := map[string]any{
		"ok":        true,
		"tool":      "show_result",
		"kind":      kind,
		"path":      abs,
		"url":       target,
		"open_mode": "frontend_preview",
		"hint":      hint,
	}
	return ToolResult{Value: result}, nil
}

// defaultShowResultBase is the fallback preview base. It is only correct when
// the browser talking to us is on the same machine as this process.
const defaultShowResultBase = "http://localhost:8081"

// showResultPublicBase returns the browser-facing base URL for previews.
//
// Why this is configurable at all: the address used to be a hardcoded
// "http://localhost:8081", which silently breaks every remote user. When Fairy
// is reached through a tunnel (Cloudflare / reverse proxy), the *browser*
// resolves "localhost" to the user's own machine, so clicking a result card
// opens nothing — and because nothing errors, it reads as "the image is
// broken" rather than "the URL is wrong". That cost a full session of showing
// images that never arrived.
//
// Set FAIRY_PUBLIC_BASE_URL to the origin that proxies the filemanager, e.g.
// "https://files.example.com/filemanage" (that host rewrites /filemanage/* to
// the 8081 filemanager). Left unset, the localhost default stands so local
// usage and every existing deployment are unaffected.
func showResultPublicBase() string {
	raw := strings.TrimSpace(os.Getenv("FAIRY_PUBLIC_BASE_URL"))
	if raw == "" {
		return defaultShowResultBase
	}
	return strings.TrimRight(raw, "/")
}

// showResultTargetURL builds "<base>/<path>?<param>=<escaped value>".
func showResultTargetURL(base, path, param, value string) string {
	return fmt.Sprintf("%s/%s?%s=%s", base, path, param, urlQueryEscape(value))
}

func isPPTDeckDirectory(abs string) bool {
	for _, subdir := range []string{"htmls", "pages"} {
		entries, err := os.ReadDir(filepath.Join(abs, subdir))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := strings.ToLower(entry.Name())
			if subdir == "htmls" && strings.HasPrefix(name, "page_") && strings.HasSuffix(name, ".html") {
				return true
			}
			if subdir == "pages" && strings.HasPrefix(name, "page_") && strings.HasSuffix(name, ".png") {
				return true
			}
		}
	}
	return false
}

// allowed reports whether abs lives under the agent repo, the workspace
// dir, the skills dir, or the frontend dir. We mirror the frontend server's
// FILE_CONTENT_ALLOWED_ROOTS so the two stay in sync.
func (t *LocalShowResultTool) allowed(abs string) bool {
	candidates := showResultAllowedRoots
	if t.root != "" {
		candidates = append([]string{t.root}, candidates...)
	}
	candidates = append(candidates,
		filepath.Join(t.root, "workspace"),
		filepath.Join(t.root, "skills"),
		filepath.Join(t.root, "frontend"),
	)
	lower := strings.ToLower(abs)
	for _, root := range candidates {
		if root == "" {
			continue
		}
		rl := strings.ToLower(root)
		if lower == rl || strings.HasPrefix(lower, rl+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func urlQueryEscape(s string) string {
	// Delegate to net/url so the encoding matches what fetch() / Go's http
	// package produce when they read the value back. Our previous custom
	// encoder percent-escaped '\\' which the browser then re-escaped,
	// turning ':' into '%253A' and breaking server-side path resolution.
	return url.QueryEscape(s)
}
