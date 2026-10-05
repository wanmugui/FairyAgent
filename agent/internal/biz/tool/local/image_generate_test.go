package local

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const tinyPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl6nL8AAAAASUVORK5CYII="

func newImageGenerateTestTool(t *testing.T, handler http.HandlerFunc) (*localImageGenerateTool, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	tool, ok := NewLocalImageGenerateTool(ToolDef{}, &Config{ImageGenerate: ImageGenerateConfig{
		ModelName:     "image-01",
		BaseURL:       server.URL,
		APIKey:        "test-key",
		AspectRatio:   "16:9",
		TimeoutSec:    30,
		MaxRetries:    0,
		RetryBaseMs:   1,
		MaxImages:     4,
		MaxImageBytes: 4 << 20,
	}}).(*localImageGenerateTool)
	if !ok {
		t.Fatalf("unexpected tool type")
	}
	tool.client = server.Client()
	return tool, server
}

func TestImageGenerateSavesBase64Image(t *testing.T) {
	png, err := base64.StdEncoding.DecodeString(tinyPNGBase64)
	if err != nil {
		t.Fatal(err)
	}
	tool, server := newImageGenerateTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/image_generation" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("unexpected auth header: %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body["model"] != "image-01" {
			t.Errorf("unexpected model: %#v", body["model"])
		}
		if body["prompt"] != "a red apple on a white table" {
			t.Errorf("prompt was not forwarded verbatim: %#v", body["prompt"])
		}
		if body["response_format"] != "base64" {
			t.Errorf("expected base64 response_format: %#v", body["response_format"])
		}
		if body["prompt_optimizer"] != false {
			t.Errorf("prompt_optimizer should default to false: %#v", body["prompt_optimizer"])
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":   "gen-1",
			"data": map[string]any{"image_base64": []string{base64.StdEncoding.EncodeToString(png)}},
			"metadata": map[string]any{
				"success_count": "1",
				"failed_count":  "0",
			},
			"base_resp": map[string]any{"status_code": 0, "status_msg": "success"},
		})
	})
	defer server.Close()

	workspace := t.TempDir()
	result, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: workspace,
		Args:      []byte(`{"prompt":"a red apple on a white table","aspect_ratio":"1:1","result_image_path":"assets/cover.png"}`),
	})
	if err != nil || result.IsError {
		t.Fatalf("image_generate failed: result=%#v err=%v", result, err)
	}
	if result.Value["model"] != "image-01" || result.Value["aspect_ratio"] != "1:1" {
		t.Fatalf("unexpected metadata: %#v", result.Value)
	}
	localPath, _ := result.Value["local_path"].(string)
	if localPath != filepath.Join(workspace, "assets", "cover.png") {
		t.Fatalf("unexpected output path: %q", localPath)
	}
	data, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("generated image is missing: %v", err)
	}
	if len(data) != len(png) {
		t.Fatalf("generated image bytes were not written verbatim: %d != %d", len(data), len(png))
	}
	results, _ := result.Value["results"].([]map[string]any)
	if len(results) != 1 || results[0]["content_type"] != "image/png" {
		t.Fatalf("unexpected results: %#v", result.Value["results"])
	}
}

func TestImageGenerateDownloadFalseSkipsWriting(t *testing.T) {
	tool, server := newImageGenerateTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":      map[string]any{"image_urls": []string{"https://cdn.example/generated.jpg"}},
			"base_resp": map[string]any{"status_code": 0, "status_msg": "success"},
		})
	})
	defer server.Close()

	workspace := t.TempDir()
	result, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: workspace,
		Args:      []byte(`{"prompt":"a blue circle","download":false,"result_image_path":"assets/out"}`),
	})
	if err != nil || result.IsError {
		t.Fatalf("image_generate failed: result=%#v err=%v", result, err)
	}
	urls, _ := result.Value["image_urls"].([]string)
	if len(urls) != 1 || urls[0] != "https://cdn.example/generated.jpg" {
		t.Fatalf("unexpected image urls: %#v", result.Value["image_urls"])
	}
	if _, err := os.Stat(filepath.Join(workspace, "assets")); !os.IsNotExist(err) {
		t.Fatalf("download=false created output directory: %v", err)
	}
}

func TestImageGenerateSurfacesProviderError(t *testing.T) {
	tool, server := newImageGenerateTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":      nil,
			"base_resp": map[string]any{"status_code": 2013, "status_msg": "invalid params, unsupported model"},
		})
	})
	defer server.Close()

	result, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: t.TempDir(),
		Args:      []byte(`{"prompt":"a red apple"}`),
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected provider error to surface, got %#v", result.Value)
	}
	message, _ := result.Value["error"].(string)
	if message == "" {
		t.Fatalf("missing error message: %#v", result.Value)
	}
}

func TestImageGenerateRequiresPrompt(t *testing.T) {
	tool, server := newImageGenerateTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("provider should not be called without a prompt")
	})
	defer server.Close()

	result, _ := tool.Execute(context.Background(), ToolInvocation{Workspace: t.TempDir(), Args: []byte(`{}`)})
	if !result.IsError {
		t.Fatalf("expected error for missing prompt, got %#v", result.Value)
	}
}

