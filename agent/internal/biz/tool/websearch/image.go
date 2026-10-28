package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	htmlstd "html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agentloop/agent/internal/biz/tool/shared"
	"agentloop/agent/internal/dtypes"
)

const (
	imageSearchDefaultTopK     = 5
	imageSearchMaxTopK         = 10
	imageSearchMaxPageBytes    = 4 << 20
	imageSearchMaxImageBytes   = 20 << 20
	imageSearchRequestTimeout  = 45 * time.Second
	imageSearchDownloadTimeout = 60 * time.Second
)

type ImageOptions struct {
	SourcePriority []string
	Timeout        time.Duration
	MaxImageBytes  int64
}

func DefaultImageOptions() ImageOptions {
	return ImageOptions{
		SourcePriority: []string{"bing", "baidu", "wikimedia"},
		Timeout:        imageSearchRequestTimeout,
		MaxImageBytes:  imageSearchMaxImageBytes,
	}
}

// ImageTool implements image_search locally by trying multiple public image
// search providers in priority order and optionally downloading the original
// image URLs. The returned shape keeps both image_url and local_path so asset
// pipelines can choose whether to download lazily.
type ImageTool struct {
	schema         dtypes.ToolDef
	client         *http.Client
	providers      map[string]imageProvider
	sourcePriority []string
	timeout        time.Duration
	maxImageBytes  int64
}

func NewImageTool(schema dtypes.ToolDef) *ImageTool {
	return NewImageToolWithClient(schema, defaultHTTPClient())
}

func NewImageToolWithClient(schema dtypes.ToolDef, client *http.Client) *ImageTool {
	return NewImageToolWithOptions(schema, DefaultImageOptions(), client)
}

func NewImageToolWithOptions(schema dtypes.ToolDef, options ImageOptions, clients ...*http.Client) *ImageTool {
	client := defaultHTTPClient()
	if len(clients) > 0 && clients[0] != nil {
		client = clients[0]
	}
	if client == nil {
		client = defaultHTTPClient()
	}
	providers := map[string]imageProvider{
		"bing":      newBingImageProvider(client),
		"baidu":     newBaiduImageProvider(client),
		"wikimedia": newWikimediaImageProvider(client),
	}
	if options.Timeout <= 0 {
		options.Timeout = imageSearchRequestTimeout
	}
	if options.MaxImageBytes <= 0 {
		options.MaxImageBytes = imageSearchMaxImageBytes
	}
	priority := normalizeImageSourcePriority(options.SourcePriority, providers)
	return &ImageTool{
		schema:         schema,
		client:         client,
		providers:      providers,
		sourcePriority: priority,
		timeout:        options.Timeout,
		maxImageBytes:  options.MaxImageBytes,
	}
}

func normalizeImageSourcePriority(requested []string, providers map[string]imageProvider) []string {
	priority := make([]string, 0, len(providers))
	seen := make(map[string]bool)
	for _, raw := range requested {
		name := strings.ToLower(strings.TrimSpace(raw))
		switch name {
		case "wiki", "commons", "wikimedia_commons":
			name = "wikimedia"
		case "baidu_images":
			name = "baidu"
		case "bing_images":
			name = "bing"
		}
		if providers[name] == nil || seen[name] {
			continue
		}
		seen[name] = true
		priority = append(priority, name)
	}
	if len(priority) == 0 {
		for _, name := range []string{"bing", "baidu", "wikimedia"} {
			if providers[name] != nil {
				priority = append(priority, name)
			}
		}
	}
	return priority
}

func (t *ImageTool) Name() string { return "image_search" }

func (t *ImageTool) Schema() dtypes.ToolDef { return t.schema }

