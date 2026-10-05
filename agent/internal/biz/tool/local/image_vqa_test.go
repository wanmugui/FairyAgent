package local

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalImageVQAAnalyzesLocalImageWithDeepSeekVision(t *testing.T) {
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl6nL8AAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	imagePath := filepath.Join(workspace, "sample.png")
	if err := os.WriteFile(imagePath, png, 0o600); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-deepseek-key" {
			t.Fatalf("unexpected authorization: %q", got)
		}
		var request imageVQARequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "deepseek-flash" || request.Stream || request.MaxTokens != 2048 {
			t.Fatalf("unexpected request: %#v", request)
		}
		if len(request.Messages) != 2 || len(request.Messages[1].Content) != 2 {
			t.Fatalf("unexpected messages: %#v", request.Messages)
		}
		imageURL := request.Messages[1].Content[1].ImageURL
		if imageURL == nil || !strings.HasPrefix(imageURL.URL, "data:image/png;base64,") {
			t.Fatalf("unexpected image content: %#v", request.Messages[1].Content[1])
		}
		if !strings.Contains(request.Messages[1].Content[0].Text, "描述图片") {
			t.Fatalf("query was not forwarded: %#v", request.Messages[1].Content[0])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"图片主体是一只白色眼睛。"},"finish_reason":"stop"}],"usage":{"prompt_tokens":120,"completion_tokens":20,"total_tokens":140}}`))
	}))
	defer server.Close()

	tool := NewLocalImageVQATool(ToolDef{}, &Config{ImageVQA: ImageVQAConfig{
		ModelName:   "deepseek-flash",
		BaseURL:     server.URL,
		APIKey:      "test-deepseek-key",
		TimeoutSec:  5,
		MaxTokens:   2048,
		Temperature: 0.1,
		MaxRetries:  0,
	}})
	result, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: workspace,
		Args:      json.RawMessage(`{"image_path":"sample.png","query":"请描述图片主体"}`),
		Timeout:   5 * time.Second,
	})
	if err != nil || result.IsError {
		t.Fatalf("image_vqa failed: result=%#v err=%v", result, err)
	}
	if result.Value["answer"] != "图片主体是一只白色眼睛。" || result.Value["model"] != "deepseek-flash" {
		t.Fatalf("unexpected result: %#v", result.Value)
	}
}

func TestLocalImageVQARejectsMissingImage(t *testing.T) {
	tool := NewLocalImageVQATool(ToolDef{}, &Config{ImageVQA: ImageVQAConfig{BaseURL: "http://127.0.0.1:1"}})
	result, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: t.TempDir(),
		Args:      json.RawMessage(`{"image_path":"missing.png","query":"describe"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Value["error"].(string), "image not found") {
		t.Fatalf("expected missing image error, got %#v", result)
	}
}

func TestParseImageVQAResponseRejectsLengthOnlyReasoning(t *testing.T) {
	_, _, err := parseImageVQAResponse([]byte(`{"choices":[{"message":{"content":"","reasoning_content":"still reasoning"},"finish_reason":"length"}]}`))
	if err == nil || !strings.Contains(err.Error(), "max_tokens") {
		t.Fatalf("expected max_tokens error, got %v", err)
	}
}

func TestNormalizeImageVQAMode(t *testing.T) {
	cases := map[string]string{
		"":           imageVQAModeAuto,
		"auto":       imageVQAModeAuto,
		"AUTO":       imageVQAModeAuto,
		" local ":    imageVQAModeLocal,
		"local_ocr":  imageVQAModeLocal,
		"ocr":        imageVQAModeLocal,
		"remote":     imageVQAModeRemote,
		"remote_vlm": imageVQAModeRemote,
		"vlm":        imageVQAModeRemote,
		"nonsense":   "",
	}
	for input, want := range cases {
		if got := normalizeImageVQAMode(input); got != want {
			t.Fatalf("normalizeImageVQAMode(%q) = %q, want %q", input, got, want)
		}
	}
}

func visionTestElements() []visionElement {
	bounds := func(x, y, w, h int) map[string]any {
		return map[string]any{"x": float64(x), "y": float64(y), "width": float64(w), "height": float64(h)}
	}
	return []visionElement{
		{Text: "保存", Source: "ocr", Bounds: bounds(100, 200, 60, 30), Score: 0.9},
		{Text: "保存全部", Source: "ocr", Bounds: bounds(100, 240, 120, 30), Score: 0.9},
		{Text: "关闭", Source: "ocr", Bounds: bounds(300, 200, 60, 30), Score: 0.9},
		{Text: "file", Source: "ocr", Bounds: bounds(10, 10, 40, 20), Score: 0.8},
	}
}

