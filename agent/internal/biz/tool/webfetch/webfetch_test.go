package webfetch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"agentloop/agent/internal/dtypes"
)

func TestWebFetchReadsPlainText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("hello world\n"))
	}))
	defer server.Close()

	tool := NewTool(dtypes.ToolDef{})
	result, _ := tool.Execute(context.Background(), invocationFor(t, `{"url":"`+server.URL+`"}`))
	if result.IsError {
		t.Fatalf("unexpected error: %#v", result.Value)
	}
	if got := result.Value["text"]; got != "hello world\n" {
		t.Fatalf("unexpected body: %#v", got)
	}
}

func TestWebFetchStripsHTML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body><h1>Title</h1><p>Hello <b>world</b>.</p><script>alert(1)</script></body></html>`))
	}))
	defer server.Close()

	tool := NewTool(dtypes.ToolDef{})
	result, _ := tool.Execute(context.Background(), invocationFor(t, `{"url":"`+server.URL+`"}`))
	if result.IsError {
		t.Fatalf("unexpected error: %#v", result.Value)
	}
	text, _ := result.Value["text"].(string)
	if !strings.Contains(text, "Title") || !strings.Contains(text, "Hello world.") {
		t.Fatalf("expected prose in output, got %q", text)
	}
	if strings.Contains(text, "alert(1)") {
		t.Fatalf("script body should be stripped, got %q", text)
	}
}

func TestWebFetchRejectsNonHTTPURL(t *testing.T) {
	tool := NewTool(dtypes.ToolDef{})
	result, _ := tool.Execute(context.Background(), invocationFor(t, `{"url":"file:///etc/passwd"}`))
	if !result.IsError {
		t.Fatalf("expected error for non-http url, got %#v", result.Value)
	}
}

func TestWebFetchReportsErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer server.Close()
	tool := NewTool(dtypes.ToolDef{})
	result, _ := tool.Execute(context.Background(), invocationFor(t, `{"url":"`+server.URL+`"}`))
	if !result.IsError {
		t.Fatalf("expected error for 500, got %#v", result.Value)
	}
}

func TestNamedWebFetchReportsConfiguredName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("named-ok"))
	}))
	defer server.Close()

	tool := NewToolWithName(dtypes.ToolDef{}, "web_fetch")
	if tool.Name() != "web_fetch" {
		t.Fatalf("unexpected tool name: %q", tool.Name())
	}
	result, _ := tool.Execute(context.Background(), invocationFor(t, `{"url":"`+server.URL+`"}`))
	if result.IsError || result.Value["tool"] != "web_fetch" || result.Value["text"] != "named-ok" {
		t.Fatalf("unexpected named result: %#v", result.Value)
	}
}

func TestWebFetchStatelessReturnsRawBytes(t *testing.T) {
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl6nL8AAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	}))
	defer server.Close()

	tool := NewTool(dtypes.ToolDef{})
	result, _ := tool.Execute(context.Background(), invocationFor(t, `{"url":"`+server.URL+`/picture.png","file_mode":"stateless"}`))
	if result.IsError {
		t.Fatalf("unexpected error: %#v", result.Value)
	}
	if result.Value["file_mode"] != "stateless" || result.Value["file_name"] != "picture.png" {
		t.Fatalf("unexpected stateless metadata: %#v", result.Value)
	}
	encoded, _ := result.Value["file_base64"].(string)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("file_base64 is not valid base64: %v", err)
	}
	if string(decoded) != string(png) {
		t.Fatalf("stateless payload did not round-trip")
	}
	if _, ok := result.Value["text"]; ok {
		t.Fatalf("stateless mode should not render text: %#v", result.Value)
	}
}

func TestWebFetchRejectsUnknownFileMode(t *testing.T) {
	tool := NewTool(dtypes.ToolDef{})
	result, _ := tool.Execute(context.Background(), invocationFor(t, `{"url":"https://example.com","file_mode":"stream"}`))
	if !result.IsError {
		t.Fatalf("expected error for unknown file_mode, got %#v", result.Value)
	}
}

func TestWebFetchStatelessRejectsOversizeBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(make([]byte, 4096))
	}))
	defer server.Close()

	opts := DefaultOptions()
	opts.MaxResponseBytes = 128
	tool := NewToolWithOptions(dtypes.ToolDef{}, opts)
	result, _ := tool.Execute(context.Background(), invocationFor(t, `{"url":"`+server.URL+`/big.png","file_mode":"stateless"}`))
	if !result.IsError {
		t.Fatalf("expected oversize stateless body to fail, got %#v", result.Value)
	}
}

func TestWebFetchHonorsDynamicBudgetAndReportsTruncation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<p>" + strings.Repeat("中", 2000) + "</p>"))
	}))
	defer server.Close()

	invocation := invocationFor(t, `{"url":"`+server.URL+`"}`)
	invocation.Metadata = map[string]string{"web_budget_tokens": "100"}
	result, _ := NewTool(dtypes.ToolDef{}).Execute(context.Background(), invocation)
	if result.IsError {
		t.Fatalf("unexpected error: %#v", result.Value)
	}
	if result.Value["truncated"] != true {
		t.Fatalf("expected truncation, got %#v", result.Value)
	}
	text, _ := result.Value["text"].(string)
	if !utf8.ValidString(text) {
		t.Fatal("truncated text is not valid UTF-8")
	}
}

func TestWebFetchSummarizesLongBody(t *testing.T) {
	// 200 distinct sentences stay well above the summarization threshold. Every
	// fifth sentence repeats the phrase "结果缓存" so the extractive scorer has a
	// recurring signal to surface; the rest are unique filler.
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString("<p>")
		if i%5 == 0 {
			b.WriteString("本节说明结果缓存策略在高并发场景下的取舍与失效处理。")
		} else {
			b.WriteString(fmt.Sprintf("第 %d 条观测记录描述了进程在压力下的行为与资源占用。", i))
		}
		b.WriteString("</p>")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(b.String()))
	}))
	defer server.Close()

	opts := DefaultOptions()
	opts.MaxBodyChars = 100000
	tool := NewToolWithOptions(dtypes.ToolDef{}, opts)
	result, _ := tool.Execute(context.Background(), invocationFor(t, `{"url":"`+server.URL+`"}`))
	if result.IsError {
		t.Fatalf("unexpected error: %#v", result.Value)
	}
	if result.Value["summarized"] != true {
		t.Fatalf("expected long body to be summarized: %#v", result.Value)
	}
	text, _ := result.Value["text"].(string)
	if !utf8.ValidString(text) {
		t.Fatal("summary is not valid UTF-8")
	}
	if !strings.Contains(text, "结果缓存") {
		t.Fatalf("summary dropped the high-signal sentence: %q", text)
	}
	if len([]rune(text)) >= 4000 {
		t.Fatalf("summary was not condensed: %d runes", len([]rune(text)))
	}
}

// invocationFor builds a ToolInvocation for tests. The ToolFactory is not
// involved — Execute only needs CallID/Args populated.
func invocationFor(t *testing.T, args string) dtypes.ToolInvocation {
	t.Helper()
	return dtypes.ToolInvocation{
		CallID: "test",
		Name:   "web_fetch",
		Args:   []byte(args),
	}
}

// TestWebFetchPreservesJSON verifies that JSON responses above the summarizer
// threshold are returned verbatim (just truncated by max_chars). The
// summarizer must NEVER paraphrase structured content, or downstream callers
// will see invalid JSON.
func TestWebFetchPreservesJSON(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"items":[`)
	for i := 0; i < 200; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":%d,"name":"item-%d","tags":["alpha","beta","gamma"]}`, i, i)
	}
	b.WriteString(`]}`)
	raw := b.String()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(raw))
	}))
	defer server.Close()

	opts := DefaultOptions()
	opts.MaxBodyChars = 100000
	tool := NewToolWithOptions(dtypes.ToolDef{}, opts)
	result, _ := tool.Execute(context.Background(), invocationFor(t, `{"url":"`+server.URL+`"}`))
	if result.IsError {
		t.Fatalf("unexpected error: %#v", result.Value)
	}
	if result.Value["summarized"] == true {
		t.Fatal("summarizer must not paraphrase JSON")
	}
	text, _ := result.Value["text"].(string)
	if !strings.Contains(text, `"id":0`) || !strings.Contains(text, `"id":199`) {
		t.Fatalf("JSON payload was damaged: %q...", text[:min(200, len(text))])
	}
	if !json.Valid([]byte(text)) {
		t.Fatal("response is no longer valid JSON")
	}
}

