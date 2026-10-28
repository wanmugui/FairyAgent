package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindRepoRootPrefersExplicitEnvironmentRoot(t *testing.T) {
	want := filepath.Clean(t.TempDir())
	t.Setenv("AGENT_REPO_ROOT", want)

	if got := findRepoRoot(""); got != want {
		t.Fatalf("unexpected repo root: got %q want %q", got, want)
	}
}

func TestPreparePPTDeckWorkspaceCreatesConfiguredLocalDeck(t *testing.T) {
	repoRoot := t.TempDir()
	cfg := &Config{RepoRoot: repoRoot, WorkspaceDir: "workspace"}
	prompt := `<ppt_config><ppt_mode>no-template</ppt_mode><deck_dir>/mnt/data/result/pptid_demo</deck_dir></ppt_config>`

	if _, err := preparePPTDeckWorkspace(cfg, prompt); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(repoRoot, "workspace", "result", "pptid_demo")
	info, err := os.Stat(want)
	if err != nil || !info.IsDir() {
		t.Fatalf("deck directory was not created: %q err=%v", want, err)
	}
}

func TestPreparePPTDeckWorkspaceRejectsHostPath(t *testing.T) {
	cfg := &Config{RepoRoot: t.TempDir(), WorkspaceDir: "workspace"}
	prompt := `<ppt_config><deck_dir>/tmp/not-a-deck</deck_dir></ppt_config>`

	if _, err := preparePPTDeckWorkspace(cfg, prompt); err == nil {
		t.Fatal("expected host path to be rejected")
	}
}