func TestNormalizeImageAspectRatio(t *testing.T) {
	cases := map[string]string{
		"":          "",
		"16:9":      "16:9",
		"landscape": "16:9",
		"portrait":  "9:16",
		"square":    "1:1",
		"1024x1024": "1:1",
		"1920x1080": "16:9",
		"1080x1920": "9:16",
		"1000x1000": "1:1",
	}
	for input, want := range cases {
		got, err := normalizeImageAspectRatio(input)
		if err != nil {
			t.Fatalf("normalizeImageAspectRatio(%q) returned error: %v", input, err)
		}
		if got != want {
			t.Fatalf("normalizeImageAspectRatio(%q) = %q, want %q", input, got, want)
		}
	}
	if _, err := normalizeImageAspectRatio("ultrawide-ish"); err == nil {
		t.Fatalf("expected an error for an unsupported ratio")
	}
}

// Reference images are forwarded to the provider as subject_reference. This
// path used to be documented as unsupported even though the provider accepts
// it, which pushed callers to hand-roll one-off scripts. Guard the wiring so
// the schema and the implementation cannot drift apart again.
func TestImageGenerateForwardsReferenceImages(t *testing.T) {
	png, err := base64.StdEncoding.DecodeString(tinyPNGBase64)
	if err != nil {
		t.Fatal(err)
	}
	refPath := filepath.Join(t.TempDir(), "ref.png")
	if err := os.WriteFile(refPath, png, 0o600); err != nil {
		t.Fatal(err)
	}
	seen := make(chan map[string]any, 1)
	tool, server := newImageGenerateTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		seen <- body
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":   "gen-ref",
			"data": map[string]any{"image_base64": []string{base64.StdEncoding.EncodeToString(png)}},
			"metadata": map[string]any{
				"success_count": "1",
				"failed_count":  "0",
			},
		})
	})
	defer server.Close()

	args := fmt.Sprintf(`{"prompt":"same character, front view","image_paths":[%q]}`, refPath)
	result, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: t.TempDir(),
		Args:      []byte(args),
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("execute returned an error result, so the provider was never called: %#v", result.Value)
	}

	var body map[string]any
	select {
	case body = <-seen:
	case <-time.After(3 * time.Second):
		t.Fatal("provider was never called; no request reached the test server")
	}
	raw, ok := body["subject_reference"]
	if !ok {
		t.Fatalf("subject_reference missing from request body: %#v", body)
	}
	refs, ok := raw.([]any)
	if !ok || len(refs) != 1 {
		t.Fatalf("expected 1 subject_reference entry, got %#v", raw)
	}
	ref := refs[0].(map[string]any)
	if ref["type"] != "character" {
		t.Errorf("type = %#v, want character", ref["type"])
	}
	if fid, ok := ref["fidelity"].(float64); !ok || fid != localImageGenerateRefFidelity {
		t.Errorf("fidelity = %#v, want %v", ref["fidelity"], localImageGenerateRefFidelity)
	}
	imgs, ok := ref["image"].([]any)
	if !ok || len(imgs) != 1 {
		t.Fatalf("expected 1 image entry, got %#v", ref["image"])
	}
	got, err := base64.StdEncoding.DecodeString(imgs[0].(string))
	if err != nil {
		t.Fatalf("image is not valid base64: %v", err)
	}
	if !bytes.Equal(got, png) {
		t.Errorf("decoded reference bytes differ from the file on disk (%d vs %d)", len(got), len(png))
	}
	if strings.HasPrefix(imgs[0].(string), "data:") {
		t.Errorf("reference image must be bare base64, not a data URI")
	}
}

func TestImageGenerateOmitsSubjectReferenceWithoutPaths(t *testing.T) {
	png, _ := base64.StdEncoding.DecodeString(tinyPNGBase64)
	seen := make(chan map[string]any, 1)
	tool, server := newImageGenerateTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		seen <- body
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":   "gen-plain",
			"data": map[string]any{"image_base64": []string{base64.StdEncoding.EncodeToString(png)}},
			"metadata": map[string]any{
				"success_count": "1",
				"failed_count":  "0",
			},
		})
	})
	defer server.Close()

	if _, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: t.TempDir(),
		Args:      []byte(`{"prompt":"a red apple"}`),
	}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if body := <-seen; body["subject_reference"] != nil {
		t.Errorf("plain text-to-image must not send subject_reference, got %#v", body["subject_reference"])
	}
}

func TestImageGenerateRejectsBadReferenceImage(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.png")
	unsupported := filepath.Join(t.TempDir(), "ref.tiff")
	if err := os.WriteFile(unsupported, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"missing": missing, "unsupported": unsupported} {
		t.Run(name, func(t *testing.T) {
			called := false
			tool, server := newImageGenerateTestTool(t, func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			})
			defer server.Close()
			args := fmt.Sprintf(`{"prompt":"x","image_paths":[%q]}`, path)
			result, _ := tool.Execute(context.Background(), ToolInvocation{
				Workspace: t.TempDir(),
				Args:      []byte(args),
			})
			if !result.IsError {
				t.Fatalf("expected an error result for %s reference, got %#v", name, result)
			}
			if called {
				t.Errorf("must not hit the provider when the reference is unusable (%s)", name)
			}
		})
	}
}
