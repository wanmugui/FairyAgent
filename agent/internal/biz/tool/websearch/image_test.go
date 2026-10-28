package websearch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"agentloop/agent/internal/dtypes"
)

func TestParseBingImagesHTML(t *testing.T) {
	meta := map[string]string{
		"murl": "https://images.example.com/photo.jpg",
		"turl": "https://thumbs.example.com/photo.jpg",
		"t":    "Example photo",
		"purl": "https://page.example.com/article",
		"mw":   "1200",
		"mh":   "800",
	}
	rawMeta, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	body := `<html><body><a class="iusc" m="` + html.EscapeString(string(rawMeta)) + `"></a></body></html>`
	hits := parseBingImagesHTML(body, 5)
	if len(hits) != 1 {
		t.Fatalf("expected one hit, got %#v", hits)
	}
	if hits[0].ImageURL != meta["murl"] || hits[0].SourceURL != meta["purl"] || hits[0].Width != 1200 || hits[0].Height != 800 {
		t.Fatalf("unexpected parsed hit: %#v", hits[0])
	}
}

// fakeImageProvider lets the tests exercise the priority walk without hitting
// the public image search engines.
type fakeImageProvider struct {
	name  string
	hits  []imageSearchHit
	err   error
	calls int
	lastN int
}

func (p *fakeImageProvider) Name() string { return p.name }

func (p *fakeImageProvider) Search(_ context.Context, _ string, n int) ([]imageSearchHit, error) {
	p.calls++
	p.lastN = n
	if p.err != nil {
		return nil, p.err
	}
	if n > 0 && n < len(p.hits) {
		return p.hits[:n], nil
	}
	return p.hits, nil
}

func newFakeImageTool(providers ...*fakeImageProvider) *ImageTool {
	tool := NewImageTool(dtypes.ToolDef{})
	tool.providers = make(map[string]imageProvider, len(providers))
	priority := make([]string, 0, len(providers))
	for _, provider := range providers {
		tool.providers[provider.name] = provider
		priority = append(priority, provider.name)
	}
	tool.sourcePriority = priority
	return tool
}

func TestImageSearchReturnsURLsAndDownloadsImages(t *testing.T) {
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl6nL8AAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/asset.png" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	}))
	defer server.Close()

	workspace := t.TempDir()
	provider := &fakeImageProvider{name: "fake", hits: []imageSearchHit{{
		Title:     "Pixel",
		ImageURL:  server.URL + "/asset.png",
		SourceURL: "https://source.example/page",
	}}}
	tool := newFakeImageTool(provider)

	result, err := tool.Execute(context.Background(), dtypes.ToolInvocation{
		Workspace: workspace,
		Args:      []byte(`{"query":"pixel","top_k":1,"result_image_path":"downloads"}`),
	})
	if err != nil || result.IsError {
		t.Fatalf("image_search failed: result=%#v err=%v", result, err)
	}
	results, ok := result.Value["results"].([]map[string]any)
	if !ok || len(results) != 1 {
		t.Fatalf("unexpected results: %#v", result.Value["results"])
	}
	if results[0]["provider"] != "fake" {
		t.Fatalf("missing provider attribution: %#v", results[0])
	}
	providers, _ := result.Value["providers"].([]string)
	if len(providers) != 1 || providers[0] != "fake" {
		t.Fatalf("unexpected provider list: %#v", result.Value["providers"])
	}
	localPath, _ := results[0]["local_path"].(string)
	if localPath == "" {
		t.Fatalf("image was not downloaded: %#v", results[0])
	}
	if _, err := os.Stat(localPath); err != nil {
		t.Fatalf("downloaded image is missing: %v", err)
	}
	if filepath.Dir(localPath) != filepath.Join(workspace, "downloads") {
		t.Fatalf("image saved outside requested directory: %s", localPath)
	}
}

func TestImageSearchDownloadFalseDoesNotCreateOutputDirectory(t *testing.T) {
	workspace := t.TempDir()
	provider := &fakeImageProvider{name: "fake", hits: []imageSearchHit{{
		Title:    "Photo",
		ImageURL: "https://images.example.com/photo.jpg",
	}}}
	tool := newFakeImageTool(provider)

	result, err := tool.Execute(context.Background(), dtypes.ToolInvocation{
		Workspace: workspace,
		Args:      []byte(`{"query":"pixel","top_k":1,"download":false,"result_image_path":"downloads"}`),
	})
	if err != nil || result.IsError {
		t.Fatalf("image_search failed: result=%#v err=%v", result, err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "downloads")); !os.IsNotExist(err) {
		t.Fatalf("download=false created output directory: %v", err)
	}
}

