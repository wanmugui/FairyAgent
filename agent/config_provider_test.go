package main

// A model derived from a provider must be selectable and must take its
// connection details from that provider.
//
// This is the seam between the settings panel (which writes a model entry
// that only names a provider) and the agent (which has to turn that into a
// usable base url, key, dialect and model id).

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSelectModelResolvesProvider(t *testing.T) {
	cfg := &Config{
		RepoRoot: t.TempDir(),
		Models: map[string]ModelProfile{
			"claude-sonnet": {
				Display:    "Claude Sonnet",
				ProviderID: "anthropic",
				ModelID:    "claude-sonnet-4-5",
			},
		},
		Providers: map[string]ProviderConfig{
			"anthropic": {
				ID:               "anthropic",
				BaseURL:          "https://api.anthropic.com/v1",
				Format:           DialectAnthropicMessages,
				OutputLimit:      4096,
				APIKeyRef:        "READ_FROM_LOCAL_SECRETS:anthropic",
				Models:           []string{"claude-sonnet"},
			},
		},
	}
	if err := cfg.SelectModel("claude-sonnet"); err != nil {
		t.Fatalf("select: %v", err)
	}
	if cfg.API.BaseURL != "https://api.anthropic.com/v1" {
		t.Fatalf("base url not taken from provider: %q", cfg.API.BaseURL)
	}
	if cfg.API.Format != DialectAnthropicMessages {
		t.Fatalf("dialect not taken from provider: %q", cfg.API.Format)
	}
	if cfg.API.MaxTokens != 4096 {
		t.Fatalf("output limit not taken from provider: %d", cfg.API.MaxTokens)
	}
	if cfg.API.Model != "claude-sonnet-4-5" {
		t.Fatalf("model id not taken from the model entry: %q", cfg.API.Model)
	}
	if cfg.SelectedModelID != "claude-sonnet" {
		t.Fatalf("selected id: %q", cfg.SelectedModelID)
	}
}

func TestSelectModelProviderMustExist(t *testing.T) {
	cfg := &Config{
		RepoRoot: t.TempDir(),
		Models: map[string]ModelProfile{
			"x": {ProviderID: "missing"},
		},
	}
	if err := cfg.SelectModel("x"); err == nil {
		t.Fatal("a model naming an unknown provider must be rejected")
	}
}

// The legacy "provider" field is an ownership tag, not a reference. A model
// whose upstream name differs from its config key must keep that name.
func TestLegacyProviderFieldIsNotAReference(t *testing.T) {
	cfg := &Config{
		RepoRoot: t.TempDir(),
		Models: map[string]ModelProfile{
			"minimax-m3": {
				Display:  "MiniMax-M3",
				Provider: "minimax",
				API:      APIConfig{BaseURL: "https://api.minimaxi.com/v1", Model: "MiniMax-M3"},
			},
		},
		Providers: map[string]ProviderConfig{
			"minimax": {ID: "minimax", BaseURL: "https://other.example/v1"},
		},
	}
	if err := cfg.SelectModel("minimax-m3"); err != nil {
		t.Fatalf("select: %v", err)
	}
	if cfg.API.Model != "MiniMax-M3" {
		t.Fatalf("model name must not be replaced by the config key: %q", cfg.API.Model)
	}
	if cfg.API.BaseURL != "https://api.minimaxi.com/v1" {
		t.Fatalf("base url must stay with the model: %q", cfg.API.BaseURL)
	}
}

func TestReadLocalSecret(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	secrets := `{"anthropic":"sk-ant-value","empty":""}`
	if err := os.WriteFile(filepath.Join(root, "config", "local_secrets.json"), []byte(secrets), 0o600); err != nil {
		t.Fatal(err)
	}

	got, ok := readLocalSecret(root, "READ_FROM_LOCAL_SECRETS:anthropic")
	if !ok || got != "sk-ant-value" {
		t.Fatalf("secret not read: %q ok=%v", got, ok)
	}
	if _, ok := readLocalSecret(root, "READ_FROM_LOCAL_SECRETS:nope"); ok {
		t.Fatal("unknown secret must not resolve")
	}
	if _, ok := readLocalSecret(root, "READ_FROM_DEEPSEEK_KEY_TXT"); ok {
		t.Fatal("a non-local-secrets placeholder must not resolve here")
	}
	// An empty secret is not a usable key.
	if _, ok := readLocalSecret(root, "READ_FROM_LOCAL_SECRETS:empty"); ok {
		t.Fatal("empty secret must not resolve")
	}
}

// Selecting a derived model must also resolve the key from the local
// secrets file, otherwise the request goes out without credentials.
func TestSelectModelResolvesProviderKey(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "local_secrets.json"),
		[]byte(`{"anthropic":"sk-ant-real"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		RepoRoot: root,
		Models: map[string]ModelProfile{
			"claude": {ProviderID: "anthropic", ModelID: "claude-sonnet-4-5"},
		},
		Providers: map[string]ProviderConfig{
			"anthropic": {
				ID:        "anthropic",
				BaseURL:   "https://api.anthropic.com/v1",
				Format:    DialectAnthropicMessages,
				APIKeyRef: "READ_FROM_LOCAL_SECRETS:anthropic",
			},
		},
	}
	if err := cfg.SelectModel("claude"); err != nil {
		t.Fatalf("select: %v", err)
	}
	if cfg.API.APIKey != "sk-ant-real" {
		t.Fatalf("key not resolved from local secrets: %q", cfg.API.APIKey)
	}
}