// TestWebFetchPreservesSourceCode confirms Go source responses stay intact.
// A naive extractive summary would slice out braces and operators and break
// the file's parseability.
func TestWebFetchPreservesSourceCode(t *testing.T) {
	var b strings.Builder
	b.WriteString("package pkg\n\n")
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&b, "func f%d(x int) int { return x + %d }\n", i, i)
	}
	b.WriteString("// trailing comment line that stays well above the threshold to trigger the summarizer if it were ever wrong about content-type\n")
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "// note %d: the implementation is intentionally repetitive to push the rune count over 4000\n", i)
	}
	b.WriteString("}\n")
	raw := b.String()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/x-go; charset=utf-8")
		_, _ = w.Write([]byte(raw))
	}))
	defer server.Close()

	opts := DefaultOptions()
	opts.MaxBodyChars = 100000
	tool := NewToolWithOptions(dtypes.ToolDef{}, opts)
	result, _ := tool.Execute(context.Background(), invocationFor(t, `{"url":"`+server.URL+`"}`))
	if result.Value["summarized"] == true {
		t.Fatal("summarizer must not rewrite source code")
	}
	text, _ := result.Value["text"].(string)
	if !strings.Contains(text, "package pkg") || !strings.Contains(text, "func f0") {
		t.Fatalf("source code was damaged: %q...", text[:min(200, len(text))])
	}
}

