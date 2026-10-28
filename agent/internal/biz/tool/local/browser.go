package local

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed browser_bridge.mjs
var browserBridgeSource []byte

type localBrowserTool struct {
	schema   ToolDef
	cfg      *Config
	resolver localExecutableResolver

	mu         sync.Mutex
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stdout     *bufio.Reader
	stderr     lockedBuffer
	done       chan error
	bridgePath string
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func (b *lockedBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.b.Reset()
}

func NewLocalBrowserTool(schema ToolDef, cfg *Config) Tool {
	return newLocalBrowserTool(schema, cfg, newLocalExecutableResolver())
}

func newLocalBrowserTool(schema ToolDef, cfg *Config, resolver localExecutableResolver) Tool {
	return &localBrowserTool{schema: schema, cfg: cfg, resolver: resolver}
}

func (t *localBrowserTool) Name() string { return "browser" }

func (t *localBrowserTool) Schema() ToolDef { return t.schema }

// Close is called by the tool registry when the agent exits normally. Without
// it the bridge can be reaped by cmd.Wait while its Playwright Chromium child
// survives as an orphan.
func (t *localBrowserTool) Close() error {
	t.stopBridge()
	return nil
}

func (t *localBrowserTool) Execute(ctx context.Context, invocation ToolInvocation) (ToolResult, error) {
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

	action := strings.ToLower(strings.TrimSpace(localStringArg(args, "action")))
	if action == "" {
		return localErrorResult(t.Name(), fmt.Errorf("action is required")), nil
	}
	if action == "close" {
		t.stopBridge()
		return ToolResult{Value: map[string]any{"tool": t.Name(), "ok": true, "action": action}}, nil
	}
	if action == "reset" {
		t.stopBridge()
	}
	if !browserActionSupported(action) {
		return localErrorResult(t.Name(), fmt.Errorf("unsupported browser action: %s", action)), nil
	}

	request := make(map[string]any, len(args)+3)
	for key, value := range args {
		request[key] = value
	}
	request["action"] = action
	request["workspace"] = localContext.Workspace
	if output := strings.TrimSpace(localStringArg(args, "output_path")); output != "" {
		resolved, resolveErr := resolveLocalWorkspacePath(localContext.Workspace, output)
		if resolveErr != nil {
			return localErrorResult(t.Name(), resolveErr), nil
		}
		if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
			return localErrorResult(t.Name(), fmt.Errorf("create screenshot directory: %w", err)), nil
		}
		request["output_path"] = resolved
	}

	repoRoot := ""
	if t.cfg != nil {
		repoRoot = t.cfg.RepoRoot
	}
	node, err := t.resolver.resolveNodeForRepo(toolRuntimeExecutables(t.cfg).Node, repoRoot)
	if err != nil {
		return localUnavailableResult(t.Name(), err), nil
	}
	browser, err := t.resolver.resolveBrowser(toolRuntimeExecutables(t.cfg).Browser)
	if err != nil {
		return localUnavailableResult(t.Name(), err), nil
	}

	response, err := t.exchangeLocked(ctx, node, browser.Path, repoRoot, localContext.Workspace, request, localToolTimeout(invocation, 120, 5*time.Second, 10*time.Minute))
	if err != nil {
		return localErrorResult(t.Name(), err), nil
	}
	response["tool"] = t.Name()
	ok, _ := response["ok"].(bool)
	return withScreenshotAttachment(t.Name(), action, ToolResult{Value: response, IsError: !ok}), nil
}

func browserActionSupported(action string) bool {
	switch action {
	case "open", "goto", "snapshot", "click", "fill", "type", "press", "hover", "check", "uncheck", "select_option", "reload", "eval", "wait", "expect_text", "screenshot", "requests", "console", "sse", "wait_sse", "assert_sse", "pages", "close", "reset":
		return true
	default:
		return false
	}
}

