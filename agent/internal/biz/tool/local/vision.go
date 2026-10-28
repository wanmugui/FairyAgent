package local

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The pixel path used when a target has no accessibility tree: games, Electron
// and Flutter canvas, remote desktop, video. It is a long-lived local service
// (ONNX sessions and the capture buffer stay warm) rather than a per-call
// process, because loading the models costs about a second on its own.
//
//go:embed cua_vision_service.py
var visionServiceScript string

const (
	visionServicePort       = 8791
	visionServiceScriptName = "cua_vision_service.py"
	visionStartupTimeout    = 90 * time.Second
	// OCR on a full 4K desktop measures ~5 s on a busy laptop and ~1.9 s on a
	// single window region, so the HTTP client must outlast a cold first pass.
	visionRequestTimeout = 180 * time.Second
)

var visionActions = map[string]string{
	"vision_start":    "session/start",
	"vision_stop":     "session/stop",
	"vision_status":   "health",
	"ocr":             "ocr",
	"find_text":       "find_text",
	"detect_elements": "elements",
	"locate_text":     "locate",
}

func isVisionAction(action string) bool {
	_, ok := visionActions[strings.ToLower(strings.TrimSpace(action))]
	return ok
}

// Seams so the routing and coordinate logic can be tested without spawning a
// real service or loading models.
var (
	ensureVisionServiceFunc = ensureVisionService
	visionPostFunc          = visionPost
	// answerImageQuestionLocallyFunc is a seam: image_vqa routing (local first,
	// remote fallback) is tested without loading OCR models.
	answerImageQuestionLocallyFunc = answerImageQuestionLocally
)

// visionServiceState is process wide: tool instances come and go, but the child
// process and its warm ONNX sessions should not.
type visionServiceState struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	baseURL string
	started time.Time
}

var visionService = &visionServiceState{}

func (s *visionServiceState) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_, _ = s.cmd.Process.Wait()
	}
	s.cmd = nil
	s.baseURL = ""
}

func (t *localComputerTool) executeVision(
	ctx context.Context,
	action string,
	args map[string]any,
	workspace string,
	invocation ToolInvocation,
) (ToolResult, error) {
	route, ok := visionActions[strings.ToLower(strings.TrimSpace(action))]
	if !ok {
		return localErrorResult(t.name, fmt.Errorf("unsupported vision action %q", action)), nil
	}

	repoRoot := ""
	if t.cfg != nil {
		repoRoot = t.cfg.RepoRoot
	}
	baseURL, err := ensureVisionServiceFunc(ctx, repoRoot)
	if err != nil {
		return ToolResult{Value: map[string]any{
			"tool":  t.name,
			"ok":    false,
			"code":  "vision_unavailable",
			"error": err.Error(),
		}, IsError: true}, nil
	}

	payload := make(map[string]any, len(args))
	for key, value := range args {
		payload[key] = value
	}
	// Default the source switch to the desktop: this tool's job is computer
	// control, camera support is opt-in per request.
	if _, exists := payload["source"]; !exists {
		payload["source"] = "screen"
	}
	if path, ok := payload["image_path"].(string); ok && strings.TrimSpace(path) != "" {
		resolved, resolveErr := resolveLocalWorkspacePath(workspace, path)
		if resolveErr != nil {
			return localErrorResult(t.name, resolveErr), nil
		}
		payload["image_path"] = resolved
	}

	timeout := visionRequestTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && remaining < timeout {
			timeout = remaining
		}
	}

	body, err := visionPostFunc(ctx, baseURL+"/"+route, payload, timeout)
	if err != nil {
		// A stale child (crashed, port stolen) must not poison the session: drop
		// it so the next call respawns cleanly.
		visionService.stop()
		return ToolResult{Value: map[string]any{
			"tool":  t.name,
			"ok":    false,
			"code":  "vision_request_failed",
			"error": err.Error(),
		}, IsError: true}, nil
	}

	offsetVisionCoordinates(body, args)
	body["tool"] = t.name
	okValue, hasOK := body["ok"].(bool)
	if !hasOK {
		okValue = true
		body["ok"] = true
	}
	return ToolResult{Value: body, IsError: !okValue}, nil
}

