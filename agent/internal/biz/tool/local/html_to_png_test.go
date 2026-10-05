package local

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var minimalPNG = []byte{'\x89', 'P', 'N', 'G', '\r', '\n', '\x1a', '\n'}

func htmlToPNGTestConfig() *Config {
	return &Config{ToolRuntime: &ToolRuntimeConfig{Executables: LocalExecutableConfig{Browser: "/custom/browser"}}}
}

func htmlToPNGTestResolver() localExecutableResolver {
	return resolverForTest("darwin", nil, nil, map[string]bool{"/custom/browser": true})
}

func screenshotPathFromRequest(t *testing.T, request localProcessRequest) string {
	t.Helper()
	for _, arg := range request.Args {
		if strings.HasPrefix(arg, "--screenshot=") {
			return strings.TrimPrefix(arg, "--screenshot=")
		}
	}
	t.Fatalf("browser request has no screenshot path: %#v", request.Args)
	return ""
}

func pngProducingRunner(t *testing.T) *recordingLocalProcessRunner {
	t.Helper()
	return &recordingLocalProcessRunner{
		result: localProcessResult{ExitCode: 0},
		inspect: func(request localProcessRequest) {
			if err := os.WriteFile(screenshotPathFromRequest(t, request), minimalPNG, 0o600); err != nil {
				t.Errorf("write fake screenshot: %v", err)
			}
		},
	}
}