func (t *localBrowserTool) exchangeLocked(
	ctx context.Context,
	node localExecutable,
	browserPath, repoRoot, workspace string,
	request map[string]any,
	timeout time.Duration,
) (map[string]any, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if err := t.ensureBridgeLocked(node, browserPath, repoRoot, workspace); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode browser request: %w", err)
	}
	if _, err := t.stdin.Write(append(payload, '\n')); err != nil {
		t.stopBridgeLocked()
		return nil, fmt.Errorf("write browser bridge request: %w", err)
	}

	type readResult struct {
		line []byte
		err  error
	}
	readCh := make(chan readResult, 1)
	go func() {
		line, err := t.stdout.ReadBytes('\n')
		readCh <- readResult{line: line, err: err}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-readCh:
		if result.err != nil && len(result.line) == 0 {
			detail := strings.TrimSpace(t.stderr.String())
			t.stopBridgeLocked()
			if detail == "" {
				detail = result.err.Error()
			}
			return nil, fmt.Errorf("browser bridge closed: %s", detail)
		}
		var response map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(result.line), &response); err != nil {
			detail := strings.TrimSpace(t.stderr.String())
			if detail == "" {
				detail = string(result.line)
			}
			return nil, fmt.Errorf("parse browser bridge response: %w: %s", err, truncateBrowserText(detail, 1200))
		}
		return response, nil
	case <-ctx.Done():
		t.stopBridgeLocked()
		return nil, ctx.Err()
	case <-timer.C:
		detail := strings.TrimSpace(t.stderr.String())
		t.stopBridgeLocked()
		if detail != "" {
			return nil, fmt.Errorf("browser action timed out after %s: %s", timeout, truncateBrowserText(detail, 1200))
		}
		return nil, fmt.Errorf("browser action timed out after %s", timeout)
	}
}

func (t *localBrowserTool) ensureBridgeLocked(node localExecutable, browserPath, repoRoot, workspace string) error {
	if t.cmd != nil && t.done != nil {
		select {
		case <-t.done:
			t.clearProcessLocked()
		default:
			return nil
		}
	}
	if strings.TrimSpace(repoRoot) == "" {
		return fmt.Errorf("repo root is required for playwright-core resolution")
	}
	bridgePath, err := writeBrowserBridge()
	if err != nil {
		return err
	}
	args := append([]string{}, node.PrefixArgs...)
	args = append(args, bridgePath)
	cmd := exec.Command(node.Path, args...)
	cmd.Dir = workspace
	cmd.Env = mergeProcessEnvironment(os.Environ(), []string{
		"FAIRY_REPO_ROOT=" + repoRoot,
		"FAIRY_BROWSER_PATH=" + browserPath,
		"PYTHONUTF8=1",
		"PYTHONIOENCODING=utf-8",
	})
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = os.Remove(bridgePath)
		return fmt.Errorf("open browser bridge stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		_ = os.Remove(bridgePath)
		return fmt.Errorf("open browser bridge stdout: %w", err)
	}
	t.stderr.Reset()
	cmd.Stderr = &t.stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = os.Remove(bridgePath)
		return fmt.Errorf("start browser bridge: %w", err)
	}
	t.cmd = cmd
	t.stdin = stdin
	t.stdout = bufio.NewReaderSize(stdout, 64*1024)
	t.done = make(chan error, 1)
	t.bridgePath = bridgePath
	go func() {
		err := cmd.Wait()
		t.done <- err
		close(t.done)
	}()
	return nil
}

func (t *localBrowserTool) stopBridge() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopBridgeLocked()
}

func (t *localBrowserTool) stopBridgeLocked() {
	if t.cmd == nil {
		return
	}
	if t.stdin != nil {
		_ = t.stdin.Close()
		t.stdin = nil
	}
	exited := false
	if t.done != nil {
		select {
		case <-t.done:
			exited = true
		case <-time.After(time.Second):
		}
	}
	if !exited && t.cmd.Process != nil {
		// Closing stdin lets browser_bridge.mjs call browser.close(). If it does
		// not finish, kill the whole tree rather than only the Node bridge.
		_ = terminateProcessTree(t.cmd.Process)
	}
	if !exited && t.done != nil {
		select {
		case <-t.done:
		case <-time.After(3 * time.Second):
		}
	}
	t.clearProcessLocked()
}

func (t *localBrowserTool) clearProcessLocked() {
	if t.bridgePath != "" {
		_ = os.Remove(t.bridgePath)
	}
	t.cmd = nil
	t.stdin = nil
	t.stdout = nil
	t.done = nil
	t.bridgePath = ""
	t.stderr.Reset()
}

func writeBrowserBridge() (string, error) {
	file, err := os.CreateTemp("", "fairy-browser-bridge-*.mjs")
	if err != nil {
		return "", fmt.Errorf("create browser bridge: %w", err)
	}
	path := file.Name()
	if _, err := file.Write(browserBridgeSource); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write browser bridge: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close browser bridge: %w", err)
	}
	return path, nil
}

func truncateBrowserText(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "...[truncated]"
}
