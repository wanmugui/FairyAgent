package local

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// image_generate runs text-to-image through MiniMax's image_generation API.
//
// The prompt is always supplied by the caller (the Agent builds it from the
// skill prompt templates); this tool never invents or rewrites it. Prompt
// optimization stays off unless the caller explicitly opts in, so what the
// Agent wrote is what the model receives.
const (
	imageGenerateDefaultModelName     = "image-01"
	imageGenerateDefaultBaseURL       = "https://api.minimaxi.com/v1"
	imageGenerateDefaultAspectRatio   = "16:9"
	imageGenerateDefaultTimeoutSec    = 180
	imageGenerateDefaultMaxRetries    = 2
	imageGenerateDefaultRetryBaseMs   = 1500
	imageGenerateDefaultMaxImages     = 4
	imageGenerateDefaultMaxImageBytes = 24 << 20
	imageGenerateMaxResponseBytes     = 2 << 20
	imageGenerateRateLimitStatusCode  = 1002
	imageGenerateSuccessStatusCode    = 0
)

// imageGenerateAspectRatios is the set MiniMax image-01 accepts. Keys are the
// wire values; the float is the width/height ratio used for nearest-match of
// legacy "WxH" inputs.
var imageGenerateAspectRatios = map[string]float64{
	"1:1":  1,
	"16:9": 16.0 / 9.0,
	"4:3":  4.0 / 3.0,
	"3:2":  1.5,
	"2:3":  2.0 / 3.0,
	"3:4":  0.75,
	"9:16": 9.0 / 16.0,
	"21:9": 21.0 / 9.0,
}

// imageGenerateAspectRatioOrder keeps nearest-match resolution deterministic.
var imageGenerateAspectRatioOrder = []string{"1:1", "16:9", "4:3", "3:2", "2:3", "3:4", "9:16", "21:9"}

type localImageGenerateTool struct {
	schema ToolDef
	cfg    *Config
	client *http.Client
}

func NewLocalImageGenerateTool(schema ToolDef, cfg *Config) Tool {
	return &localImageGenerateTool{schema: schema, cfg: cfg}
}

func (t *localImageGenerateTool) Name() string    { return "image_generate" }
func (t *localImageGenerateTool) Schema() ToolDef { return t.schema }

func (t *localImageGenerateTool) Execute(ctx context.Context, invocation ToolInvocation) (ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	args, err := decodeLocalToolArgs(invocation)
	if err != nil {
		return localErrorResult(t.Name(), err), nil
	}
	prompt := strings.TrimSpace(localStringArg(args, "prompt"))
	if prompt == "" {
		return localErrorResult(t.Name(), fmt.Errorf("prompt is required")), nil
	}
	if style := strings.TrimSpace(localStringArg(args, "style")); style != "" {
		prompt = prompt + "\n\n风格要求：" + style
	}

	cfg := t.imageGenerateConfig()
	aspectRatio, err := normalizeImageAspectRatio(localStringArg(args, "aspect_ratio", "image_size", "size"))
	if err != nil {
		return localErrorResult(t.Name(), err), nil
	}
	if aspectRatio == "" {
		aspectRatio = cfg.AspectRatio
	}
	count := localIntArg(args, "n", 1)
	if count < 1 {
		count = 1
	}
	if cfg.MaxImages > 0 && count > cfg.MaxImages {
		count = cfg.MaxImages
	}

	images, err := t.generate(ctx, invocation.Timeout, prompt, aspectRatio, count, cfg, args)
	if err != nil {
		return localErrorResult(t.Name(), err), nil
	}
	if len(images) == 0 {
		return localErrorResult(t.Name(), fmt.Errorf("MiniMax returned no images")), nil
	}

	value := map[string]any{
		"tool":         t.Name(),
		"ok":           true,
		"model":        cfg.ModelName,
		"prompt":       prompt,
		"aspect_ratio": aspectRatio,
		"count":        len(images),
	}
	if _, ok := args["style"]; ok {
		value["style"] = localStringArg(args, "style")
	}

	download := true
	if _, ok := args["download"]; ok {
		download = localBoolArg(args, "download")
	}
	value["download"] = download

	requested := strings.TrimSpace(localStringArg(args, "result_image_path", "output_path", "path"))
	if !download {
		urls := make([]string, 0, len(images))
		for _, image := range images {
			if image.url != "" {
				urls = append(urls, image.url)
			}
		}
		value["image_urls"] = urls
		return ToolResult{Value: value}, nil
	}

	localContext, err := localToolContext(invocation)
	if err != nil {
		return localErrorResult(t.Name(), err), nil
	}
	plan, err := imageGenerateOutputPlan(localContext.Workspace, requested, prompt, len(images))
	if err != nil {
		return localErrorResult(t.Name(), err), nil
	}

	results := make([]map[string]any, 0, len(images))
	for index, image := range images {
		localPath, size, mediaType, saveErr := t.saveImage(ctx, plan, index, image, cfg)
		item := map[string]any{
			"index":      index + 1,
			"image_url":  image.url,
			"local_path": nil,
		}
		if saveErr != nil {
			item["save_error"] = saveErr.Error()
		} else {
			item["local_path"] = localPath
			item["bytes"] = size
			item["content_type"] = mediaType
		}
		results = append(results, item)
	}
	value["results"] = results
	if first, _ := results[0]["local_path"].(string); first != "" {
		value["local_path"] = first
	}
	return ToolResult{Value: value}, nil
}