// offsetVisionCoordinates converts image relative boxes into screen pixels.
//
// The service reports boxes against the frame it analysed. When the caller asked
// for a region (typically a window frame from computer_window), adding the region
// origin turns every box and centre into a coordinate the pointer tool can click
// directly, which keeps the "screenshot then click" hop from silently mixing
// coordinate spaces.
func offsetVisionCoordinates(body map[string]any, args map[string]any) {
	// The service already translates region-relative boxes into screen pixels and
	// marks the response. Only do it here when it did not (an older service, or a
	// response that carries no coordinate space at all).
	if body["coordinate_space"] == "screen" {
		return
	}
	originX := 0
	originY := 0
	if region, ok := args["region"].(map[string]any); ok {
		originX = visionInt(region["x"])
		originY = visionInt(region["y"])
	}
	for _, key := range []string{"texts", "elements", "detections"} {
		items, ok := body[key].([]any)
		if !ok {
			continue
		}
		for _, item := range items {
			if entry, ok := item.(map[string]any); ok {
				shiftVisionEntry(entry, originX, originY)
			}
		}
	}
	if matched, ok := body["matched"].(map[string]any); ok {
		shiftVisionEntry(matched, originX, originY)
	}
	if bounds, ok := body["bounds"].(map[string]any); ok {
		shiftVisionEntry(map[string]any{"bounds": bounds}, originX, originY)
	}
	if point, ok := body["point"].(map[string]any); ok {
		point["x"] = visionInt(point["x"]) + originX
		point["y"] = visionInt(point["y"]) + originY
	}
	if body["texts"] != nil || body["elements"] != nil || body["detections"] != nil ||
		body["matched"] != nil || body["bounds"] != nil || body["point"] != nil {
		body["coordinate_space"] = "screen"
	}
}

func shiftVisionEntry(entry map[string]any, originX, originY int) {
	if bounds, ok := entry["bounds"].(map[string]any); ok {
		bounds["x"] = visionInt(bounds["x"]) + originX
		bounds["y"] = visionInt(bounds["y"]) + originY
	}
	if box, ok := entry["box"].(map[string]any); ok {
		box["x"] = visionInt(box["x"]) + originX
		box["y"] = visionInt(box["y"]) + originY
	}
	if center, ok := entry["center"].(map[string]any); ok {
		center["x"] = visionInt(center["x"]) + originX
		center["y"] = visionInt(center["y"]) + originY
	}
}

func visionInt(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case json.Number:
		parsed, _ := typed.Int64()
		return int(parsed)
	default:
		return 0
	}
}

// ---------------------------------------------------------------------------
// Local answerer
//
// image_vqa used to send every question to a remote multimodal model. Most
// questions about a desktop screenshot are really "what text is here" or "where
// is this control", and those are answered exactly and for free by OCR/YOLO on
// the local frame. This answerer covers those cases and reports honestly when a
// question needs real visual understanding, so the caller can fall back.
// ---------------------------------------------------------------------------

// visionLocalAnswer is an answer produced entirely on device.
type visionLocalAnswer struct {
	Answered bool
	Answer   string
	Engine   string
	Matches  []map[string]any
	Timing   map[string]any
	Reason   string
	Texts    int
}

type visionElement struct {
	Text   string
	Source string
	Bounds map[string]any
	Score  float64
}

