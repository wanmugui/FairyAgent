package protectedassets

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeTestBundle(t *testing.T, repoRoot string, files map[string]string) string {
	t.Helper()
	contents := make(map[string][]byte, len(files))
	for name, content := range files {
		contents[name] = []byte(content)
	}
	raw, err := EncodeBundle(contents)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repoRoot, filepath.FromSlash(BundleRelPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadFileForPathPrefersPlaintext(t *testing.T) {
	repo := t.TempDir()
	plain := filepath.Join(repo, "config", "system", "zh.md")
	if err := os.MkdirAll(filepath.Dir(plain), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plain, []byte("plaintext prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestBundle(t, repo, map[string]string{
		"config/system/zh.md": "encrypted prompt",
	})

	got, err := ReadFileForPath(plain)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "plaintext prompt" {
		t.Fatalf("got %q", got)
	}
}

func TestReadFileForPathFallsBackToBundle(t *testing.T) {
	repo := t.TempDir()
	writeTestBundle(t, repo, map[string]string{
		"config/system/zh.md": "encrypted prompt",
	})

	got, err := ReadFileForPath(filepath.Join(repo, "config", "system", "zh.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "encrypted prompt" {
		t.Fatalf("got %q", got)
	}
}

func TestLoadBundleRejectsCorruptCiphertext(t *testing.T) {
	repo := t.TempDir()
	path := writeTestBundle(t, repo, map[string]string{"config/system/zh.md": "prompt"})
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xff
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = LoadBundle(repo)
	if err == nil {
		t.Fatal("expected authentication failure")
	}
}

func TestReadFileForPathReportsMissingEntry(t *testing.T) {
	repo := t.TempDir()
	writeTestBundle(t, repo, map[string]string{
		"config/system/zh.md": "encrypted prompt",
	})

	_, err := ReadFileForPath(filepath.Join(repo, "config", "modules", "summary", "zh.md"))
	if err == nil {
		t.Fatal("expected missing entry error")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing bundle entry should not be reported as a missing file: %v", err)
	}
}

func TestRepositoryBundleDecodesWhenPresent(t *testing.T) {
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(repo, filepath.FromSlash(BundleRelPath))
	if _, err := os.Stat(bundlePath); errors.Is(err, os.ErrNotExist) {
		t.Skip("repository does not contain a protected bundle")
	} else if err != nil {
		t.Fatal(err)
	}
	bundle, err := LoadBundle(repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"config/system/zh.md",
		"config/modules/summary/zh.md",
		"config/tools/schemas.json",
	} {
		data, ok := bundle.Read(name)
		if !ok {
			t.Fatalf("protected bundle is missing %s", name)
		}
		if len(bytes.TrimSpace(data)) == 0 {
			t.Fatalf("protected bundle entry %s is empty", name)
		}
	}
	systemPrompt, _ := bundle.Read("config/system/zh.md")
	if !bytes.Contains(systemPrompt, []byte("Today:")) {
		t.Fatal("protected system prompt is not an assembled prompt")
	}
	if _, err := os.Stat(filepath.Join(repo, "config", "system", "zh.md")); err == nil {
		t.Log("plaintext prompt still present; bundle will become the fallback after packaging")
	}
}
