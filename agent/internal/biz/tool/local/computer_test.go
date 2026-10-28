package local

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLocalComputerToolBuildsScreenshotPath(t *testing.T) {
	workspace := t.TempDir()
	runner := &recordingLocalProcessRunner{
		result: localProcessResult{Stdout: cuaAutoResultPrefix + `{"ok":true,"path":"captured.png"}` + "\n"},
	}
	tool := &localComputerTool{
		name:     "computer_observe",
		schema:   ToolDef{},
		cfg:      localExecuteTestConfig(),
		resolver: localExecuteTestResolver(),
		runner:   runner,
	}

	result, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: workspace,
		Args:      json.RawMessage(`{"action":"screenshot"}`),
	})
	if err != nil || result.IsError || result.Value["ok"] != true {
		t.Fatalf("unexpected screenshot result: result=%#v err=%v", result, err)
	}

	var request map[string]any
	if err := json.Unmarshal(runner.request.Stdin, &request); err != nil {
		t.Fatalf("bridge request is invalid JSON: %v", err)
	}
	output, _ := request["output_path"].(string)
	if !strings.HasPrefix(output, filepath.Join(workspace, "result", "computer-screenshots")) || filepath.Ext(output) != ".png" {
		t.Fatalf("unexpected screenshot path: %q", output)
	}
	if len(runner.request.Args) != 1 || !strings.HasSuffix(runner.request.Args[0], ".py") {
		t.Fatalf("bridge script argument missing: %#v", runner.request.Args)
	}
}

func TestLocalComputerToolPropagatesBridgeError(t *testing.T) {
	runner := &recordingLocalProcessRunner{
		result: localProcessResult{Stdout: cuaAutoResultPrefix + `{"ok":false,"code":"execution_error","error":"denied"}` + "\n"},
	}
	tool := &localComputerTool{
		name:     "computer_pointer",
		schema:   ToolDef{},
		cfg:      localExecuteTestConfig(),
		resolver: localExecuteTestResolver(),
		runner:   runner,
	}
	result, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: t.TempDir(),
		Args:      json.RawMessage(`{"action":"click","x":10,"y":20}`),
	})
	if err != nil || !result.IsError || result.Value["error"] != "denied" {
		t.Fatalf("unexpected bridge error result: result=%#v err=%v", result, err)
	}
}

func TestLocalComputerWindowOpenValidatesTarget(t *testing.T) {
	workspace := t.TempDir()
	tool := &localComputerTool{name: "computer_window", schema: ToolDef{}}

	request, err := tool.prepareRequest("open", map[string]any{"target": "https://example.com"}, workspace)
	if err != nil || request["target"] != "https://example.com" {
		t.Fatalf("URL target should pass through: request=%#v err=%v", request, err)
	}
	if _, err := tool.prepareRequest("open", map[string]any{"target": "../outside.txt"}, workspace); err == nil {
		t.Fatal("workspace escape must be rejected")
	}

	localPath := filepath.Join(workspace, "note.txt")
	if err := os.WriteFile(localPath, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	request, err = tool.prepareRequest("open", map[string]any{"target": "note.txt"}, workspace)
	if err != nil || request["target"] != localPath {
		t.Fatalf("workspace file was not resolved: request=%#v err=%v", request, err)
	}
}

func TestLocalComputerEnvironmentSmoke(t *testing.T) {
	if os.Getenv("RUN_LOCAL_COMPUTER_SMOKE") != "1" {
		t.Skip("set RUN_LOCAL_COMPUTER_SMOKE=1 to exercise cua-auto on the current desktop")
	}
	repoRoot, err := filepath.Abs("../../../../..")
	if err != nil {
		t.Fatal(err)
	}
	tool := NewLocalComputerTool("computer_observe", ToolDef{}, &Config{RepoRoot: repoRoot})
	result, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: t.TempDir(),
		Args:      json.RawMessage(`{"action":"screen_info"}`),
	})
	if err != nil || result.IsError {
		t.Fatalf("computer smoke failed: result=%#v err=%v", result, err)
	}
	if result.Value["ok"] != true || result.Value["width"] == nil || result.Value["height"] == nil {
		t.Fatalf("computer smoke returned unexpected result: %#v", result.Value)
	}
	workspace := t.TempDir()
	screenshot, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: workspace,
		Args:      json.RawMessage(`{"action":"screenshot"}`),
	})
	if err != nil || screenshot.IsError {
		t.Fatalf("computer screenshot smoke failed: result=%#v err=%v", screenshot, err)
	}
	path, _ := screenshot.Value["path"].(string)
	if info, statErr := os.Stat(path); statErr != nil || info.Size() == 0 {
		t.Fatalf("computer screenshot missing: path=%q stat=%v", path, statErr)
	}
}
func TestLocalComputerToolUsesWindowsBridge(t *testing.T) {
	powerShell := `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`
	runner := &recordingLocalProcessRunner{
		result: localProcessResult{Stdout: cuaAutoResultPrefix + `{"ok":true,"width":1920,"height":1080}` + "\n"},
	}
	tool := &localComputerTool{
		name:          "computer_observe",
		nativeWindows: true,
		schema:        ToolDef{},
		cfg:           localExecuteTestConfig(),
		resolver:      resolverForTest("windows", nil, map[string]string{"powershell.exe": powerShell}, map[string]bool{powerShell: true}),
		runner:        runner,
	}
	result, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: t.TempDir(),
		Args:      json.RawMessage(`{"action":"screen_info"}`),
	})
	if err != nil || result.IsError || result.Value["ok"] != true {
		t.Fatalf("unexpected Windows bridge result: result=%#v err=%v", result, err)
	}
	if runner.request.Path != powerShell {
		t.Fatalf("expected PowerShell bridge, got %q", runner.request.Path)
	}
	hasFileArg := false
	for _, arg := range runner.request.Args {
		if arg == "-File" {
			hasFileArg = true
			break
		}
	}
	if !hasFileArg {
		t.Fatalf("Windows bridge did not invoke a script file: %#v", runner.request.Args)
	}
}

