package local

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

//go:embed cua_auto_bridge.py
var cuaAutoBridgeScript string

//go:embed computer_windows.ps1
var computerWindowsBridgeScript string

const cuaAutoResultPrefix = "__FAIRY_CUA_RESULT__"

var computerToolNames = map[string]bool{
	"computer_observe":   true,
	"computer_pointer":   true,
	"computer_keyboard":  true,
	"computer_window":    true,
	"computer_clipboard": true,
}

type localComputerTool struct {
	name          string
	nativeWindows bool
	schema        ToolDef
	cfg           *Config
	resolver      localExecutableResolver
	runner        localProcessRunner
}

// NewLocalComputerTool returns one of the cua-auto backed desktop tools. The
// Python library runs in a short-lived child process, so this integration does
// not allocate an HTTP port or require a background daemon.
func NewLocalComputerTool(name string, schema ToolDef, cfg *Config) Tool {
	return &localComputerTool{
		name:          name,
		nativeWindows: runtime.GOOS == "windows",
		schema:        schema,
		cfg:           cfg,
		resolver:      newLocalExecutableResolver(),
		runner:        osLocalProcessRunner{},
	}
}

func (t *localComputerTool) Name() string {
	return t.name
}

func (t *localComputerTool) Schema() ToolDef {
	return t.schema
}

func (t *localComputerTool) Execute(ctx context.Context, invocation ToolInvocation) (ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	if t == nil || !computerToolNames[t.name] {
		name := "computer"
		if t != nil && strings.TrimSpace(t.name) != "" {
			name = t.name
		}
		return localErrorResult(name, fmt.Errorf("unsupported computer tool %q", name)), nil
	}
	args, err := decodeLocalToolArgs(invocation)
	if err != nil {
		return localErrorResult(t.name, err), nil
	}
	action := strings.ToLower(strings.TrimSpace(localStringArg(args, "action")))
	if action == "" {
		return localErrorResult(t.name, fmt.Errorf("action is required")), nil
	}
	localContext, err := localToolContext(invocation)
	if err != nil {
		return localErrorResult(t.name, err), nil
	}
	// Normal single-image viewing: action=screenshot with image_path=<file> attaches
	// that file straight into the model context (vision_context.go injects it and the
	// main model looks at it natively) — no extra VQA round trip. Batch/parallel work
	// still goes through image_vqa so many images never crowd the context.
	if action == "screenshot" {
		if requested := strings.TrimSpace(localStringArg(args, "image_path")); requested != "" {
			return t.attachmentForLocalImage(localContext.Workspace, requested)
		}
	}
	// Pixel based actions (OCR / YOLO) have no accessibility tree to read, so
	// they are served by the local vision service instead of the Windows broker.
	if isVisionAction(action) {
		return t.executeVision(ctx, action, args, localContext.Workspace, invocation)
	}
	request, err := t.prepareRequest(action, args, localContext.Workspace)
	if err != nil {
		return localErrorResult(t.name, err), nil
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return localErrorResult(t.name, fmt.Errorf("encode computer request: %w", err)), nil
	}
	if t.nativeWindows {
		nativeResult, nativeErr := t.executeWindowsBridge(ctx, payload, localContext.Workspace, invocation, action)
		if nativeErr == nil && !nativeResult.IsError {
			return nativeResult, nil
		}
	}

	repoRoot := ""
	if t.cfg != nil {
		repoRoot = t.cfg.RepoRoot
	}
	python, err := t.resolver.resolvePythonForRepo(toolRuntimeExecutables(t.cfg).Python, repoRoot)
	if err != nil {
		if t.nativeWindows {
			return t.executeWindowsBridge(ctx, payload, localContext.Workspace, invocation, action)
		}
		return localUnavailableResult(t.name, err), nil
	}
	bridgePath, cleanup, err := writeCuaAutoBridge()
	if err != nil {
		return localErrorResult(t.name, err), nil
	}
	defer cleanup()

	processArgs := append([]string{}, python.PrefixArgs...)
	processArgs = append(processArgs, bridgePath)
	timeout := localToolTimeout(invocation, 30, 5*time.Second, 2*time.Minute)
	processResult, err := t.runner.Run(ctx, localProcessRequest{
		Path:    python.Path,
		Args:    processArgs,
		Dir:     localContext.Workspace,
		Env:     []string{"PYTHONUTF8=1", "PYTHONIOENCODING=utf-8"},
		Stdin:   payload,
		Timeout: timeout,
	})
	if err != nil {
		return localErrorResult(t.name, err), nil
	}
	if processResult.TimedOut {
		return ToolResult{Value: map[string]any{
			"tool":      t.name,
			"ok":        false,
			"code":      "timeout",
			"error":     fmt.Sprintf("computer action timed out after %s", timeout),
			"timed_out": true,
			"stdout":    strings.TrimSpace(processResult.Stdout),
			"stderr":    strings.TrimSpace(processResult.Stderr),
		}, IsError: true}, nil
	}

	value, parseErr := parseCuaAutoResult(processResult.Stdout)
	if parseErr != nil {
		message := strings.TrimSpace(processResult.Stderr)
		if message == "" {
			message = strings.TrimSpace(processResult.Stdout)
		}
		if message == "" {
			message = parseErr.Error()
		}
		return ToolResult{Value: map[string]any{
			"tool":      t.name,
			"ok":        false,
			"code":      "bridge_error",
			"error":     message,
			"exit_code": processResult.ExitCode,
		}, IsError: true}, nil
	}
	value["tool"] = t.name
	ok, _ := value["ok"].(bool)
	if !ok && t.nativeWindows && isMissingCuaAutoError(value) {
		return t.executeWindowsBridge(ctx, payload, localContext.Workspace, invocation, action)
	}
	return withScreenshotAttachment(t.name, action, ToolResult{Value: value, IsError: !ok}), nil
}

