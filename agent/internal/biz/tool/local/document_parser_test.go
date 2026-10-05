package local

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runDocParser(t *testing.T, workspace, argsJSON string) ToolResult {
	t.Helper()
	if root := projectRoot(); root != "" {
		t.Setenv("AGENT_REPO_ROOT", root)
	}
	tool := NewLocalDocumentParserTool(ToolDef{}, &Config{})
	result, err := tool.Execute(context.Background(), ToolInvocation{
		CallID:    "test-call",
		Name:      "document_parser",
		Args:      json.RawMessage(argsJSON),
		Workspace: workspace,
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	return result
}

// projectRoot walks up from the package directory looking for the repository
// root (identified by .tools/venv), matching the resolver used by the tool.
func projectRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for depth := 0; depth < 8; depth++ {
		if info, statErr := os.Stat(filepath.Join(dir, ".tools", "venv")); statErr == nil && info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func projectVenvPython() string {
	root := projectRoot()
	if root == "" {
		return ""
	}
	for _, rel := range []string{
		filepath.Join(".tools", "venv", "Scripts", "python.exe"),
		filepath.Join(".tools", "venv", "bin", "python"),
	} {
		candidate := filepath.Join(root, rel)
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func hostPythonAvailable(t *testing.T) string {
	t.Helper()
	candidates := make([]string, 0, 3)
	if venv := projectVenvPython(); venv != "" {
		candidates = append(candidates, venv)
	}
	if found, err := exec.LookPath("python3"); err == nil {
		candidates = append(candidates, found)
	}
	if found, err := exec.LookPath("python"); err == nil {
		candidates = append(candidates, found)
	}
	for _, interpreter := range candidates {
		check := exec.Command(interpreter, "-c", "import docx, pdfminer")
		if err := check.Run(); err == nil {
			return interpreter
		}
	}
	t.Skip("python-docx / pdfminer.six not installed; skipping python-backed document_parser tests")
	return ""
}

func TestLocalDocumentParserTextFile(t *testing.T) {
	dir := t.TempDir()
	text := "# \u8bbf\u8c08\u7eaa\u8981\n\n\u5171 6 \u4f4d\u53d7\u8bbf\u8005\u3002"
	if err := os.WriteFile(filepath.Join(dir, "note.md"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	result := runDocParser(t, dir, `{"file_path": "/mnt/data/note.md"}`)
	if result.IsError {
		t.Fatalf("unexpected error result: %v", result.Value)
	}
	content, _ := result.Value["content"].(string)
	if !strings.Contains(content, "6 \u4f4d\u53d7\u8bbf\u8005") {
		t.Fatalf("content missing text: %q", content)
	}
}

func TestLocalDocumentParserDocx(t *testing.T) {
	interpreter := hostPythonAvailable(t)
	dir := t.TempDir()
	docxPath := filepath.Join(dir, "\u8bbf\u8c08\u8bb0\u5f55.docx")
	generator := `import sys
from docx import Document
doc = Document()
doc.add_heading("\u8bbf\u8c08\u80cc\u666f", level=1)
doc.add_paragraph("\u672c\u6b21\u8bbf\u8c08\u56f4\u7ed5\u667a\u80fd\u95e8\u9501\u4ea7\u54c1\u5c55\u5f00\uff0c\u51716\u4f4d\u53d7\u8bbf\u8005\u3002")
table = doc.add_table(rows=2, cols=2)
table.cell(0, 0).text = "\u7ef4\u5ea6"
table.cell(0, 1).text = "\u7ed3\u8bba"
table.cell(1, 0).text = "\u8d2d\u4e70\u51b3\u7b56"
table.cell(1, 1).text = "\u5b89\u5168\u4e0e\u4fbf\u6377\u5e76\u91cd"
doc.save(sys.argv[1])`
	if out, err := exec.Command(interpreter, "-c", generator, docxPath).CombinedOutput(); err != nil {
		t.Fatalf("generate docx fixture: %v: %s", err, out)
	}
	result := runDocParser(t, dir, `{"file_path": "/mnt/data/\u8bbf\u8c08\u8bb0\u5f55.docx"}`)
	if result.IsError {
		t.Fatalf("unexpected error result: %v", result.Value)
	}
	content, _ := result.Value["content"].(string)
	for _, want := range []string{"\u8bbf\u8c08\u80cc\u666f", "6\u4f4d\u53d7\u8bbf\u8005", "| \u7ef4\u5ea6 | \u7ed3\u8bba |", "\u5b89\u5168\u4e0e\u4fbf\u6377\u5e76\u91cd"} {
		if !strings.Contains(content, want) {
			t.Fatalf("docx content missing %q; got: %q", want, content)
		}
	}
}

func TestLocalDocumentParserUnsupportedType(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "legacy.doc"), []byte("binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := runDocParser(t, dir, `{"file_path": "/mnt/data/legacy.doc"}`)
	if !result.IsError {
		t.Fatalf("expected error result for .doc, got: %v", result.Value)
	}
	if message, _ := result.Value["error"].(string); !strings.Contains(message, "unsupported file type") {
		t.Fatalf("unexpected error message: %q", message)
	}
}

func TestLocalDocumentParserMissingFile(t *testing.T) {
	dir := t.TempDir()
	result := runDocParser(t, dir, `{"file_path": "/mnt/data/missing.pdf"}`)
	if !result.IsError {
		t.Fatalf("expected error result for missing file")
	}
	if message, _ := result.Value["error"].(string); !strings.Contains(message, "file not found") {
		t.Fatalf("unexpected error message: %q", message)
	}
}

func TestTruncateDocParserContentMiddle(t *testing.T) {
	long := strings.Repeat("A", 100) + strings.Repeat("B", 100) + strings.Repeat("C", 100)
	got := truncateDocParserContent(long, 30, "middle") // 30*3 = 90 runes < 300
	if len([]rune(got)) >= 300 {
		t.Fatalf("content was not truncated: %d runes", len([]rune(got)))
	}
	if !strings.Contains(got, "[Middle part truncated]") {
		t.Fatalf("missing truncation notice: %q", got)
	}
	if !strings.HasPrefix(got, "AAAA") || !strings.HasSuffix(got, "CCCC") {
		t.Fatalf("middle truncation should keep head and tail: %q", got)
	}
	if short := truncateDocParserContent("short", 30, "middle"); short != "short" {
		t.Fatalf("short content must pass through: %q", short)
	}
}