// The C# broker must win over the PowerShell fallback whenever it is available,
// because only the broker keeps screenshot, window and cursor in one physical
// pixel space on a DPI-scaled desktop.
func TestLocalComputerToolPrefersWindowsBroker(t *testing.T) {
	brokerPath := filepath.Join(t.TempDir(), computerBrokerExecutableName)
	if err := os.WriteFile(brokerPath, []byte("stub"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := resolveWindowsComputerBroker
	resolveWindowsComputerBroker = func(string, string) (string, error) { return brokerPath, nil }
	t.Cleanup(func() { resolveWindowsComputerBroker = original })

	runner := &recordingLocalProcessRunner{
		result: localProcessResult{Stdout: cuaAutoResultPrefix + `{"ok":true,"width":3840,"height":2160,"coordinate_space":"screen","geometry_hash":"geom:0,0,3840,2160"}` + "\n"},
	}
	tool := &localComputerTool{
		name:          "computer_observe",
		nativeWindows: true,
		schema:        ToolDef{},
		cfg:           localExecuteTestConfig(),
		resolver:      localExecuteTestResolver(),
		runner:        runner,
	}

	result, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: t.TempDir(),
		Args:      json.RawMessage(`{"action":"screen_info"}`),
	})
	if err != nil || result.IsError || result.Value["ok"] != true {
		t.Fatalf("unexpected broker result: result=%#v err=%v", result, err)
	}
	if runner.request.Path != brokerPath {
		t.Fatalf("expected the C# broker to run, got %q", runner.request.Path)
	}
	if len(runner.request.Args) != 0 {
		t.Fatalf("broker should receive its request on stdin, got args %#v", runner.request.Args)
	}
	if result.Value["coordinate_space"] != "screen" {
		t.Fatalf("broker result lost coordinate space: %#v", result.Value)
	}
}