func (t *ImageTool) Execute(ctx context.Context, invocation dtypes.ToolInvocation) (dtypes.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return dtypes.ToolResult{}, err
	}
	args, err := shared.DecodeArgs(invocation)
	if err != nil {
		return shared.ErrorResult(t.Name(), err), nil
	}
	query := strings.TrimSpace(shared.StringArg(args, "query", "q"))
	if query == "" {
		return shared.ErrorResult(t.Name(), fmt.Errorf("query is required")), nil
	}
	topK := imageSearchIntArg(args, "top_k", imageSearchDefaultTopK)
	if topK < 1 {
		topK = 1
	}
	if topK > imageSearchMaxTopK {
		topK = imageSearchMaxTopK
	}
	download := true
	if _, ok := args["download"]; ok {
		download = imageSearchBoolArg(args["download"])
	}

	searchPriority := t.sourcePriority
	if overrides := imageSearchStringListArg(args, "sources", "source"); len(overrides) > 0 {
		if normalized := normalizeImageSourcePriority(overrides, t.providers); len(normalized) > 0 {
			searchPriority = normalized
		}
	}

	hits, providersUsed, providerErrors, err := t.search(ctx, query, topK, searchPriority)
	if err != nil {
		return shared.ErrorResult(t.Name(), err), nil
	}
	if len(hits) == 0 {
		return shared.ErrorResult(t.Name(), fmt.Errorf("image search returned no parseable results")), nil
	}

	plan := imageSearchOutput{}
	if download {
		workspace, err := imageSearchWorkspace(invocation)
		if err != nil {
			return shared.ErrorResult(t.Name(), err), nil
		}
		plan, err = imageSearchOutputPlan(workspace, shared.StringArg(args, "result_image_path"), query, len(hits))
		if err != nil {
			return shared.ErrorResult(t.Name(), err), nil
		}
	}

	results := make([]map[string]any, 0, len(hits))
	for index, hit := range hits {
		item := map[string]any{
			"title":         hit.Title,
			"image_url":     hit.ImageURL,
			"thumbnail_url": hit.ThumbnailURL,
			"source_url":    hit.SourceURL,
			"local_path":    nil,
		}
		if hit.Width > 0 {
			item["width"] = hit.Width
		}
		if hit.Height > 0 {
			item["height"] = hit.Height
		}
		if download {
			localPath, bytes, mediaType, downloadErr := t.download(ctx, hit, plan, index)
			if downloadErr != nil {
				item["download_error"] = downloadErr.Error()
			} else {
				item["local_path"] = localPath
				item["bytes"] = bytes
				item["content_type"] = mediaType
			}
		}
		item["provider"] = hit.Provider
		results = append(results, item)
	}

	return dtypes.ToolResult{Value: map[string]any{
		"tool":            t.Name(),
		"ok":              true,
		"query":           query,
		"count":           len(results),
		"download":        download,
		"providers":       providersUsed,
		"provider_errors": providerErrors,
		"results":         results,
	}}, nil
}

