package local

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// localDocumentParserTool parses documents entirely on the local host.
//
// The HTTP backend routes document_parser to the unified tool gateway, which
// expects files to live in the production session sandbox. Local runs have no
// such kernel, so every parse failed with "no active kernel found". This tool
// resolves the same virtual paths against the local workspace and extracts
// text with the configured Python interpreter (python-docx for .docx,
// pdfminer.six for .pdf, stdlib zipfile for .pptx/.xlsx), keeping the server
// tool's {"content": ...} result contract.
const (
	docParserMaxBytes      = 20 << 20 // 20 MB, aligns with server-side size guards
	docParserTimeoutSec    = 120
	docParserDefaultTokens = 4000 // mirrors config documentParser.outputMaxTokens
	docParserBytesPerToken = 3    // mirrors the server-side token estimate
)

// textLikeExtensions are returned verbatim (validated as UTF-8) without a
// round-trip through python.
var textLikeExtensions = map[string]bool{
	".txt": true, ".md": true, ".markdown": true, ".csv": true, ".tsv": true,
	".json": true, ".log": true, ".html": true, ".htm": true, ".xml": true,
	".yml": true, ".yaml": true,
}

type localDocumentParserTool struct {
	schema   ToolDef
	cfg      *Config
	resolver localExecutableResolver
	runner   localProcessRunner
}

// NewLocalDocumentParserTool returns the local document_parser implementation.
func NewLocalDocumentParserTool(schema ToolDef, cfg *Config) Tool {
	return &localDocumentParserTool{
		schema:   schema,
		cfg:      cfg,
		resolver: newLocalExecutableResolver(),
		runner:   osLocalProcessRunner{},
	}
}

func (t *localDocumentParserTool) Name() string    { return "document_parser" }
func (t *localDocumentParserTool) Schema() ToolDef { return t.schema }

func (t *localDocumentParserTool) Execute(ctx context.Context, invocation ToolInvocation) (ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	args, err := decodeLocalToolArgs(invocation)
	if err != nil {
		return localErrorResult(t.Name(), err), nil
	}
	requested := localStringArg(args, "file_path")
	if strings.TrimSpace(requested) == "" {
		return localErrorResult(t.Name(), fmt.Errorf("param file_path empty")), nil
	}
	localContext, err := localToolContext(invocation)
	if err != nil {
		return localErrorResult(t.Name(), err), nil
	}
	path, err := resolveLocalWorkspacePath(localContext.Workspace, requested)
	if err != nil {
		return localErrorResult(t.Name(), err), nil
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		return localErrorResult(t.Name(), fmt.Errorf("file not found: %s", requested)), nil
	}
	if info.IsDir() {
		return localErrorResult(t.Name(), fmt.Errorf("path is a directory, not a file: %s", requested)), nil
	}
	if info.Size() > docParserMaxBytes {
		return localErrorResult(t.Name(), fmt.Errorf("file too large: %s (%d bytes, max %d bytes)", requested, info.Size(), docParserMaxBytes)), nil
	}

	ext := strings.ToLower(filepath.Ext(path))
	var content string
	switch {
	case textLikeExtensions[ext]:
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return localErrorResult(t.Name(), readErr), nil
		}
		if !utf8.Valid(raw) {
			return localErrorResult(t.Name(), fmt.Errorf("file is not valid UTF-8 text: %s", requested)), nil
		}
		content = string(raw)
	case ext == ".docx":
		content, err = t.runPython(ctx, docParserDocxScript, path, localContext.Workspace)
	case ext == ".pdf":
		content, err = t.runPython(ctx, docParserPDFScript, path, localContext.Workspace)
	case ext == ".pptx":
		content, err = t.runPython(ctx, docParserPptxScript, path, localContext.Workspace)
	case ext == ".xlsx":
		content, err = t.runPython(ctx, docParserXlsxScript, path, localContext.Workspace)
	default:
		return localErrorResult(t.Name(), fmt.Errorf("unsupported file type %q for local document_parser (%s); supported: txt/md/csv/tsv/json/html/xml/yaml, docx, pdf, pptx, xlsx", ext, requested)), nil
	}
	if err != nil {
		return localErrorResult(t.Name(), err), nil
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return localErrorResult(t.Name(), fmt.Errorf("no extractable text in %s", requested)), nil
	}
	content = truncateDocParserContent(content, t.outputMaxTokens(), t.outputTruncateStrategy())
	return ToolResult{Value: map[string]any{
		"tool":    t.Name(),
		"ok":      true,
		"path":    requested,
		"bytes":   info.Size(),
		"content": content,
	}}, nil
}

// runPython executes an extraction script with the target file path as argv[1].
func (t *localDocumentParserTool) runPython(ctx context.Context, script, path, workspace string) (string, error) {
	python, err := t.resolver.resolvePython(toolRuntimeExecutables(t.cfg).Python)
	if err != nil {
		return "", fmt.Errorf("python interpreter is not available (%w); document parsing needs the project venv with python-docx and pdfminer.six installed", err)
	}
	tmp, err := os.CreateTemp("", "fairy-doc-parser-*.py")
	if err != nil {
		return "", fmt.Errorf("write extraction script: %w", err)
	}
	scriptPath := tmp.Name()
	defer os.Remove(scriptPath)
	if _, err := tmp.WriteString(script); err != nil {
		tmp.Close()
		return "", fmt.Errorf("write extraction script: %w", err)
	}
	tmp.Close()

	processArgs := append([]string{}, python.PrefixArgs...)
	processArgs = append(processArgs, scriptPath, path)
	result, runErr := t.runner.Run(ctx, localProcessRequest{
		Path:    python.Path,
		Args:    processArgs,
		Dir:     workspace,
		Env:     []string{"PYTHONIOENCODING=utf-8"},
		Timeout: docParserTimeoutSec * time.Second,
	})
	if runErr != nil {
		return "", fmt.Errorf("document parsing failed: %w", runErr)
	}
	if result.TimedOut {
		return "", fmt.Errorf("document parsing timed out after %ds", docParserTimeoutSec)
	}
	if result.ExitCode != 0 {
		message := strings.TrimSpace(result.Stderr)
		if message == "" {
			message = fmt.Sprintf("exit code %d", result.ExitCode)
		}
		if strings.Contains(message, "No module named") || strings.Contains(message, "not installed") {
			return "", fmt.Errorf("python parser missing dependency: %s", message)
		}
		return "", fmt.Errorf("python parser failed: %s", message)
	}
	return result.Stdout, nil
}

