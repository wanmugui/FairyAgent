package local

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	imageVQADefaultModelName     = "deepseek-flash"
	imageVQADefaultBaseURL       = "https://api.deepseek.com"
	imageVQADefaultTimeoutSec    = 120
	imageVQADefaultMaxTokens     = 2048
	imageVQADefaultTemperature   = 0.1
	imageVQADefaultMaxRetries    = 2
	imageVQADefaultRetryBaseMs   = 1000
	imageVQADefaultMaxImageBytes = 16 << 20

	// Answering pipelines. auto is the default: answer from local OCR/YOLO when
	// the question is about visible text or a locatable control, and only spend a
	// multimodal API call when the question needs real visual understanding.
	imageVQAModeAuto   = "auto"
	imageVQAModeLocal  = "local"
	imageVQAModeRemote = "remote"
)

func normalizeImageVQAMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", imageVQAModeAuto:
		return imageVQAModeAuto
	case imageVQAModeLocal, "local_ocr", "ocr", "yolo":
		return imageVQAModeLocal
	case imageVQAModeRemote, "remote_vlm", "vlm", "api":
		return imageVQAModeRemote
	default:
		return ""
	}
}

type localImageVQATool struct {
	schema ToolDef
	cfg    *Config
	client *http.Client
}

func NewLocalImageVQATool(schema ToolDef, cfg *Config) Tool {
	return &localImageVQATool{schema: schema, cfg: cfg}
}

func (t *localImageVQATool) Name() string    { return "image_vqa" }
func (t *localImageVQATool) Schema() ToolDef { return t.schema }

func (t *localImageVQATool) Execute(ctx context.Context, invocation ToolInvocation) (ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	args, err := decodeLocalToolArgs(invocation)
	if err != nil {
		return localErrorResult(t.Name(), err), nil
	}
	query := strings.TrimSpace(localStringArg(args, "query", "question", "prompt"))
	if query == "" {
		return localErrorResult(t.Name(), fmt.Errorf("query is required")), nil
	}
	imagePath := strings.TrimSpace(localStringArg(args, "image_path", "image", "path"))
	if imagePath == "" {
		return localErrorResult(t.Name(), fmt.Errorf("image_path is required")), nil
	}
	localContext, err := localToolContext(invocation)
	if err != nil {
		return localErrorResult(t.Name(), err), nil
	}

	cfg := t.imageVQAConfig()
	mode := strings.ToLower(strings.TrimSpace(localStringArg(args, "mode")))
	if mode == "" {
		mode = cfg.Mode
	}
	mode = normalizeImageVQAMode(mode)
	if mode == "" {
		return localErrorResult(t.Name(), fmt.Errorf("unsupported mode; use auto, local or remote")), nil
	}

	localPath, displayedPath, err := t.resolveImageTarget(localContext.Workspace, imagePath)
	if err != nil {
		return localErrorResult(t.Name(), err), nil
	}

	// OCR plus YOLO is useful only for exact visible-text and coordinate
	// questions. Do not make semantic questions pay for a local pass first.
	localCapable := visionLocalCapableQuery(query)
	if mode == imageVQAModeLocal && !localCapable {
		return ToolResult{Value: map[string]any{
			"tool":       t.Name(),
			"ok":         false,
			"image_path": displayedPath,
			"query":      query,
			"engine":     "local_ocr",
			"code":       "local_query_requires_remote",
			"error":      "this question needs visual reasoning or visual inspection, not OCR/YOLO",
			"answer":     "该问题不是可见文字/坐标定位任务，本地 OCR/YOLO 不适合回答。请改用 mode=remote；如果主模型已有原生视觉，则直接查看附件回答，不要调用 image_vqa。",
		}, IsError: true}, nil
	}
	if mode != imageVQAModeRemote && localCapable && localPath != "" {
		localAnswer, localErr := answerImageQuestionLocallyFunc(ctx, t.repoRoot(), localPath, query)
		if localErr == nil && localAnswer.Answered {
			value := map[string]any{
				"tool":          t.Name(),
				"ok":            true,
				"image_path":    displayedPath,
				"query":         query,
				"answer":        localAnswer.Answer,
				"engine":        localAnswer.Engine,
				"elements_seen": localAnswer.Texts,
			}
			if len(localAnswer.Matches) > 0 {
				value["matches"] = localAnswer.Matches
			}
			if localAnswer.Timing != nil {
				value["timing"] = localAnswer.Timing
			}
			return ToolResult{Value: value}, nil
		}
		if mode == imageVQAModeLocal {
			return ToolResult{Value: map[string]any{
				"tool":          t.Name(),
				"ok":            false,
				"image_path":    displayedPath,
				"query":         query,
				"engine":        "local_ocr",
				"code":          "local_answer_unavailable",
				"error":         "the local OCR/YOLO pipeline cannot answer this question",
				"answer":        "本地 OCR/YOLO 无法确认该问题（它只能回答可见文字和可定位控件）。请改用 mode=remote；如果主模型已有原生视觉，则直接查看附件回答。",
				"elements_seen": localAnswer.Texts,
			}, IsError: true}, nil
		}
	}
	if mode == imageVQAModeLocal {
		return ToolResult{Value: map[string]any{
			"tool":       t.Name(),
			"ok":         false,
			"image_path": displayedPath,
			"query":      query,
			"engine":     "local_ocr",
			"code":       "local_image_unavailable",
			"error":      "the local pipeline needs a file on disk; remote URLs are not downloaded",
		}, IsError: true}, nil
	}

	imageURL, err := t.encodeRemoteImage(localPath, displayedPath)
	if err != nil {
		return localErrorResult(t.Name(), err), nil
	}
	answer, usage, err := t.analyze(ctx, invocation.Timeout, imageURL, query)
	if err != nil {
		return localErrorResult(t.Name(), err), nil
	}

	value := map[string]any{
		"tool":       t.Name(),
		"ok":         true,
		"image_path": displayedPath,
		"query":      query,
		"answer":     answer,
		"engine":     "remote_vlm",
		"model":      cfg.ModelName,
	}
	if usage != nil {
		value["usage"] = map[string]any{
			"prompt_tokens":     usage.PromptTokens,
			"completion_tokens": usage.CompletionTokens,
			"total_tokens":      usage.TotalTokens,
		}
	}
	return ToolResult{Value: value}, nil
}