// The matcher must find a label the question literally refers to, and must
// prefer the most specific one.
func TestVisionMatchQueryPrefersLiteralLabels(t *testing.T) {
	elements := visionTestElements()

	matches := visionMatchQuery("帮我点一下保存全部这个按钮", elements)
	if len(matches) == 0 {
		t.Fatal("expected a match for 保存全部")
	}
	if matches[0].Text != "保存全部" {
		t.Fatalf("expected the longest label to win, got %q", matches[0].Text)
	}

	matches = visionMatchQuery("图里有没有 Close 按钮", elements)
	if len(matches) == 0 || matches[0].Text != "file" {
		// "Close" is absent; only the question-inside-label direction can match,
		// and it must not fire here.
		for _, match := range matches {
			if match.Text != "file" {
				t.Fatalf("unexpected match %q", match.Text)
			}
		}
	}
}

// A question about meaning must not be answered from a coincidental text match.
func TestVisionMatchQueryRefusesMeaningQuestions(t *testing.T) {
	elements := visionTestElements()
	for _, query := range []string{
		"这个人看起来开心吗",
		"这张照片的氛围怎么样",
		"describe the mood of the scene",
	} {
		if matches := visionMatchQuery(query, elements); len(matches) != 0 {
			t.Fatalf("query %q should not match any element, got %d", query, len(matches))
		}
	}
}

func TestVisionIntentDetection(t *testing.T) {
	for _, query := range []string{"图里写了什么", "提取文字", "what text is there", "list the text"} {
		if !visionListIntent(query) {
			t.Fatalf("%q should be a list-intent question", query)
		}
	}
	if visionListIntent("开始游戏在哪") {
		t.Fatal("a location question is not a list question")
	}
	for _, query := range []string{"有几个按钮", "how many items", "数量是多少"} {
		if !visionCountIntent(query) {
			t.Fatalf("%q should be a count-intent question", query)
		}
	}
}

func TestVisionLocalCapableQuery(t *testing.T) {
	for _, query := range []string{
		"图里写了什么文字",
		"保存按钮在哪",
		"有没有开始游戏按钮",
		"where is the save button",
	} {
		if !visionLocalCapableQuery(query) {
			t.Fatalf("%q should be local-capable", query)
		}
	}
	for _, query := range []string{
		"这张图什么色调",
		"这个人看起来开心吗",
		"把字体规格和色板颜色值完整读出来",
		"解释一下画面里的因果关系",
		"图里有没有猫",
		"有多少人在笑",
		"describe the mood of the scene",
	} {
		if visionLocalCapableQuery(query) {
			t.Fatalf("%q must go to the multimodal model", query)
		}
	}
}

func stubLocalVisionAnswer(t *testing.T, fn func(context.Context, string, string, string) (visionLocalAnswer, error)) {
	t.Helper()
	original := answerImageQuestionLocallyFunc
	answerImageQuestionLocallyFunc = fn
	t.Cleanup(func() { answerImageQuestionLocallyFunc = original })
}

func writeTestPNG(t *testing.T, workspace string) {
	t.Helper()
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl6nL8AAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "sample.png"), png, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The whole point of the change: a text question is answered on device and the
// multimodal API is never called.
func TestImageVQAPrefersLocalAnswerOverRemote(t *testing.T) {
	remoteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteCalls++
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"remote"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	stubLocalVisionAnswer(t, func(_ context.Context, _, imagePath, query string) (visionLocalAnswer, error) {
		if !strings.HasSuffix(imagePath, "sample.png") {
			t.Fatalf("local answerer received %q", imagePath)
		}
		if query != "保存按钮在哪" {
			t.Fatalf("query was not forwarded to the local answerer: %q", query)
		}
		return visionLocalAnswer{
			Answered: true,
			Answer:   "图中存在「保存」，中心坐标 (130, 215)，区域 60×30。",
			Engine:   "local_ocr",
			Texts:    4,
			Matches:  []map[string]any{{"text": "保存"}},
			Timing:   map[string]any{"total_ms": float64(42)},
		}, nil
	})

	workspace := t.TempDir()
	writeTestPNG(t, workspace)
	tool := NewLocalImageVQATool(ToolDef{}, &Config{
		RepoRoot: workspace,
		ImageVQA: ImageVQAConfig{BaseURL: server.URL, APIKey: "k", Mode: imageVQAModeAuto, MaxRetries: 0},
	})
	result, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: workspace,
		Args:      json.RawMessage(`{"image_path":"sample.png","query":"保存按钮在哪"}`),
	})
	if err != nil || result.IsError {
		t.Fatalf("image_vqa failed: result=%#v err=%v", result, err)
	}
	if remoteCalls != 0 {
		t.Fatalf("the multimodal API must not be called when the local pipeline answers, got %d calls", remoteCalls)
	}
	if result.Value["engine"] != "local_ocr" {
		t.Fatalf("engine = %v, want local_ocr", result.Value["engine"])
	}
	if !strings.Contains(result.Value["answer"].(string), "保存") {
		t.Fatalf("unexpected answer: %#v", result.Value["answer"])
	}
	if result.Value["elements_seen"] != 4 {
		t.Fatalf("elements_seen = %v, want 4", result.Value["elements_seen"])
	}
}