func isMissingCuaAutoError(value map[string]any) bool {
	message, _ := value["error"].(string)
	lower := strings.ToLower(message)
	return strings.Contains(lower, "no module named cua_auto") ||
		strings.Contains(lower, "no module named pywinctl") ||
		strings.Contains(lower, "no module named 'cua_auto'") ||
		strings.Contains(lower, "no module named 'pywinctl'")
}

func (t *localComputerTool) executeWindowsBridge(ctx context.Context, payload []byte, workspace string, invocation ToolInvocation, action string) (ToolResult, error) {
	// Prefer the compiled C# broker: it is DPI-aware, keeps screenshot/window/
	// cursor in one coordinate space and verifies every pointer move. The
	// PowerShell bridge stays as a fallback when the broker cannot be built.
	if result, ok := t.executeWindowsBroker(ctx, payload, workspace, invocation); ok {
		return withScreenshotAttachment(t.name, action, result), nil
	}
	shell, err := t.resolver.resolveShell(toolRuntimeExecutables(t.cfg).Shell)
	if err != nil {
		return localUnavailableResult(t.name, err), nil
	}
	bridgePath, cleanup, err := writeWindowsComputerBridge()
	if err != nil {
		return localErrorResult(t.name, err), nil
	}
	defer cleanup()
	args := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", bridgePath}
	timeout := localToolTimeout(invocation, 30, 5*time.Second, 2*time.Minute)
	processResult, err := t.runner.Run(ctx, localProcessRequest{
		Path:    shell.Path,
		Args:    args,
		Dir:     workspace,
		Stdin:   payload,
		Timeout: timeout,
	})
	if err != nil {
		return localErrorResult(t.name, err), nil
	}
	if processResult.TimedOut {
		return ToolResult{Value: map[string]any{
			"tool":      t.name,
			"ok":        false,
			"code":      "timeout",
			"error":     fmt.Sprintf("computer action timed out after %s", timeout),
			"timed_out": true,
			"stdout":    strings.TrimSpace(processResult.Stdout),
			"stderr":    strings.TrimSpace(processResult.Stderr),
		}, IsError: true}, nil
	}
	value, parseErr := parseCuaAutoResult(processResult.Stdout)
	if parseErr != nil {
		message := strings.TrimSpace(processResult.Stderr)
		if message == "" {
			message = strings.TrimSpace(processResult.Stdout)
		}
		if message == "" {
			message = parseErr.Error()
		}
		return ToolResult{Value: map[string]any{
			"tool":      t.name,
			"ok":        false,
			"code":      "bridge_error",
			"error":     message,
			"exit_code": processResult.ExitCode,
		}, IsError: true}, nil
	}
	value["tool"] = t.name
	ok, _ := value["ok"].(bool)
	return withScreenshotAttachment(t.name, action, ToolResult{Value: value, IsError: !ok}), nil
}

func withScreenshotAttachment(toolName, action string, result ToolResult) ToolResult {
	if result.IsError || strings.ToLower(strings.TrimSpace(action)) != "screenshot" {
		return result
	}
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "computer_observe", "browser":
	default:
		return result
	}
	path := strings.TrimSpace(localStringArg(result.Value, "path", "output_path"))
	if path == "" {
		return result
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() <= 0 {
		return result
	}
	attachment := ToolAttachment{
		Path:      path,
		MIME:      "image/png",
		SizeBytes: info.Size(),
		Width:     localIntArg(result.Value, "width", 0),
		Height:    localIntArg(result.Value, "height", 0),
		Label:     strings.ToLower(strings.TrimSpace(toolName)) + " screenshot",
	}
	result.Attachments = append(result.Attachments, attachment)
	return result
}