// resolveImageTarget validates the requested image and returns its on-disk path
// (empty for a remote URL). The empty local path is what tells the caller that
// the on-device OCR/YOLO pipeline cannot be used.
func (t *localImageVQATool) resolveImageTarget(workspace, requested string) (string, string, error) {
	if isHTTPURL(requested) {
		return "", requested, nil
	}
	fullPath, err := resolveLocalWorkspacePath(workspace, requested)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", fmt.Errorf("image not found: %s", requested)
		}
		return "", "", fmt.Errorf("stat image: %w", err)
	}
	if info.IsDir() {
		return "", "", fmt.Errorf("image_path is a directory: %s", requested)
	}
	if info.Size() <= 0 {
		return "", "", fmt.Errorf("image is empty: %s", requested)
	}
	return fullPath, fullPath, nil
}

// encodeRemoteImage builds the data URL the multimodal API needs. It runs only
// after the local pipeline declined to answer, so a locally answered question
// never pays the base64 cost or the upload.
func (t *localImageVQATool) encodeRemoteImage(localPath, displayedPath string) (string, error) {
	if localPath == "" {
		return displayedPath, nil
	}
	cfg := t.imageVQAConfig()
	info, err := os.Stat(localPath)
	if err != nil {
		return "", fmt.Errorf("stat image: %w", err)
	}
	if cfg.MaxImageBytes > 0 && info.Size() > cfg.MaxImageBytes {
		return "", fmt.Errorf("image is too large: %d bytes (max %d bytes)", info.Size(), cfg.MaxImageBytes)
	}
	data, err := os.ReadFile(localPath)
	if err != nil {
		return "", fmt.Errorf("read image: %w", err)
	}
	mediaType := supportedImageMediaType(localPath, data)
	if mediaType == "" {
		return "", fmt.Errorf("unsupported image format for the vision API; use PNG, JPEG, WebP, or GIF: %s", displayedPath)
	}
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func (t *localImageVQATool) repoRoot() string {
	if t == nil || t.cfg == nil {
		return ""
	}
	return t.cfg.RepoRoot
}

type imageVQARequest struct {
	Model       string            `json:"model"`
	Messages    []imageVQAMessage `json:"messages"`
	MaxTokens   int               `json:"max_tokens"`
	Temperature float64           `json:"temperature,omitempty"`
	Stream      bool              `json:"stream"`
}

type imageVQAMessage struct {
	Role    string         `json:"role"`
	Content []imageVQAPart `json:"content"`
}

type imageVQAPart struct {
	Type     string            `json:"type"`
	Text     string            `json:"text,omitempty"`
	ImageURL *imageVQAImageURL `json:"image_url,omitempty"`
}

type imageVQAImageURL struct {
	URL string `json:"url"`
}

type imageVQAResponse struct {
	Choices []struct {
		Message struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *imageVQAUsage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

type imageVQAUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (t *localImageVQATool) analyze(ctx context.Context, invocationTimeout time.Duration, imageURL, query string) (string, *imageVQAUsage, error) {
	cfg := t.imageVQAConfig()
	timeout := time.Duration(cfg.TimeoutSec) * time.Second
	if invocationTimeout > 0 && (timeout <= 0 || invocationTimeout < timeout) {
		timeout = invocationTimeout
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	payload, err := json.Marshal(imageVQARequest{
		Model: cfg.ModelName,
		Messages: []imageVQAMessage{
			{
				Role: "system",
				Content: []imageVQAPart{{
					Type: "text",
					Text: "你是图像分析助手。只依据图片中可见的信息回答，使用用户提问的语言，直接给出结论，不要描述推理过程。无法从图片确认的内容必须明确说明。",
				}},
			},
			{
				Role: "user",
				Content: []imageVQAPart{
					{Type: "text", Text: query},
					{Type: "image_url", ImageURL: &imageVQAImageURL{URL: imageURL}},
				},
			},
		},
		MaxTokens:   cfg.MaxTokens,
		Temperature: cfg.Temperature,
		Stream:      false,
	})
	if err != nil {
		return "", nil, fmt.Errorf("encode image_vqa request: %w", err)
	}

	client := t.client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/") + "/chat/completions"
	attempts := cfg.MaxRetries + 1
	if attempts < 1 {
		attempts = 1
	}
	baseDelay := time.Duration(cfg.RetryBaseMs) * time.Millisecond
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if err := requestCtx.Err(); err != nil {
			return "", nil, err
		}
		req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return "", nil, fmt.Errorf("create image_vqa request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

		resp, err := client.Do(req)
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			resp.Body.Close()
			if readErr != nil {
				lastErr = fmt.Errorf("read image_vqa response: %w", readErr)
			} else if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return parseImageVQAResponse(body)
			} else {
				lastErr = imageVQAAPIError(resp.StatusCode, body)
				if !isRetryableImageVQAStatus(resp.StatusCode) {
					return "", nil, lastErr
				}
			}
		} else {
			lastErr = fmt.Errorf("call DeepSeek vision API: %w", err)
		}
		if attempt+1 >= attempts {
			break
		}
		delay := baseDelay * time.Duration(1<<attempt)
		if delay <= 0 {
			delay = time.Second
		}
		select {
		case <-time.After(delay):
		case <-requestCtx.Done():
			return "", nil, requestCtx.Err()
		}
	}
	return "", nil, lastErr
}

func parseImageVQAResponse(body []byte) (string, *imageVQAUsage, error) {
	var decoded imageVQAResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", nil, fmt.Errorf("parse image_vqa response: %w", err)
	}
	if decoded.Error != nil {
		message := strings.TrimSpace(decoded.Error.Message)
		if message == "" {
			message = "DeepSeek vision API returned an error"
		}
		return "", nil, fmt.Errorf("%s", message)
	}
	if len(decoded.Choices) == 0 {
		return "", nil, fmt.Errorf("DeepSeek vision API returned no choices")
	}
	choice := decoded.Choices[0]
	answer := strings.TrimSpace(choice.Message.Content)
	if answer == "" {
		if strings.TrimSpace(choice.Message.ReasoningContent) != "" && choice.FinishReason == "length" {
			return "", nil, fmt.Errorf("DeepSeek vision model exhausted max_tokens before producing a final answer; increase tools.imageVQA.maxTokens")
		}
		return "", nil, fmt.Errorf("DeepSeek vision API returned an empty answer")
	}
	return answer, decoded.Usage, nil
}

func imageVQAAPIError(status int, body []byte) error {
	var decoded imageVQAResponse
	if err := json.Unmarshal(body, &decoded); err == nil && decoded.Error != nil && strings.TrimSpace(decoded.Error.Message) != "" {
		return fmt.Errorf("DeepSeek vision API error %d: %s", status, strings.TrimSpace(decoded.Error.Message))
	}
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = http.StatusText(status)
	}
	return fmt.Errorf("DeepSeek vision API error %d: %s", status, message)
}

func isRetryableImageVQAStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func (t *localImageVQATool) imageVQAConfig() ImageVQAConfig {
	cfg := ImageVQAConfig{}
	if t != nil && t.cfg != nil {
		cfg = t.cfg.ImageVQA
	}
	if strings.TrimSpace(cfg.ModelName) == "" {
		cfg.ModelName = imageVQADefaultModelName
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = imageVQADefaultBaseURL
	}
	if strings.TrimSpace(cfg.Mode) == "" {
		cfg.Mode = imageVQAModeAuto
	}
	if cfg.TimeoutSec <= 0 {
		cfg.TimeoutSec = imageVQADefaultTimeoutSec
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = imageVQADefaultMaxTokens
	}
	if cfg.Temperature == 0 {
		cfg.Temperature = imageVQADefaultTemperature
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = imageVQADefaultMaxRetries
	}
	if cfg.RetryBaseMs <= 0 {
		cfg.RetryBaseMs = imageVQADefaultRetryBaseMs
	}
	if cfg.MaxImageBytes <= 0 {
		cfg.MaxImageBytes = imageVQADefaultMaxImageBytes
	}
	return cfg
}

func supportedImageMediaType(path string, data []byte) string {
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(mime.TypeByExtension(strings.ToLower(filepath.Ext(path))), ";")[0]))
	if mediaType == "image/jpg" {
		mediaType = "image/jpeg"
	}
	if isSupportedImageMediaType(mediaType) {
		return mediaType
	}
	mediaType = strings.ToLower(strings.TrimSpace(strings.Split(http.DetectContentType(data), ";")[0]))
	if isSupportedImageMediaType(mediaType) {
		return mediaType
	}
	return ""
}

func isSupportedImageMediaType(mediaType string) bool {
	switch mediaType {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
		return true
	default:
		return false
	}
}