// A question the local pipeline cannot answer still reaches the model, so
// behaviour is never worse than before for genuinely visual questions.
func TestImageVQAFallsBackToRemoteWhenLocalDeclines(t *testing.T) {
	remoteCalls := 0
	localCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteCalls++
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"画面整体偏冷色调。"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	stubLocalVisionAnswer(t, func(context.Context, string, string, string) (visionLocalAnswer, error) {
		localCalls++
		return visionLocalAnswer{Answered: false, Engine: "local_ocr", Reason: "no_text_match"}, nil
	})

	workspace := t.TempDir()
	writeTestPNG(t, workspace)
	tool := NewLocalImageVQATool(ToolDef{}, &Config{
		RepoRoot: workspace,
		ImageVQA: ImageVQAConfig{BaseURL: server.URL, APIKey: "k", ModelName: "deepseek-flash", Mode: imageVQAModeAuto, MaxRetries: 0},
	})
	result, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: workspace,
		Args:      json.RawMessage(`{"image_path":"sample.png","query":"保存按钮在哪"}`),
	})
	if err != nil || result.IsError {
		t.Fatalf("image_vqa failed: result=%#v err=%v", result, err)
	}
	if remoteCalls != 1 {
		t.Fatalf("expected exactly one remote call, got %d", remoteCalls)
	}
	if localCalls != 1 {
		t.Fatalf("expected one local attempt for a locator question, got %d", localCalls)
	}
	if result.Value["engine"] != "remote_vlm" || result.Value["model"] != "deepseek-flash" {
		t.Fatalf("unexpected result: %#v", result.Value)
	}
}

func TestImageVQAAutoSkipsLocalForSemanticQuestion(t *testing.T) {
	remoteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteCalls++
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"字体、字重、字距和颜色已逐项读取。"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	localCalls := 0
	stubLocalVisionAnswer(t, func(context.Context, string, string, string) (visionLocalAnswer, error) {
		localCalls++
		return visionLocalAnswer{Answered: true, Answer: "local should not run"}, nil
	})

	workspace := t.TempDir()
	writeTestPNG(t, workspace)
	tool := NewLocalImageVQATool(ToolDef{}, &Config{
		RepoRoot: workspace,
		ImageVQA: ImageVQAConfig{BaseURL: server.URL, APIKey: "k", ModelName: "deepseek-flash", Mode: imageVQAModeAuto, MaxRetries: 0},
	})
	result, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: workspace,
		Args:      json.RawMessage(`{"image_path":"sample.png","query":"把字体规格和色板颜色值完整读出来"}`),
	})
	if err != nil || result.IsError {
		t.Fatalf("image_vqa failed: result=%#v err=%v", result, err)
	}
	if localCalls != 0 || remoteCalls != 1 {
		t.Fatalf("semantic prompt should skip local and call remote once: local=%d remote=%d", localCalls, remoteCalls)
	}
	if result.Value["engine"] != "remote_vlm" {
		t.Fatalf("unexpected result: %#v", result.Value)
	}
}