// Falls back to PowerShell when the broker cannot be produced, so a machine
// without a C# toolchain still has desktop control.
func TestLocalComputerToolFallsBackToPowerShellBridge(t *testing.T) {
	powerShell := `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`
	original := resolveWindowsComputerBroker
	resolveWindowsComputerBroker = func(string, string) (string, error) {
		return "", os.ErrNotExist
	}
	t.Cleanup(func() { resolveWindowsComputerBroker = original })

	runner := &recordingLocalProcessRunner{
		result: localProcessResult{Stdout: cuaAutoResultPrefix + `{"ok":true,"width":1920,"height":1080}` + "\n"},
	}
	tool := &localComputerTool{
		name:          "computer_observe",
		nativeWindows: true,
		schema:        ToolDef{},
		cfg:           localExecuteTestConfig(),
		resolver:      resolverForTest("windows", nil, map[string]string{"powershell.exe": powerShell}, map[string]bool{powerShell: true}),
		runner:        runner,
	}
	result, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: t.TempDir(),
		Args:      json.RawMessage(`{"action":"screen_info"}`),
	})
	if err != nil || result.IsError || result.Value["ok"] != true {
		t.Fatalf("unexpected fallback result: result=%#v err=%v", result, err)
	}
	if runner.request.Path != powerShell {
		t.Fatalf("expected PowerShell fallback, got %q", runner.request.Path)
	}
}

// End-to-end: compile the embedded C# source and run its self test. Skipped
// automatically when the host has no csc.exe (non-Windows CI, stripped images).
func TestWindowsComputerBrokerCompilesAndSelfTests(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the computer broker is Windows-only")
	}
	if _, err := findCSharpCompiler(); err != nil {
		t.Skipf("no C# compiler available: %v", err)
	}

	executable, err := ensureWindowsComputerBroker(t.TempDir(), "windows")
	if err != nil {
		t.Fatalf("build computer broker: %v", err)
	}
	if info, statErr := os.Stat(executable); statErr != nil || info.Size() == 0 {
		t.Fatalf("computer broker executable missing: %v", statErr)
	}

	output, err := exec.Command(executable, "--self-test").Output()
	if err != nil {
		t.Fatalf("run computer broker self test: %v", err)
	}
	value, parseErr := parseCuaAutoResult(string(output))
	if parseErr != nil {
		t.Fatalf("parse broker self test: %v", parseErr)
	}
	if value["ok"] != true {
		t.Fatalf("broker self test failed: %#v", value)
	}
	if value["geometry_hash"] == nil || value["monitors"] == nil {
		t.Fatalf("broker self test is missing geometry data: %#v", value)
	}
}

func TestVisionActionRouting(t *testing.T) {
	expected := map[string]string{
		"vision_start":    "session/start",
		"vision_stop":     "session/stop",
		"vision_status":   "health",
		"ocr":             "ocr",
		"find_text":       "find_text",
		"detect_elements": "elements",
		"locate_text":     "locate",
	}
	for action, route := range expected {
		if !isVisionAction(action) {
			t.Fatalf("%s should be a vision action", action)
		}
		if got := visionActions[action]; got != route {
			t.Fatalf("visionActions[%s] = %q, want %q", action, got, route)
		}
		if got := visionActions[strings.ToUpper(action)]; got != "" {
			t.Fatalf("visionActions should be keyed lower case, got %q", got)
		}
	}
	for _, action := range []string{"screenshot", "click", "ui_tree", ""} {
		if isVisionAction(action) {
			t.Fatalf("%q must not route to the vision service", action)
		}
	}
	if !isVisionAction("  Find_Text  ") {
		t.Fatal("vision action lookup should trim and lower-case its input")
	}
}

// Vision boxes come back in the analysed frame's space; when the caller passed a
// region they must be shifted into screen pixels or the click lands in the wrong
// place - the exact failure mode the broker was written to remove.
func TestOffsetVisionCoordinatesAppliesRegionOrigin(t *testing.T) {
	body := map[string]any{
		"texts": []any{
			map[string]any{
				"text":   "开始游戏",
				"box":    map[string]any{"x": float64(100), "y": float64(40), "width": float64(200), "height": float64(50)},
				"center": map[string]any{"x": float64(200), "y": float64(65)},
			},
		},
		"matched": map[string]any{
			"box":    map[string]any{"x": float64(100), "y": float64(40), "width": float64(200), "height": float64(50)},
			"center": map[string]any{"x": float64(200), "y": float64(65)},
		},
		"point": map[string]any{"x": float64(200), "y": float64(65)},
	}
	args := map[string]any{
		"region": map[string]any{"x": float64(144), "y": float64(420), "width": float64(1942), "height": float64(1136)},
	}

	offsetVisionCoordinates(body, args)

	if body["coordinate_space"] != "screen" {
		t.Fatalf("coordinate_space = %v, want screen", body["coordinate_space"])
	}
	entry := body["texts"].([]any)[0].(map[string]any)
	if entry["box"].(map[string]any)["x"] != 244 || entry["box"].(map[string]any)["y"] != 460 {
		t.Fatalf("box was not shifted into screen space: %#v", entry["box"])
	}
	if entry["center"].(map[string]any)["x"] != 344 || entry["center"].(map[string]any)["y"] != 485 {
		t.Fatalf("centre was not shifted into screen space: %#v", entry["center"])
	}
	if body["point"].(map[string]any)["x"] != 344 || body["point"].(map[string]any)["y"] != 485 {
		t.Fatalf("point was not shifted into screen space: %#v", body["point"])
	}
}