type imageGenerateImage struct {
	url  string
	data []byte
}

type imageGenerateResponse struct {
	ID   string `json:"id"`
	Data *struct {
		ImageURLs   []string `json:"image_urls"`
		ImageBase64 []string `json:"image_base64"`
	} `json:"data"`
	Metadata struct {
		SuccessCount string `json:"success_count"`
		FailedCount  string `json:"failed_count"`
	} `json:"metadata"`
	BaseResp struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
	} `json:"base_resp"`
}

func (t *localImageGenerateTool) generate(
	ctx context.Context,
	invocationTimeout time.Duration,
	prompt string,
	aspectRatio string,
	count int,
	cfg ImageGenerateConfig,
	args map[string]any,
) ([]imageGenerateImage, error) {
	timeout := time.Duration(cfg.TimeoutSec) * time.Second
	if invocationTimeout > 0 && (timeout <= 0 || invocationTimeout < timeout) {
		timeout = invocationTimeout
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// base64 keeps the bytes in-process, so generation does not depend on the
	// temporary OSS URL surviving until the download step.
	requestBody := map[string]any{
		"model":            cfg.ModelName,
		"prompt":           prompt,
		"aspect_ratio":     aspectRatio,
		"response_format":  "base64",
		"n":                count,
		"prompt_optimizer": localBoolArg(args, "prompt_optimizer"),
		"aigc_watermark":   localBoolArg(args, "aigc_watermark"),
	}
	// Reference images condition the generation via the provider's
	// subject_reference field. Omitted entirely when no image_paths are given so
	// plain text-to-image requests stay byte-identical to before.
	refs, err := localImageGenerateReferenceSubject(localStringSliceArg(args, "image_paths"))
	if err != nil {
		return nil, err
	}
	if len(refs) > 0 {
		requestBody["subject_reference"] = refs
	}
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("encode image_generate request: %w", err)
	}

	client := t.client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/") + "/image_generation"
	attempts := cfg.MaxRetries + 1
	if attempts < 1 {
		attempts = 1
	}
	baseDelay := time.Duration(cfg.RetryBaseMs) * time.Millisecond
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if err := requestCtx.Err(); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("create image_generate request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("call MiniMax image API: %w", err)
		} else {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, imageGenerateMaxResponseBytes))
			resp.Body.Close()
			if readErr != nil {
				lastErr = fmt.Errorf("read MiniMax image response: %w", readErr)
			} else if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				lastErr = imageGenerateAPIError(resp.StatusCode, body)
				if !isRetryableImageGenerateStatus(resp.StatusCode) {
					return nil, lastErr
				}
			} else if images, err := parseImageGenerateResponse(body); err != nil {
				lastErr = err
				if !isRetryableImageGenerateStatusCode(lastErr) {
					return nil, lastErr
				}
			} else {
				return images, nil
			}
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
			return nil, requestCtx.Err()
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("MiniMax image API failed")
	}
	return nil, lastErr
}