// mode=local is the guarantee that no image ever leaves the machine.
func TestImageVQALocalModeDoesNotCallRemote(t *testing.T) {
	remoteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteCalls++
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"remote"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	stubLocalVisionAnswer(t, func(context.Context, string, string, string) (visionLocalAnswer, error) {
		return visionLocalAnswer{Answered: false, Engine: "local_ocr", Reason: "no_text_match", Texts: 3}, nil
	})

	workspace := t.TempDir()
	writeTestPNG(t, workspace)
	tool := NewLocalImageVQATool(ToolDef{}, &Config{
		RepoRoot: workspace,
		ImageVQA: ImageVQAConfig{BaseURL: server.URL, APIKey: "k", Mode: imageVQAModeAuto, MaxRetries: 0},
	})
	result, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: workspace,
		Args:      json.RawMessage(`{"image_path":"sample.png","query":"这个人开心吗","mode":"local"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if remoteCalls != 0 {
		t.Fatalf("mode=local must never call the API, got %d calls", remoteCalls)
	}
	if !result.IsError || result.Value["code"] != "local_query_requires_remote" {
		t.Fatalf("expected local_query_requires_remote, got %#v", result)
	}
}

// mode=remote keeps the old behaviour available for debugging comparisons.
func TestImageVQARemoteModeSkipsLocalPipeline(t *testing.T) {
	remoteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteCalls++
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"remote"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	localCalled := false
	stubLocalVisionAnswer(t, func(context.Context, string, string, string) (visionLocalAnswer, error) {
		localCalled = true
		return visionLocalAnswer{Answered: true, Answer: "local"}, nil
	})

	workspace := t.TempDir()
	writeTestPNG(t, workspace)
	tool := NewLocalImageVQATool(ToolDef{}, &Config{
		RepoRoot: workspace,
		ImageVQA: ImageVQAConfig{BaseURL: server.URL, APIKey: "k", ModelName: "deepseek-flash", Mode: imageVQAModeAuto, MaxRetries: 0},
	})
	result, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: workspace,
		Args:      json.RawMessage(`{"image_path":"sample.png","query":"保存在哪","mode":"remote"}`),
	})
	if err != nil || result.IsError {
		t.Fatalf("image_vqa failed: result=%#v err=%v", result, err)
	}
	if localCalled {
		t.Fatal("mode=remote must skip the local pipeline")
	}
	if remoteCalls != 1 || result.Value["engine"] != "remote_vlm" {
		t.Fatalf("unexpected result: calls=%d value=%#v", remoteCalls, result.Value)
	}
}

// End to end against the real service and a real screenshot. Skipped by default
// because it needs a running desktop and the OCR models.
//
//	$env:RUN_VISION_LOCAL_SMOKE=1
//	$env:VISION_SMOKE_IMAGE="workspace\result\vision-smoke\<run>\after.png"
func TestVisionLocalAnswerSmoke(t *testing.T) {
	if os.Getenv("RUN_VISION_LOCAL_SMOKE") != "1" {
		t.Skip("set RUN_VISION_LOCAL_SMOKE=1 to exercise the local OCR pipeline")
	}
	image := strings.TrimSpace(os.Getenv("VISION_SMOKE_IMAGE"))
	if image == "" {
		t.Skip("set VISION_SMOKE_IMAGE to a PNG on disk")
	}
	if _, err := os.Stat(image); err != nil {
		t.Fatalf("smoke image is unavailable: %v", err)
	}
	repoRoot, err := filepath.Abs("../../../../..")
	if err != nil {
		t.Fatal(err)
	}

	listAnswer, err := answerImageQuestionLocally(context.Background(), repoRoot, image, "图里写了什么文字")
	if err != nil {
		t.Fatalf("local list answer failed: %v", err)
	}
	if !listAnswer.Answered || listAnswer.Texts == 0 {
		t.Fatalf("expected the local pipeline to list text, got %#v", listAnswer)
	}
	t.Logf("list answer engine=%s elements=%d", listAnswer.Engine, listAnswer.Texts)

	matchAnswer, err := answerImageQuestionLocally(context.Background(), repoRoot, image, "文件在哪")
	if err != nil {
		t.Fatalf("local match answer failed: %v", err)
	}
	if !matchAnswer.Answered {
		t.Fatalf("expected the local pipeline to locate 文件, got %#v", matchAnswer)
	}
	if len(matchAnswer.Matches) == 0 {
		t.Fatalf("expected matches to be reported: %#v", matchAnswer)
	}
	t.Logf("match answer: %s", matchAnswer.Answer)

	meaning, err := answerImageQuestionLocally(context.Background(), repoRoot, image, "这张图让人感觉怎么样")
	if err != nil {
		t.Fatalf("local decline check failed: %v", err)
	}
	if meaning.Answered {
		t.Fatalf("a meaning question must not be answered locally: %#v", meaning)
	}
}