// TestWebFetchFocusPromotesAnswerSentence shows that when the caller is
// hunting for a specific fact, the summarizer must surface that exact
// sentence instead of generic topic sentences.
func TestWebFetchFocusPromotesAnswerSentence(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString("<p>")
		fmt.Fprintf(&b, "第 %d 段是背景描述与节奏与调度的一般说明。", i)
		b.WriteString("</p>")
	}
	b.WriteString("<p>事件首发时间是 2024-03-15 14:30 UTC。</p>")
	for i := 0; i < 80; i++ {
		b.WriteString("<p>")
		fmt.Fprintf(&b, "补充段落 %d 继续叙述相关背景。", i)
		b.WriteString("</p>")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(b.String()))
	}))
	defer server.Close()

	opts := DefaultOptions()
	opts.MaxBodyChars = 100000
	tool := NewToolWithOptions(dtypes.ToolDef{}, opts)
	args := fmt.Sprintf(`{"url":"%s","focus":"首发时间"}`, server.URL)
	result, _ := tool.Execute(context.Background(), invocationFor(t, args))
	if result.Value["summarized"] != true {
		t.Fatalf("expected summarization: %#v", result.Value)
	}
	text, _ := result.Value["text"].(string)
	if !strings.Contains(text, "2024-03-15") {
		t.Fatalf("focus did not surface the answer sentence: %q", text)
	}
}

// TestSummarizeWithFocusEmptyFocusBehavesLikeGeneric checks that passing an
// empty focus keyword falls back to the generic relevance ranking rather
// than returning an empty string.
func TestSummarizeWithFocusEmptyFocusBehavesLikeGeneric(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "第 %d 段持续记录观测数据并说明系统负载情况。", i)
	}
	out := summarizeWithFocus(b.String(), "   ", summaryTargetRunes)
	if out == "" {
		t.Fatal("expected non-empty summary with empty focus")
	}
	if !strings.Contains(out, "观测数据") {
		t.Fatalf("expected baseline ranking output, got %q", out)
	}
}

// TestLooksStructuredRejectsCodeLikeBody asserts the fallback heuristic
// catches diff/log/code-shaped responses that don't carry a JSON MIME.
func TestLooksStructuredRejectsCodeLikeBody(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&b, "func h%d(x int) int { return x } // filler line\n", i)
	}
	if !looksStructured(b.String()) {
		t.Fatal("looksStructured should flag repetitive code-shaped bodies")
	}
	prose := strings.Repeat("This is a regular English sentence about something. ", 40)
	if looksStructured(prose) {
		t.Fatal("looksStructured should not flag ordinary prose")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