type imageSearchHit struct {
	Title        string `json:"title"`
	ImageURL     string `json:"image_url"`
	ThumbnailURL string `json:"thumbnail_url"`
	SourceURL    string `json:"source_url"`
	Provider     string `json:"provider"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
}

// search walks the provider priority list, accumulating de-duplicated hits
// until top_k is satisfied. A provider that errors is skipped so the next one
// still gets a chance; the collected errors are reported alongside the hits.
func (t *ImageTool) search(ctx context.Context, query string, n int, priority []string) ([]imageSearchHit, []string, []string, error) {
	hits := make([]imageSearchHit, 0, n)
	seen := make(map[string]bool)
	providersUsed := make([]string, 0, len(priority))
	providerErrors := make([]string, 0)
	for _, source := range priority {
		provider := t.providers[source]
		if provider == nil {
			continue
		}
		remaining := n - len(hits)
		if remaining <= 0 {
			break
		}
		providersUsed = append(providersUsed, source)
		providerCtx, cancel := context.WithTimeout(ctx, t.timeout)
		providerHits, err := provider.Search(providerCtx, query, remaining)
		cancel()
		if err != nil {
			providerErrors = append(providerErrors, source+": "+err.Error())
			continue
		}
		for _, hit := range providerHits {
			hit.Provider = source
			imageURL := CanonicalizeURL(hit.ImageURL)
			if !isHTTPURLValue(imageURL) || seen[imageURL] {
				continue
			}
			seen[imageURL] = true
			hit.ImageURL = imageURL
			hits = append(hits, hit)
			if len(hits) >= n {
				break
			}
		}
	}
	if len(hits) == 0 && len(providerErrors) > 0 {
		return nil, providersUsed, providerErrors, fmt.Errorf("image search failed across providers: %s", strings.Join(providerErrors, "; "))
	}
	return hits, providersUsed, providerErrors, nil
}

func parseBingImagesHTML(body string, n int) []imageSearchHit {
	hits := make([]imageSearchHit, 0, n)
	seen := make(map[string]bool)
	lower := strings.ToLower(body)
	cursor := 0
	for cursor < len(body) {
		startRel := strings.Index(lower[cursor:], "<a")
		if startRel < 0 {
			break
		}
		start := cursor + startRel
		if start+2 >= len(body) {
			break
		}
		next := body[start+2]
		if next != ' ' && next != '\t' && next != '\n' && next != '\r' && next != '>' {
			cursor = start + 2
			continue
		}
		tagEnd := indexOfUnquoted(body, start, '>')
		if tagEnd < 0 {
			break
		}
		tag := body[start:tagEnd]
		if !hasClassToken(attrValue(tag, "class"), "iusc") {
			cursor = tagEnd + 1
			continue
		}
		rawMeta := htmlstd.UnescapeString(attrValue(tag, "m"))
		var meta struct {
			ImageURL     string          `json:"murl"`
			ThumbnailURL string          `json:"turl"`
			Title        string          `json:"t"`
			SourceURL    string          `json:"purl"`
			Width        json.RawMessage `json:"mw"`
			Height       json.RawMessage `json:"mh"`
		}
		if rawMeta != "" && json.Unmarshal([]byte(rawMeta), &meta) == nil {
			imageURL := CanonicalizeURL(meta.ImageURL)
			if isHTTPURLValue(imageURL) && !seen[imageURL] {
				seen[imageURL] = true
				hits = append(hits, imageSearchHit{
					Title:        strings.TrimSpace(meta.Title),
					ImageURL:     imageURL,
					ThumbnailURL: CanonicalizeURL(meta.ThumbnailURL),
					SourceURL:    CanonicalizeURL(meta.SourceURL),
					Width:        imageSearchJSONInt(meta.Width),
					Height:       imageSearchJSONInt(meta.Height),
				})
			}
		}
		cursor = tagEnd + 1
		if len(hits) >= n {
			break
		}
	}
	return hits
}

type imageSearchOutput struct {
	dir       string
	prefix    string
	exactPath string
}

func imageSearchOutputPlan(workspace, requested, query string, count int) (imageSearchOutput, error) {
	if strings.TrimSpace(requested) == "" {
		dir := filepath.Join(workspace, "result", "image-search", imageSearchSlug(query)+"-"+time.Now().Format("20060102-150405"))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return imageSearchOutput{}, fmt.Errorf("create image search directory: %w", err)
		}
		return imageSearchOutput{dir: dir, prefix: "image"}, nil
	}
	resolved, err := shared.ResolveWorkspacePath(workspace, requested)
	if err != nil {
		return imageSearchOutput{}, err
	}
	ext := imageSearchFileExtension(filepath.Ext(resolved))
	if ext != "" && count == 1 {
		if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
			return imageSearchOutput{}, fmt.Errorf("create image output directory: %w", err)
		}
		return imageSearchOutput{dir: filepath.Dir(resolved), prefix: strings.TrimSuffix(filepath.Base(resolved), ext), exactPath: resolved}, nil
	}
	dir := resolved
	prefix := "image"
	if ext != "" {
		dir = filepath.Dir(resolved)
		prefix = strings.TrimSuffix(filepath.Base(resolved), ext)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return imageSearchOutput{}, fmt.Errorf("create image output directory: %w", err)
	}
	return imageSearchOutput{dir: dir, prefix: prefix}, nil
}

func (t *ImageTool) download(ctx context.Context, hit imageSearchHit, plan imageSearchOutput, index int) (string, int, string, error) {
	imageURL := hit.ImageURL
	if !isHTTPURLValue(imageURL) {
		return "", 0, "", fmt.Errorf("unsupported image URL %q", imageURL)
	}
	requestCtx, cancel := context.WithTimeout(ctx, imageSearchDownloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, imageURL, nil)
	if err != nil {
		return "", 0, "", fmt.Errorf("build image download request: %w", err)
	}
	req.Header.Set("User-Agent", defaultUserAgent)
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/*,*/*;q=0.8")
	referer := strings.TrimSpace(hit.SourceURL)
	if referer == "" {
		referer = "https://www.bing.com/"
	}
	req.Header.Set("Referer", referer)

	resp, err := t.client.Do(req)
	if err != nil {
		return "", 0, "", fmt.Errorf("download image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", 0, "", fmt.Errorf("image download returned status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, t.maxImageBytes+1))
	if err != nil {
		return "", 0, "", fmt.Errorf("read image: %w", err)
	}
	if int64(len(data)) > t.maxImageBytes {
		return "", 0, "", fmt.Errorf("image exceeds maximum size of %d bytes", t.maxImageBytes)
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
	if !strings.HasPrefix(mediaType, "image/") {
		mediaType = strings.ToLower(strings.TrimSpace(strings.Split(http.DetectContentType(data), ";")[0]))
	}
	ext := imageSearchFileExtensionForMediaType(mediaType)
	if ext == "" {
		return "", 0, "", fmt.Errorf("downloaded content is not a supported image (%s)", mediaType)
	}
	target := plan.exactPath
	if target == "" {
		target = filepath.Join(plan.dir, fmt.Sprintf("%s-%02d%s", plan.prefix, index+1, ext))
	}
	tmp, err := os.CreateTemp(plan.dir, ".image-search-*")
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

func imageSearchWorkspace(invocation dtypes.ToolInvocation) (string, error) {
	workspace := strings.TrimSpace(invocation.Workspace)
	if workspace == "" {
		return "", fmt.Errorf("workspace is required")
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	return filepath.Clean(abs), nil
}

// imageSearchStringListArg accepts either a JSON array or a delimited string so
// callers can override the configured search source order per call.
func imageSearchStringListArg(args map[string]any, keys ...string) []string {
	for _, key := range keys {
		value, ok := args[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case []string:
			return trimImageSearchStrings(typed)
		case []any:
			items := make([]string, 0, len(typed))
			for _, item := range typed {
				if text, ok := item.(string); ok {
					items = append(items, text)
				}
			}
			if trimmed := trimImageSearchStrings(items); len(trimmed) > 0 {
				return trimmed
			}
		case string:
			if trimmed := trimImageSearchStrings(strings.FieldsFunc(typed, func(r rune) bool {
				return r == ',' || r == ';' || r == '|'
			})); len(trimmed) > 0 {
				return trimmed
			}
		}
	}
	return nil
}

func trimImageSearchStrings(values []string) []string {
	trimmed := make([]string, 0, len(values))
	for _, value := range values {
		if item := strings.TrimSpace(value); item != "" {
			trimmed = append(trimmed, item)
		}
	}
	return trimmed
}

func imageSearchIntArg(args map[string]any, key string, fallback int) int {
	value, ok := args[key]
	if !ok {
		return fallback
	}
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil {
			return int(parsed)
		}
	}
	return fallback
}

func imageSearchBoolArg(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off", "":
			return false
		}
	case float64:
		return typed != 0
	case int:
		return typed != 0
	}
	return false
}

func imageSearchJSONInt(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		if parsed, err := number.Int64(); err == nil {
			return int(parsed)
		}
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		var parsed int
		if _, err := fmt.Sscanf(text, "%d", &parsed); err == nil {
			return parsed
		}
	}
	return 0
}

func imageSearchSlug(query string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(query)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '-' || r == '_':
			builder.WriteByte('-')
		case r == ' ' || r == '.':
			builder.WriteByte('-')
		}
		if builder.Len() >= 40 {
			break
		}
	}
	slug := strings.Trim(builder.String(), "-")
	if slug == "" {
		return "query"
	}
	return slug
}

func imageSearchFileExtension(ext string) string {
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

func imageSearchFileExtensionForMediaType(mediaType string) string {
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

func isHTTPURLValue(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}