// imageGenerateError carries the MiniMax business status code so callers can
// tell a rate limit (worth retrying) from a bad-prompt error (not worth it).
type imageGenerateError struct {
	statusCode int
	message    string
}

func (e *imageGenerateError) Error() string { return e.message }

func parseImageGenerateResponse(body []byte) ([]imageGenerateImage, error) {
	var decoded imageGenerateResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("parse MiniMax image response: %w", err)
	}
	if decoded.BaseResp.StatusCode != imageGenerateSuccessStatusCode {
		message := strings.TrimSpace(decoded.BaseResp.StatusMsg)
		if message == "" {
			message = "MiniMax image API returned an error"
		}
		return nil, &imageGenerateError{
			statusCode: decoded.BaseResp.StatusCode,
			message:    fmt.Sprintf("MiniMax image API error %d: %s", decoded.BaseResp.StatusCode, message),
		}
	}
	if decoded.Data == nil {
		return nil, fmt.Errorf("MiniMax image API returned no data")
	}
	images := make([]imageGenerateImage, 0, len(decoded.Data.ImageBase64)+len(decoded.Data.ImageURLs))
	for _, encoded := range decoded.Data.ImageBase64 {
		encoded = strings.TrimSpace(encoded)
		if encoded == "" {
			continue
		}
		if comma := strings.Index(encoded, ","); strings.HasPrefix(encoded, "data:") && comma > 0 {
			encoded = encoded[comma+1:]
		}
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("decode MiniMax image payload: %w", err)
		}
		images = append(images, imageGenerateImage{data: data})
	}
	for _, imageURL := range decoded.Data.ImageURLs {
		imageURL = strings.TrimSpace(imageURL)
		if imageURL == "" {
			continue
		}
		images = append(images, imageGenerateImage{url: imageURL})
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("MiniMax image API returned no images")
	}
	return images, nil
}

func imageGenerateAPIError(status int, body []byte) error {
	var decoded imageGenerateResponse
	if err := json.Unmarshal(body, &decoded); err == nil && strings.TrimSpace(decoded.BaseResp.StatusMsg) != "" {
		return &imageGenerateError{
			statusCode: decoded.BaseResp.StatusCode,
			message:    fmt.Sprintf("MiniMax image API error %d: %s", status, strings.TrimSpace(decoded.BaseResp.StatusMsg)),
		}
	}
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = http.StatusText(status)
	}
	return fmt.Errorf("MiniMax image API error %d: %s", status, truncateLocalMessage(message, 300))
}

func isRetryableImageGenerateStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func isRetryableImageGenerateStatusCode(err error) bool {
	var apiErr *imageGenerateError
	if errors.As(err, &apiErr) {
		return apiErr.statusCode == imageGenerateRateLimitStatusCode
	}
	return false
}

func truncateLocalMessage(message string, limit int) string {
	if limit <= 0 || len(message) <= limit {
		return message
	}
	return message[:limit] + "..."
}

type imageGenerateOutput struct {
	dir       string
	prefix    string
	exactPath string
}

func imageGenerateOutputPlan(workspace, requested, prompt string, count int) (imageGenerateOutput, error) {
	if strings.TrimSpace(requested) == "" {
		dir := filepath.Join(workspace, "result", "image-generate", imageGenerateSlug(prompt)+"-"+time.Now().Format("20060102-150405"))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return imageGenerateOutput{}, fmt.Errorf("create image_generate directory: %w", err)
		}
		return imageGenerateOutput{dir: dir, prefix: "image"}, nil
	}
	resolved, err := resolveLocalWorkspacePath(workspace, requested)
	if err != nil {
		return imageGenerateOutput{}, err
	}
	ext := imageGenerateKnownExtension(filepath.Ext(resolved))
	if ext != "" && count == 1 {
		if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
			return imageGenerateOutput{}, fmt.Errorf("create image output directory: %w", err)
		}
		return imageGenerateOutput{dir: filepath.Dir(resolved), prefix: strings.TrimSuffix(filepath.Base(resolved), ext), exactPath: resolved}, nil
	}
	dir := resolved
	prefix := "image"
	if ext != "" {
		dir = filepath.Dir(resolved)
		prefix = strings.TrimSuffix(filepath.Base(resolved), ext)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return imageGenerateOutput{}, fmt.Errorf("create image output directory: %w", err)
	}
	return imageGenerateOutput{dir: dir, prefix: prefix}, nil
}