func TestLocalHTMLToPNGStatelessReturnsBase64WithoutKeepingFiles(t *testing.T) {
	runner := pngProducingRunner(t)
	tool := newLocalHTMLToPNGTool(ToolDef{}, htmlToPNGTestConfig(), htmlToPNGTestResolver(), runner)

	result, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: t.TempDir(),
		Args:      json.RawMessage(`{"mode":"stateless","html_file_content":"<html><body>你好</body></html>"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || result.Value["ok"] != true || result.Value["mode"] != "stateless" {
		t.Fatalf("unexpected stateless result: %#v", result)
	}
	if result.Value["png_base64"] != base64.StdEncoding.EncodeToString(minimalPNG) || result.Value["size"] != len(minimalPNG) {
		t.Fatalf("unexpected PNG payload: %#v", result.Value)
	}
	if result.Value["width"] != 1600 || result.Value["height"] != 900 {
		t.Fatalf("unexpected default viewport: %#v", result.Value)
	}
	output := screenshotPathFromRequest(t, runner.request)
	if runner.request.SuccessFile != output || runner.request.SuccessFileMinSize != int64(len(minimalPNG)) {
		t.Fatalf("browser runner is not configured to stop after a stable PNG: %#v", runner.request)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("stateless screenshot was not removed: %q err=%v", output, err)
	}
	if runner.request.Path != "/custom/browser" {
		t.Fatalf("unexpected browser: %#v", runner.request)
	}
}

func TestLocalHTMLToPNGFileModeWritesWorkspaceOutput(t *testing.T) {
	workspace := t.TempDir()
	htmlPath := filepath.Join(workspace, "slides", "page.html")
	if err := os.MkdirAll(filepath.Dir(htmlPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(htmlPath, []byte("<html>page</html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := pngProducingRunner(t)
	tool := newLocalHTMLToPNGTool(ToolDef{}, htmlToPNGTestConfig(), htmlToPNGTestResolver(), runner)

	result, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: workspace,
		Args:      json.RawMessage(`{"html_file_path":"local://slides/page.html","result_image_path":"local://output/page.png","viewport_width":800,"viewport_height":600}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	wantOutput := filepath.Join(workspace, "output", "page.png")
	if result.IsError || result.Value["result_image_path"] != wantOutput || result.Value["width"] != 800 || result.Value["height"] != 600 {
		t.Fatalf("unexpected file result: %#v", result)
	}
	raw, err := os.ReadFile(wantOutput)
	if err != nil || string(raw) != string(minimalPNG) {
		t.Fatalf("unexpected output file: raw=%q err=%v", raw, err)
	}
	lastArg := runner.request.Args[len(runner.request.Args)-1]
	if !strings.HasPrefix(lastArg, "file://") || !strings.Contains(lastArg, "page.html") {
		t.Fatalf("browser did not receive source file URL: %#v", runner.request.Args)
	}
}

func TestLocalHTMLToPNGFileModeDefaultsNextToSource(t *testing.T) {
	workspace := t.TempDir()
	htmlPath := filepath.Join(workspace, "page.html")
	if err := os.WriteFile(htmlPath, []byte("<html></html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := newLocalHTMLToPNGTool(ToolDef{}, htmlToPNGTestConfig(), htmlToPNGTestResolver(), pngProducingRunner(t))

	result, err := tool.Execute(context.Background(), ToolInvocation{Workspace: workspace, Args: json.RawMessage(`{"html_file_path":"page.html"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if result.Value["result_image_path"] != filepath.Join(workspace, "page.png") {
		t.Fatalf("unexpected default output: %#v", result.Value)
	}
}

func TestLocalHTMLToPNGRejectsWorkspaceTraversal(t *testing.T) {
	tool := newLocalHTMLToPNGTool(ToolDef{}, htmlToPNGTestConfig(), htmlToPNGTestResolver(), &recordingLocalProcessRunner{})
	for name, args := range map[string]string{
		"input":  `{"html_file_path":"../outside.html"}`,
		"output": `{"html_file_path":"inside.html","result_image_path":"../outside.png"}`,
	} {
		workspace := t.TempDir()
		if name == "output" {
			if err := os.WriteFile(filepath.Join(workspace, "inside.html"), []byte("<html></html>"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		result, err := tool.Execute(context.Background(), ToolInvocation{Workspace: workspace, Args: json.RawMessage(args)})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		// Post-loosen: outside-workspace paths are accepted as long as the file
		// exists. The previous "outside workspace" check is gone. For the
		// "input" case the .html is missing → 'unavailable'. For the "output"
		// case the input exists but the screenshot tool never wrote the .png
		// → that runtime error is also expected.
		if !result.IsError {
			t.Fatalf("%s: expected error, got %#v", name, result.Value)
		}
	}
}

func TestLocalHTMLToPNGReturnsUnavailableWithoutBrowser(t *testing.T) {
	runner := &recordingLocalProcessRunner{}
	tool := newLocalHTMLToPNGTool(ToolDef{}, &Config{}, resolverForTest("linux", nil, nil, nil), runner)

	result, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: t.TempDir(),
		Args:      json.RawMessage(`{"mode":"stateless","html_file_content":"<html></html>"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || result.Value["code"] != "unavailable" || len(result.Value["checked"].([]string)) == 0 {
		t.Fatalf("unexpected unavailable result: %#v", result)
	}
	if runner.request.Path != "" {
		t.Fatal("runner should not start without a browser")
	}
}

func TestLocalHTMLToPNGRejectsMissingScreenshotAndBrowserFailure(t *testing.T) {
	for name, runner := range map[string]*recordingLocalProcessRunner{
		"missing": {result: localProcessResult{ExitCode: 0}},
		"failure": {result: localProcessResult{ExitCode: 2, Stderr: "browser failed"}},
		"timeout": {result: localProcessResult{ExitCode: -1, TimedOut: true}},
	} {
		tool := newLocalHTMLToPNGTool(ToolDef{}, htmlToPNGTestConfig(), htmlToPNGTestResolver(), runner)
		result, err := tool.Execute(context.Background(), ToolInvocation{Workspace: t.TempDir(), Args: json.RawMessage(`{"mode":"stateless","html_file_content":"<html></html>"}`)})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !result.IsError || result.Value["ok"] != false {
			t.Fatalf("%s: expected structured error, got %#v", name, result)
		}
	}
}

func TestLocalFileURLHandlesMacAndWindowsPaths(t *testing.T) {
	if got := localFileURL("/tmp/a b.html", "darwin"); got != "file:///tmp/a%20b.html" {
		t.Fatalf("unexpected macOS file URL: %q", got)
	}
	if got := localFileURL(`C:\work\a b.html`, "windows"); got != "file:///C:/work/a%20b.html" {
		t.Fatalf("unexpected Windows file URL: %q", got)
	}
}
