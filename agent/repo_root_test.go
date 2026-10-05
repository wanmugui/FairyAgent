package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindRepoRoot(t *testing.T) {
	orig := os.Getenv("AGENT_REPO_ROOT")
	defer os.Setenv("AGENT_REPO_ROOT", orig)

	t.Run("env override wins", func(t *testing.T) {
		os.Setenv("AGENT_REPO_ROOT", `C:\fake\override`)
		if got := findRepoRoot(""); got != filepath.Clean(`C:\fake\override`) {
			t.Fatalf("override: got %q", got)
		}
	})

	os.Unsetenv("AGENT_REPO_ROOT")

	t.Run("absolute config path derives root", func(t *testing.T) {
		root := t.TempDir()
		cfgDir := filepath.Join(root, "config")
		if err := os.MkdirAll(cfgDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		cfgPath := filepath.Join(cfgDir, "variant.json")
		if got := findRepoRoot(cfgPath); filepath.Clean(got) != filepath.Clean(root) {
			t.Fatalf("config-derived: got %q want %q", got, root)
		}
	})

	t.Run("cwd walk-up finds repo marker", func(t *testing.T) {
		got := findRepoRoot("")
		if got == "" {
			t.Fatal("fallback returned empty")
		}
		if _, err := os.Stat(filepath.Join(got, "config", "config.json")); err != nil {
			t.Fatalf("resolved root %q lacks config/config.json", got)
		}
	})
}