func visionCollectElements(body map[string]any) []visionElement {
	raw, ok := body["elements"].([]any)
	if !ok {
		return nil
	}
	elements := make([]visionElement, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := entry["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		source, _ := entry["source"].(string)
		bounds, _ := entry["bounds"].(map[string]any)
		score := 0.0
		if value, ok := entry["confidence"].(float64); ok {
			score = value
		}
		elements = append(elements, visionElement{Text: name, Source: source, Bounds: bounds, Score: score})
	}
	return elements
}

// visionListIntent reports whether the question is asking for the text itself
// rather than for one particular control.
func visionListIntent(query string) bool {
	lower := strings.ToLower(query)
	for _, marker := range []string{
		"什么文字", "写了什么", "写的什么", "文字内容", "文本内容", "有哪些字",
		"提取文字", "识别文字", "所有文字", "全部文字", "上面写", "说的是什么",
		"what text", "what does it say", "read the text", "extract text", "list the text",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func visionCountIntent(query string) bool {
	lower := strings.ToLower(query)
	for _, marker := range []string{"几个", "几处", "多少", "数量", "how many", "count"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// visionLocalCapableQuery keeps the OCR/YOLO pipeline on the narrow jobs it is
// actually good at: listing visible text, locating a known label, or checking
// whether that label exists. Semantic questions go straight to the multimodal
// model instead of paying for an OCR pass that cannot answer them.
func visionLocalCapableQuery(query string) bool {
	lower := strings.ToLower(strings.TrimSpace(query))
	if lower == "" || len([]rune(lower)) > 160 {
		return false
	}
	for _, marker := range []string{
		"描述", "分析", "解释", "为什么", "怎么", "如何", "含义", "意思", "作用",
		"情绪", "开心", "氛围", "色调", "风格", "字体", "字号", "字重", "字距",
		"色板", "颜色值", "关系", "比较", "评价", "建议", "看起来",
		"describe", "analyze", "explain", "why", "how", "meaning", "mood",
		"tone", "font", "palette", "color value", "compare",
	} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	if visionListIntent(lower) {
		return true
	}
	hasLocalTarget := false
	for _, marker := range []string{
		"文字", "文本", "按钮", "图标", "控件", "菜单", "标签", "链接",
		"输入框", "选项", "坐标", "位置", "button", "icon", "control",
		"menu", "label", "link", "input", "coordinate", "text",
	} {
		if strings.Contains(lower, marker) {
			hasLocalTarget = true
			break
		}
	}
	if !hasLocalTarget {
		return false
	}
	for _, marker := range []string{
		"在哪", "哪里", "位置", "坐标", "定位", "找到", "帮我找", "查找",
		"有没有", "是否有", "是否存在", "在哪裡",
		"where is", "locate", "find the", "find text", "click the",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	if visionCountIntent(lower) {
		return true
	}
	return false
}

// visionMatchQuery finds recognised text that the question literally refers to.
//
// The rule is deliberately narrow: an element only counts when its label appears
// inside the question, or the question appears inside the label. That covers
// "图里有没有『保存』按钮" and "帮我找到开始游戏" without any NLP, and it refuses to
// guess when the question is really about meaning ("这个人看起来开心吗").
func visionMatchQuery(query string, elements []visionElement) []visionElement {
	lowerQuery := strings.ToLower(strings.TrimSpace(query))
	if lowerQuery == "" {
		return nil
	}
	var matches []visionElement
	for _, element := range elements {
		label := strings.ToLower(strings.TrimSpace(element.Text))
		if len([]rune(label)) < 2 {
			continue
		}
		if strings.Contains(lowerQuery, label) ||
			(len([]rune(lowerQuery)) >= 2 && strings.Contains(label, lowerQuery)) {
			matches = append(matches, element)
		}
	}
	// The most specific label wins: "保存全部" should beat "保存".
	sort.SliceStable(matches, func(i, j int) bool {
		return len([]rune(matches[i].Text)) > len([]rune(matches[j].Text))
	})
	return matches
}

func visionElementCenter(element visionElement) (int, int, int, int, bool) {
	if element.Bounds == nil {
		return 0, 0, 0, 0, false
	}
	x := visionInt(element.Bounds["x"])
	y := visionInt(element.Bounds["y"])
	width := visionInt(element.Bounds["width"])
	height := visionInt(element.Bounds["height"])
	if width <= 0 || height <= 0 {
		return 0, 0, 0, 0, false
	}
	return x + width/2, y + height/2, width, height, true
}

// answerImageQuestionLocally answers a question from OCR/YOLO alone. Answered is
// false whenever the local pipeline cannot answer with confidence, which is the
// signal for the caller to fall back to the multimodal API.
func answerImageQuestionLocally(ctx context.Context, repoRoot, imagePath, query string) (visionLocalAnswer, error) {
	if strings.TrimSpace(repoRoot) == "" || strings.TrimSpace(imagePath) == "" {
		return visionLocalAnswer{}, errors.New("a local image path and repo root are required")
	}
	baseURL, err := ensureVisionServiceFunc(ctx, repoRoot)
	if err != nil {
		return visionLocalAnswer{}, err
	}
	payload := map[string]any{
		"image_path": imagePath,
		"ocr":        true,
		"yolo":       true,
	}
	body, err := visionPostFunc(ctx, baseURL+"/elements", payload, visionRequestTimeout)
	if err != nil {
		return visionLocalAnswer{}, err
	}
	elements := visionCollectElements(body)
	timing, _ := body["timing"].(map[string]any)
	engine := "local_ocr"
	for _, element := range elements {
		if element.Source == "yolo" {
			engine = "local_ocr+yolo"
			break
		}
	}
	result := visionLocalAnswer{Engine: engine, Timing: timing, Texts: len(elements)}

	if visionListIntent(query) && len(elements) > 0 {
		var builder strings.Builder
		builder.WriteString(fmt.Sprintf("图中识别到 %d 段文字：\n", len(elements)))
		for index, element := range elements {
			if index >= 60 {
				builder.WriteString(fmt.Sprintf("…（其余 %d 段省略）", len(elements)-index))
				break
			}
			builder.WriteString("- " + element.Text + "\n")
		}
		result.Answered = true
		result.Answer = strings.TrimRight(builder.String(), "\n")
		return result, nil
	}

	matches := visionMatchQuery(query, elements)
	if len(matches) == 0 {
		result.Reason = "no_text_match"
		return result, nil
	}

	best := matches[0]
	answer := ""
	if centerX, centerY, width, height, ok := visionElementCenter(best); ok {
		answer = fmt.Sprintf("图中存在「%s」，中心坐标 (%d, %d)，区域 %d×%d。", best.Text, centerX, centerY, width, height)
	} else {
		answer = fmt.Sprintf("图中存在「%s」。", best.Text)
	}
	if visionCountIntent(query) {
		answer += fmt.Sprintf("匹配到 %d 处。", len(matches))
	}
	result.Answered = true
	result.Answer = answer
	for _, match := range matches {
		entry := map[string]any{"text": match.Text, "source": match.Source}
		if match.Bounds != nil {
			entry["bounds"] = match.Bounds
			if centerX, centerY, _, _, ok := visionElementCenter(match); ok {
				entry["center"] = map[string]any{"x": centerX, "y": centerY}
			}
		}
		result.Matches = append(result.Matches, entry)
	}
	return result, nil
}

// ensureVisionService returns the base URL of a running vision service, starting
// one when needed.
//
// Lifecycle is deliberately session scoped rather than boot scoped: the agent
// decides when it wants a pixel pipeline, and outside a real-time screen or
// camera observation window the process should not be resident at all. The child
// is also given our PID so it cannot outlive the agent.
func ensureVisionService(ctx context.Context, repoRoot string) (string, error) {
	visionService.mu.Lock()
	defer visionService.mu.Unlock()

	if visionService.baseURL != "" && visionHealthy(ctx, visionService.baseURL) {
		return visionService.baseURL, nil
	}
	visionService.baseURL = ""

	if strings.TrimSpace(repoRoot) == "" {
		return "", errors.New("repo root is unavailable, so the vision service cannot be started")
	}
	directory := filepath.Join(repoRoot, ".tools", "vision-service")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", fmt.Errorf("create vision service directory: %w", err)
	}
	scriptPath := filepath.Join(directory, visionServiceScriptName)
	if err := os.WriteFile(scriptPath, []byte(visionServiceScript), 0o644); err != nil {
		return "", fmt.Errorf("write vision service script: %w", err)
	}

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", visionServicePort)
	// Another agent instance may already own the port; adopt it instead of
	// spawning a duplicate service.
	if visionHealthy(ctx, baseURL) {
		visionService.baseURL = baseURL
		visionService.started = time.Now()
		return baseURL, nil
	}

	python, err := resolveVisionPython(repoRoot)
	if err != nil {
		return "", err
	}
	modelDir := filepath.Join(repoRoot, ".tools", "vision-models")
	command := exec.Command(
		python,
		scriptPath,
		"--port", fmt.Sprintf("%d", visionServicePort),
		"--model-dir", modelDir,
		"--idle-timeout", "180",
		"--parent-pid", fmt.Sprintf("%d", os.Getpid()),
	)
	command.Dir = directory
	command.Env = append(os.Environ(), "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8")
	if err := command.Start(); err != nil {
		return "", fmt.Errorf("start vision service: %w", err)
	}
	visionService.cmd = command
	visionService.baseURL = baseURL
	visionService.started = time.Now()

	deadline := time.Now().Add(visionStartupTimeout)
	for time.Now().Before(deadline) {
		if visionHealthy(ctx, baseURL) {
			return baseURL, nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	visionService.baseURL = ""
	return "", fmt.Errorf("vision service did not become healthy within %s", visionStartupTimeout)
}

// resolveVisionPython prefers the project venv, which is where the vision
// dependencies are installed.
func resolveVisionPython(repoRoot string) (string, error) {
	candidates := []string{
		filepath.Join(repoRoot, ".tools", "venv", "Scripts", "python.exe"),
		filepath.Join(repoRoot, ".tools", "venv", "bin", "python"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	if path, err := exec.LookPath("python"); err == nil {
		return path, nil
	}
	return "", errors.New("no Python interpreter was found for the vision service")
}

func visionHealthy(ctx context.Context, baseURL string) bool {
	requestCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, baseURL+"/health", nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode == http.StatusOK
}

func visionPost(ctx context.Context, url string, payload map[string]any, timeout time.Duration) (map[string]any, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode vision request: %w", err)
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: timeout}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("decode vision response (status %d): %w", response.StatusCode, err)
	}
	if response.StatusCode >= 400 {
		message, _ := decoded["error"].(string)
		if message == "" {
			message = strings.TrimSpace(string(raw))
		}
		return nil, fmt.Errorf("vision service error %d: %s", response.StatusCode, message)
	}
	return decoded, nil
}