func (t *localDocumentParserTool) outputMaxTokens() int {
	if t != nil && t.cfg != nil && t.cfg.DocumentParser.OutputMaxTokens > 0 {
		return t.cfg.DocumentParser.OutputMaxTokens
	}
	return docParserDefaultTokens
}

func (t *localDocumentParserTool) outputTruncateStrategy() string {
	if t != nil && t.cfg != nil {
		if strategy := strings.TrimSpace(t.cfg.DocumentParser.OutputTruncateStrategy); strategy != "" {
			return strategy
		}
	}
	return "middle"
}

// truncateDocParserContent mirrors the server-side truncation contract:
// ~3 bytes per token; "middle" keeps head+tail, otherwise head only.
func truncateDocParserContent(content string, maxTokens int, strategy string) string {
	runes := []rune(content)
	maxRunes := maxTokens * docParserBytesPerToken
	if maxTokens <= 0 || len(runes) <= maxRunes {
		return content
	}
	if strategy == "middle" {
		half := maxRunes / 2
		if half <= 0 {
			return content
		}
		return string(runes[:half]) + "\n\n[Middle part truncated]\n\n" + string(runes[len(runes)-half:])
	}
	return string(runes[:maxRunes]) + "\n\n[Content truncated]"
}

const docParserDocxScript = `
import sys
try:
    from docx import Document
except ImportError:
    sys.exit("python-docx is not installed on the host (pip install python-docx)")
from docx.oxml.ns import qn
from docx.table import Table
from docx.text.paragraph import Paragraph

def iter_blocks(document):
    for child in document.element.body.iterchildren():
        if child.tag == qn("w:p"):
            yield Paragraph(child, document)
        elif child.tag == qn("w:tbl"):
            yield Table(child, document)

lines = []
for block in iter_blocks(Document(sys.argv[1])):
    if isinstance(block, Paragraph):
        text = block.text.strip()
        if not text:
            continue
        style = ""
        try:
            style = (block.style.name or "").lower()
        except Exception:
            style = ""
        if style.startswith("heading"):
            parts = style.split()
            level = 2
            if parts and parts[-1].isdigit():
                level = min(int(parts[-1]), 6)
            lines.append("#" * level + " " + text)
        else:
            lines.append(text)
    else:
        for row in block.rows:
            cells = [cell.text.strip().replace("\n", " ") for cell in row.cells]
            lines.append("| " + " | ".join(cells) + " |")
        lines.append("")
print("\n".join(lines).strip())
`

const docParserPDFScript = `
import sys
try:
    from pdfminer.high_level import extract_text
except ImportError:
    sys.exit("pdfminer.six is not installed on the host (pip install pdfminer.six)")
text = extract_text(sys.argv[1]) or ""
print(text.strip())
`

const docParserPptxScript = `
import re
import sys
import zipfile
import xml.etree.ElementTree as ET

TEXT_TAG = "{http://schemas.openxmlformats.org/drawingml/2006/main}t"
with zipfile.ZipFile(sys.argv[1]) as archive:
    names = [n for n in archive.namelist() if re.match(r"ppt/slides/slide\d+\.xml$", n)]
    names.sort(key=lambda n: int(re.search(r"(\d+)", n).group(1)))
    sections = []
    for index, name in enumerate(names, 1):
        root = ET.fromstring(archive.read(name))
        texts = [node.text.strip() for node in root.iter(TEXT_TAG) if node.text and node.text.strip()]
        if texts:
            sections.append("## Slide %d\n%s" % (index, "\n".join(texts)))
print("\n\n".join(sections))
`

const docParserXlsxScript = `
import re
import sys
import zipfile
import xml.etree.ElementTree as ET

MAIN = "{http://schemas.openxmlformats.org/spreadsheetml/2006/main}"
with zipfile.ZipFile(sys.argv[1]) as archive:
    shared = []
    if "xl/sharedStrings.xml" in archive.namelist():
        root = ET.fromstring(archive.read("xl/sharedStrings.xml"))
        for item in root.iter(MAIN + "si"):
            shared.append("".join(t.text or "" for t in item.iter(MAIN + "t")))
    sheets = sorted(n for n in archive.namelist() if re.match(r"xl/worksheets/sheet\d+\.xml$", n))
    for index, name in enumerate(sheets, 1):
        print("## Sheet %d" % index)
        root = ET.fromstring(archive.read(name))
        for row in root.iter(MAIN + "row"):
            cells = []
            for cell in row.iter(MAIN + "c"):
                value = ""
                v = cell.find(MAIN + "v")
                if cell.get("t") == "s" and v is not None and v.text:
                    idx = int(v.text)
                    value = shared[idx] if 0 <= idx < len(shared) else ""
                elif v is not None and v.text:
                    value = v.text
                cells.append(value.replace("\n", " ").strip())
            if any(cells):
                print("| " + " | ".join(cells) + " |")
        print("")
`