func TestOffsetVisionCoordinatesWithoutRegionKeepsImageSpace(t *testing.T) {
	body := map[string]any{
		"texts": []any{map[string]any{"box": map[string]any{"x": float64(10), "y": float64(20)}}},
	}
	offsetVisionCoordinates(body, map[string]any{})
	entry := body["texts"].([]any)[0].(map[string]any)
	if entry["box"].(map[string]any)["x"] != 10 {
		t.Fatalf("coordinates should be untouched without a region: %#v", entry["box"])
	}
}

// The pixel path must be reachable through computer_observe and must forward the
// right route, workspace-resolved image paths and source default.
func TestLocalComputerToolRoutesVisionActions(t *testing.T) {
	originalEnsure := ensureVisionServiceFunc
	originalPost := visionPostFunc
	t.Cleanup(func() {
		ensureVisionServiceFunc = originalEnsure
		visionPostFunc = originalPost
	})

	ensureVisionServiceFunc = func(context.Context, string) (string, error) {
		return "http://127.0.0.1:8791", nil
	}
	var gotURL string
	var gotPayload map[string]any
	visionPostFunc = func(_ context.Context, url string, payload map[string]any, _ time.Duration) (map[string]any, error) {
		gotURL = url
		gotPayload = payload
		return map[string]any{"ok": true, "matched": map[string]any{"text": "开始游戏"}}, nil
	}

	workspace := t.TempDir()
	tool := &localComputerTool{
		name:          "computer_observe",
		nativeWindows: true,
		schema:        ToolDef{},
		cfg:           localExecuteTestConfig(),
		resolver:      localExecuteTestResolver(),
		runner:        &recordingLocalProcessRunner{},
	}

	result, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: workspace,
		Args:      json.RawMessage(`{"action":"find_text","text":"开始游戏","image_path":"shot.png"}`),
	})
	if err != nil || result.IsError {
		t.Fatalf("vision action failed: result=%#v err=%v", result, err)
	}
	if gotURL != "http://127.0.0.1:8791/find_text" {
		t.Fatalf("unexpected vision route: %q", gotURL)
	}
	if gotPayload["source"] != "screen" {
		t.Fatalf("vision request should default to the screen source: %#v", gotPayload)
	}
	wantPath := filepath.Join(workspace, "shot.png")
	if gotPayload["image_path"] != wantPath {
		t.Fatalf("image_path = %v, want %v", gotPayload["image_path"], wantPath)
	}
	if result.Value["ok"] != true {
		t.Fatalf("vision result lost its payload: %#v", result.Value)
	}
}

func TestLocalComputerToolReportsVisionUnavailable(t *testing.T) {
	originalEnsure := ensureVisionServiceFunc
	t.Cleanup(func() { ensureVisionServiceFunc = originalEnsure })
	ensureVisionServiceFunc = func(context.Context, string) (string, error) {
		return "", os.ErrNotExist
	}

	tool := &localComputerTool{
		name:          "computer_observe",
		nativeWindows: true,
		schema:        ToolDef{},
		cfg:           localExecuteTestConfig(),
		resolver:      localExecuteTestResolver(),
		runner:        &recordingLocalProcessRunner{},
	}
	result, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: t.TempDir(),
		Args:      json.RawMessage(`{"action":"ocr"}`),
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !result.IsError || result.Value["code"] != "vision_unavailable" {
		t.Fatalf("expected a vision_unavailable result, got %#v", result)
	}
}