func TestImageSearchFallsBackWhenPrimaryProviderFails(t *testing.T) {
	primary := &fakeImageProvider{name: "primary", err: errors.New("primary blocked")}
	secondary := &fakeImageProvider{name: "secondary", hits: []imageSearchHit{{
		Title:    "Fallback",
		ImageURL: "https://img.example/secondary.png",
	}}}
	tool := newFakeImageTool(primary, secondary)

	result, err := tool.Execute(context.Background(), dtypes.ToolInvocation{
		Args: []byte(`{"query":"pixel","top_k":1,"download":false}`),
	})
	if err != nil || result.IsError {
		t.Fatalf("image_search failed: result=%#v err=%v", result, err)
	}
	results, _ := result.Value["results"].([]map[string]any)
	if len(results) != 1 || results[0]["provider"] != "secondary" {
		t.Fatalf("fallback provider was not used: %#v", result.Value["results"])
	}
	if primary.calls != 1 || secondary.calls != 1 {
		t.Fatalf("unexpected provider calls: primary=%d secondary=%d", primary.calls, secondary.calls)
	}
	errs, _ := result.Value["provider_errors"].([]string)
	if len(errs) != 1 {
		t.Fatalf("provider errors were not reported: %#v", result.Value["provider_errors"])
	}
}

func TestImageSearchContinuesDownPriorityUntilTopKSatisfied(t *testing.T) {
	first := &fakeImageProvider{name: "first", hits: []imageSearchHit{{
		Title:    "One",
		ImageURL: "https://img.example/one.png",
	}}}
	second := &fakeImageProvider{name: "second", hits: []imageSearchHit{{
		Title:    "Two",
		ImageURL: "https://img.example/two.png",
	}}}
	third := &fakeImageProvider{name: "third", hits: []imageSearchHit{{
		Title:    "Three",
		ImageURL: "https://img.example/three.png",
	}}}
	tool := newFakeImageTool(first, second, third)

	result, err := tool.Execute(context.Background(), dtypes.ToolInvocation{
		Args: []byte(`{"query":"pixel","top_k":2,"download":false}`),
	})
	if err != nil || result.IsError {
		t.Fatalf("image_search failed: result=%#v err=%v", result, err)
	}
	results, _ := result.Value["results"].([]map[string]any)
	if len(results) != 2 {
		t.Fatalf("expected top_k results, got %#v", result.Value["results"])
	}
	if second.lastN != 1 {
		t.Fatalf("second provider should be asked for the remaining slot, got n=%d", second.lastN)
	}
	if third.calls != 0 {
		t.Fatalf("search kept going after top_k was satisfied: third.calls=%d", third.calls)
	}
	providers, _ := result.Value["providers"].([]string)
	if len(providers) != 2 || providers[0] != "first" || providers[1] != "second" {
		t.Fatalf("unexpected provider usage: %#v", result.Value["providers"])
	}
}

func TestImageSearchSourceOverrideUsesRequestedOrder(t *testing.T) {
	first := &fakeImageProvider{name: "first", hits: []imageSearchHit{{
		Title:    "One",
		ImageURL: "https://img.example/one.png",
	}}}
	second := &fakeImageProvider{name: "second", hits: []imageSearchHit{{
		Title:    "Two",
		ImageURL: "https://img.example/two.png",
	}}}
	tool := newFakeImageTool(first, second)

	result, err := tool.Execute(context.Background(), dtypes.ToolInvocation{
		Args: []byte(`{"query":"pixel","top_k":1,"download":false,"sources":["second","first"]}`),
	})
	if err != nil || result.IsError {
		t.Fatalf("image_search failed: result=%#v err=%v", result, err)
	}
	results, _ := result.Value["results"].([]map[string]any)
	if len(results) != 1 || results[0]["provider"] != "second" {
		t.Fatalf("source override was ignored: %#v", result.Value["results"])
	}
	if first.calls != 0 {
		t.Fatalf("first provider should not be used when top_k is already satisfied")
	}
}

func TestNormalizeImageSourcePriorityHandlesAliases(t *testing.T) {
	providers := map[string]imageProvider{
		"bing":      &fakeImageProvider{name: "bing"},
		"baidu":     &fakeImageProvider{name: "baidu"},
		"wikimedia": &fakeImageProvider{name: "wikimedia"},
	}
	got := normalizeImageSourcePriority([]string{"Bing_Images", "wiki", "unknown", "baidu"}, providers)
	want := []string{"bing", "wikimedia", "baidu"}
	if len(got) != len(want) {
		t.Fatalf("unexpected priority: %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected priority: %#v", got)
		}
	}
	if fallback := normalizeImageSourcePriority(nil, providers); len(fallback) != 3 || fallback[0] != "bing" {
		t.Fatalf("unexpected default priority: %#v", fallback)
	}
}