func (t *localImageGenerateTool) saveImage(ctx context.Context, plan imageGenerateOutput, index int, image imageGenerateImage, cfg ImageGenerateConfig) (string, int, string, error) {
	data := image.data
	if len(data) == 0 && image.url != "" {
		downloaded, err := t.downloadImage(ctx, image.url, cfg)
		if err != nil {
			return "", 0, "", err
		}
		data = downloaded
	}
	if len(data) == 0 {
		return "", 0, "", fmt.Errorf("MiniMax returned an empty image payload")
	}
	if cfg.MaxImageBytes > 0 && int64(len(data)) > cfg.MaxImageBytes {
		return "", 0, "", fmt.Errorf("generated image exceeds maximum size of %d bytes", cfg.MaxImageBytes)
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(http.DetectContentType(data), ";")[0]))
	ext := imageGenerateExtensionForMediaType(mediaType)
	if ext == "" {
		ext = ".jpg"
	}
	target := plan.exactPath
	if target == "" {
		target = filepath.Join(plan.dir, fmt.Sprintf("%s-%02d%s", plan.prefix, index+1, ext))
	}
	tmp, err := os.CreateTemp(plan.dir, ".image-generate-*")
	if err != nil {
		return "", 0, "", fmt.Errorf("create temporary image: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", 0, "", fmt.Errorf("write image: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", 0, "", fmt.Errorf("close image: %w", err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		_ = os.Remove(tmpPath)
		return "", 0, "", fmt.Errorf("save image: %w", err)
	}
	return target, len(data), mediaType, nil
}

func (t *localImageGenerateTool) downloadImage(ctx context.Context, imageURL string, cfg ImageGenerateConfig) ([]byte, error) {
	limit := cfg.MaxImageBytes
	if limit <= 0 {
		limit = imageGenerateDefaultMaxImageBytes
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build image download request: %w", err)
	}
	req.Header.Set("User-Agent", "fairy-agent/1.0")
	req.Header.Set("Accept", "image/*,*/*;q=0.8")
	client := t.client
	if client == nil {
		client = &http.Client{Timeout: 120 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download generated image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download generated image: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read generated image: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("generated image exceeds maximum size of %d bytes", limit)
	}
	return data, nil
}

// normalizeImageAspectRatio accepts MiniMax's own "16:9" form, common English
// aliases, and legacy "1024x1024" pixel sizes (mapped to the nearest supported
// ratio). An empty input means "use the configured default".
func normalizeImageAspectRatio(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return "", nil
	}
	switch value {
	case "landscape", "wide", "horizontal", "16x9":
		return "16:9", nil
	case "portrait", "vertical", "tall", "9x16":
		return "9:16", nil
	case "square":
		return "1:1", nil
	}
	if _, ok := imageGenerateAspectRatios[value]; ok {
		return value, nil
	}
	if width, height, ok := parsePixelSize(value); ok && width > 0 && height > 0 {
		target := float64(width) / float64(height)
		best := imageGenerateAspectRatioOrder[0]
		bestDelta := -1.0
		for _, candidate := range imageGenerateAspectRatioOrder {
			delta := imageGenerateAspectRatios[candidate] - target
			if delta < 0 {
				delta = -delta
			}
			if bestDelta < 0 || delta < bestDelta {
				best = candidate
				bestDelta = delta
			}
		}
		return best, nil
	}
	return "", fmt.Errorf("unsupported aspect ratio %q; use one of %s or a pixel size like 1024x1024",
		raw, strings.Join(imageGenerateAspectRatioOrder, ", "))
}

func parsePixelSize(value string) (int, int, bool) {
	for _, separator := range []string{"x", "*", "×"} {
		if !strings.Contains(value, separator) {
			continue
		}
		parts := strings.SplitN(value, separator, 2)
		if len(parts) != 2 {
			continue
		}
		width, errW := strconv.Atoi(strings.TrimSpace(parts[0]))
		height, errH := strconv.Atoi(strings.TrimSpace(parts[1]))
		if errW == nil && errH == nil {
			return width, height, true
		}
	}
	return 0, 0, false
}

// localImageGenerateRefFidelity matches the value proven against
// /image_generation in workspace/3d-pipeline-dev/exp_threeview.py.
const localImageGenerateRefFidelity = 0.8

// localStringSliceArg reads a repeated-string argument. JSON decoding leaves
// array arguments as []any while in-process callers may pass []string, so
// accept both and skip non-strings.
func localStringSliceArg(args map[string]any, key string) []string {
	if args == nil {
		return nil
	}
	var raw []any
	switch v := args[key].(type) {
	case []string:
		out := make([]string, 0, len(v))
		for _, s := range v {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		raw = v
	default:
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// localImageGenerateReferenceSubject builds the provider's subject_reference
// payload from local reference images: bare base64 per image (no data URI
// prefix), tagged type=character.
//
// Unreadable or unsupported files are rejected rather than silently skipped. A
// quietly ignored reference is how you ship an asset that claims to be
// reference-based but is really just text-to-image.
func localImageGenerateReferenceSubject(paths []string) ([]map[string]any, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	out := make([]map[string]any, 0, len(paths))
	for _, p := range paths {
		if imageGenerateKnownExtension(filepath.Ext(p)) == "" {
			return nil, fmt.Errorf("image_generate: unsupported reference image type for %q (want .png/.jpg/.jpeg/.webp)", p)
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("image_generate: read reference image %q: %w", p, err)
		}
		if len(raw) == 0 {
			return nil, fmt.Errorf("image_generate: reference image %q is empty", p)
		}
		out = append(out, map[string]any{
			"type":     "character",
			"image":    []string{base64.StdEncoding.EncodeToString(raw)},
			"fidelity": localImageGenerateRefFidelity,
		})
	}
	return out, nil
}

func imageGenerateExtensionForMediaType(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ""
	}
}

func imageGenerateKnownExtension(ext string) string {
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case ".png":
		return ".png"
	case ".jpg", ".jpeg":
		return ".jpg"
	case ".webp":
		return ".webp"
	case ".gif":
		return ".gif"
	default:
		return ""
	}
}

func imageGenerateSlug(prompt string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(prompt)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '-' || r == '_':
			builder.WriteByte('-')
		case r == ' ' || r == '.' || r == ',' || r == '\n' || r == '\t':
			builder.WriteByte('-')
		}
		if builder.Len() >= 40 {
			break
		}
	}
	slug := strings.Trim(builder.String(), "-")
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	if slug == "" {
		return "prompt"
	}
	return slug
}

func (t *localImageGenerateTool) imageGenerateConfig() ImageGenerateConfig {
	cfg := ImageGenerateConfig{}
	if t != nil && t.cfg != nil {
		cfg = t.cfg.ImageGenerate
	}
	if strings.TrimSpace(cfg.ModelName) == "" {
		cfg.ModelName = imageGenerateDefaultModelName
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = imageGenerateDefaultBaseURL
	}
	if cfg.TimeoutSec <= 0 {
		cfg.TimeoutSec = imageGenerateDefaultTimeoutSec
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = imageGenerateDefaultMaxRetries
	}
	if cfg.RetryBaseMs <= 0 {
		cfg.RetryBaseMs = imageGenerateDefaultRetryBaseMs
	}
	if strings.TrimSpace(cfg.AspectRatio) == "" {
		cfg.AspectRatio = imageGenerateDefaultAspectRatio
	}
	if cfg.MaxImages <= 0 {
		cfg.MaxImages = imageGenerateDefaultMaxImages
	}
	if cfg.MaxImageBytes <= 0 {
		cfg.MaxImageBytes = imageGenerateDefaultMaxImageBytes
	}
	return cfg
}