func writeWindowsComputerBridge() (string, func(), error) {
	file, err := os.CreateTemp("", "fairy-computer-windows-*.ps1")
	if err != nil {
		return "", nil, fmt.Errorf("create Windows computer bridge: %w", err)
	}
	path := file.Name()
	cleanup := func() { _ = os.Remove(path) }
	if _, err := file.WriteString(computerWindowsBridgeScript); err != nil {
		_ = file.Close()
		cleanup()
		return "", nil, fmt.Errorf("write Windows computer bridge: %w", err)
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("close Windows computer bridge: %w", err)
	}
	return path, cleanup, nil
}
func (t *localComputerTool) prepareRequest(action string, args map[string]any, workspace string) (map[string]any, error) {
	request := make(map[string]any, len(args)+2)
	for key, value := range args {
		request[key] = value
	}
	request["tool"] = t.name
	request["action"] = action

	if t.name == "computer_observe" && action == "screenshot" {
		output := strings.TrimSpace(localStringArg(args, "output_path"))
		if output == "" {
			output = filepath.Join("result", "computer-screenshots", time.Now().Format("20060102-150405.000")+".png")
		}
		resolved, err := resolveLocalWorkspacePath(workspace, output)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
			return nil, fmt.Errorf("create screenshot directory: %w", err)
		}
		request["output_path"] = resolved
	}
	if t.name == "computer_window" && action == "open" {
		target := strings.TrimSpace(localStringArg(args, "target"))
		if target == "" {
			return nil, fmt.Errorf("target is required")
		}
		if !isHTTPURL(target) {
			resolved, err := resolveLocalWorkspacePath(workspace, target)
			if err != nil {
				return nil, err
			}
			if _, err := os.Stat(resolved); err != nil {
				return nil, fmt.Errorf("open target is unavailable: %w", err)
			}
			request["target"] = resolved
		}
	}
	return request, nil
}

// attachmentForLocalImage turns a workspace-relative or absolute image path into a
// ToolResult carrying that file as an image attachment, so vision_context.go can
// inject it into the main model context for native (no-extra-API) viewing.
func (t *localComputerTool) attachmentForLocalImage(workspace, requested string) (ToolResult, error) {
	if isHTTPURL(requested) {
		return localErrorResult(t.name, fmt.Errorf("image_path must be a local file for native viewing; use image_vqa for remote URLs")), nil
	}
	resolved := requested
	if !filepath.IsAbs(resolved) {
		if workspace == "" {
			return localErrorResult(t.name, fmt.Errorf("relative image_path %q needs an agent workspace", requested)), nil
		}
		resolved = filepath.Join(workspace, resolved)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return localErrorResult(t.name, fmt.Errorf("image_path is unavailable: %w", err)), nil
	}
	if info.IsDir() {
		return localErrorResult(t.name, fmt.Errorf("image_path is a directory: %s", resolved)), nil
	}
	return ToolResult{
		Value: map[string]any{
			"tool":  t.name,
			"ok":    true,
			"path":  resolved,
			"bytes": info.Size(),
			"note":  "local image attached to the vision context; inspect it directly, do not call image_vqa",
		},
		Attachments: []ToolAttachment{{Path: resolved, MIME: "image/png", SizeBytes: info.Size(), Label: "local image"}},
	}, nil
}

func isHTTPURL(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

func writeCuaAutoBridge() (string, func(), error) {
	file, err := os.CreateTemp("", "fairy-cua-auto-*.py")
	if err != nil {
		return "", nil, fmt.Errorf("create cua-auto bridge: %w", err)
	}
	path := file.Name()
	cleanup := func() { _ = os.Remove(path) }
	if _, err := file.WriteString(cuaAutoBridgeScript); err != nil {
		_ = file.Close()
		cleanup()
		return "", nil, fmt.Errorf("write cua-auto bridge: %w", err)
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("close cua-auto bridge: %w", err)
	}
	return path, cleanup, nil
}

func parseCuaAutoResult(stdout string) (map[string]any, error) {
	lines := strings.Split(stdout, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimSpace(lines[index])
		if !strings.HasPrefix(line, cuaAutoResultPrefix) {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, cuaAutoResultPrefix))
		var value map[string]any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return nil, fmt.Errorf("decode cua-auto bridge result: %w", err)
		}
		if value == nil {
			return nil, fmt.Errorf("cua-auto bridge returned an empty result")
		}
		return value, nil
	}
	return nil, fmt.Errorf("cua-auto bridge returned no result")
}
