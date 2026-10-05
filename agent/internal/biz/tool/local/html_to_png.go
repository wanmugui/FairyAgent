package local

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type localHTMLToPNGTool struct {
	schema   ToolDef
	cfg      *Config
	resolver localExecutableResolver
	runner   localProcessRunner
}

func NewLocalHTMLToPNGTool(schema ToolDef, cfg *Config) Tool {
	return newLocalHTMLToPNGTool(schema, cfg, newLocalExecutableResolver(), osLocalProcessRunner{})
}

func newLocalHTMLToPNGTool(schema ToolDef, cfg *Config, resolver localExecutableResolver, runner localProcessRunner) Tool {
	if runner == nil {
		runner = osLocalProcessRunner{}
	}
	return &localHTMLToPNGTool{schema: schema, cfg: cfg, resolver: resolver, runner: runner}
}

func (t *localHTMLToPNGTool) Name() string {
	return "html_to_png"
}

func (t *localHTMLToPNGTool) Schema() ToolDef {
	return t.schema
}

func (t *localHTMLToPNGTool) Execute(ctx context.Context, invocation ToolInvocation) (ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	args, err := decodeLocalToolArgs(invocation)
	if err != nil {
		return localErrorResult(t.Name(), err), nil
	}
	localContext, err := localToolContext(invocation)
	if err != nil {
		return localErrorResult(t.Name(), err), nil
	}
	mode := strings.ToLower(strings.TrimSpace(localStringArg(args, "mode")))
	if mode != "" && mode != "stateless" {
		return localErrorResult(t.Name(), fmt.Errorf("unsupported mode: %s", mode)), nil
	}

	tmpDir, err := os.MkdirTemp("", "agent-html-to-png-*")
	if err != nil {
		return localErrorResult(t.Name(), fmt.Errorf("create temporary render directory: %w", err)), nil
	}
	defer os.RemoveAll(tmpDir)

	var sourcePath, outputPath string
	if mode == "stateless" {
		htmlContent := localStringArg(args, "html_file_content")
		if strings.TrimSpace(htmlContent) == "" {
			return localErrorResult(t.Name(), fmt.Errorf("html_file_content is required in stateless mode")), nil
		}
		sourcePath = filepath.Join(tmpDir, "page.html")
		if err := os.WriteFile(sourcePath, []byte(htmlContent), 0o600); err != nil {
			return localErrorResult(t.Name(), fmt.Errorf("write temporary HTML: %w", err)), nil
		}
		outputPath = filepath.Join(tmpDir, "screenshot.png")
	} else {
		htmlRequested := localStringArg(args, "html_file_path")
		if strings.TrimSpace(htmlRequested) == "" {
			return localErrorResult(t.Name(), fmt.Errorf("html_file_path is required in file mode")), nil
		}
		sourcePath, err = resolveLocalWorkspacePath(localContext.Workspace, htmlRequested)
		if err != nil {
			return localErrorResult(t.Name(), err), nil
		}
		info, statErr := os.Stat(sourcePath)
		if statErr != nil {
			return localErrorResult(t.Name(), fmt.Errorf("HTML source is unavailable: %w", statErr)), nil
		}
		if info.IsDir() {
			return localErrorResult(t.Name(), fmt.Errorf("HTML source is a directory: %s", htmlRequested)), nil
		}
		outputRequested := localStringArg(args, "result_image_path")
		if strings.TrimSpace(outputRequested) == "" {
			outputPath = strings.TrimSuffix(sourcePath, filepath.Ext(sourcePath)) + ".png"
		} else {
			outputPath, err = resolveLocalWorkspacePath(localContext.Workspace, outputRequested)
			if err != nil {
				return localErrorResult(t.Name(), err), nil
			}
		}
		if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
			return localErrorResult(t.Name(), fmt.Errorf("create screenshot directory: %w", err)), nil
		}
	}

	browser, err := t.resolver.resolveBrowser(toolRuntimeExecutables(t.cfg).Browser)
	if err != nil {
		return localUnavailableResult(t.Name(), err), nil
	}
	width := localIntArg(args, "viewport_width", 1600)
	height := localIntArg(args, "viewport_height", 900)
	if width <= 0 {
		width = 1600
	}
	if height <= 0 {
		height = 900
	}
	profileDir := filepath.Join(tmpDir, "profile")
	if err := os.Remove(outputPath); err != nil && !os.IsNotExist(err) {
		return localErrorResult(t.Name(), fmt.Errorf("remove previous screenshot: %w", err)), nil
	}
	processArgs := []string{
		"--headless=new",
		"--disable-gpu",
		"--hide-scrollbars",
		"--allow-file-access-from-files",
		fmt.Sprintf("--window-size=%d,%d", width, height),
		"--user-data-dir=" + profileDir,
		"--virtual-time-budget=3000",
		"--screenshot=" + outputPath,
		localFileURL(sourcePath, t.resolver.withDefaults().goos),
	}
	timeout := localToolTimeout(invocation, 60, 5*time.Second, 10*time.Minute)
	processResult, err := t.runner.Run(ctx, localProcessRequest{
		Path:               browser.Path,
		Args:               processArgs,
		Dir:                localContext.Workspace,
		Timeout:            timeout,
		SuccessFile:        outputPath,
		SuccessFileMinSize: int64(len(pngSignature)),
	})
	if err != nil {
		return localErrorResult(t.Name(), err), nil
	}
	if processResult.TimedOut {
		return localHTMLToPNGError(fmt.Errorf("browser render timeout after %s", timeout), map[string]any{
			"timed_out": true,
			"stdout":    strings.TrimSpace(processResult.Stdout),
			"stderr":    strings.TrimSpace(processResult.Stderr),
		}), nil
	}
	if processResult.ExitCode != 0 {
		return localHTMLToPNGError(fmt.Errorf("browser exited with code %d", processResult.ExitCode), map[string]any{
			"exit_code": processResult.ExitCode,
			"stdout":    strings.TrimSpace(processResult.Stdout),
			"stderr":    strings.TrimSpace(processResult.Stderr),
		}), nil
	}
	png, err := os.ReadFile(outputPath)
	if err != nil {
		return localHTMLToPNGError(fmt.Errorf("screenshot was not produced: %w", err), nil), nil
	}
	if len(png) < len(pngSignature) || !bytes.Equal(png[:len(pngSignature)], pngSignature) {
		return localHTMLToPNGError(fmt.Errorf("browser output is not a valid PNG"), map[string]any{"size": len(png)}), nil
	}

	value := map[string]any{
		"tool":   t.Name(),
		"ok":     true,
		"width":  width,
		"height": height,
		"size":   len(png),
	}
	if mode == "stateless" {
		value["mode"] = "stateless"
		value["png_base64"] = base64.StdEncoding.EncodeToString(png)
	} else {
		value["mode"] = "file"
		value["result_image_path"] = outputPath
	}
	return ToolResult{Value: value}, nil
}

var pngSignature = []byte{'\x89', 'P', 'N', 'G', '\r', '\n', '\x1a', '\n'}

func localHTMLToPNGError(err error, fields map[string]any) ToolResult {
	value := map[string]any{
		"tool":  "html_to_png",
		"ok":    false,
		"error": err.Error(),
	}
	for key, field := range fields {
		value[key] = field
	}
	return ToolResult{Value: value, IsError: true}
}

func localFileURL(path, goos string) string {
	slashPath := strings.ReplaceAll(path, "\\", "/")
	if goos == "windows" && !strings.HasPrefix(slashPath, "/") {
		slashPath = "/" + slashPath
	}
	return (&url.URL{Scheme: "file", Path: slashPath}).String()
}
