package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssembleSystemPromptUsesManifestOrder(t *testing.T) {
	root := t.TempDir()
	partsDir := filepath.Join(root, "parts", "zh")
	if err := os.MkdirAll(partsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partsDir, "02_core.md"), []byte("SECOND"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partsDir, "01_role.md"), []byte("FIRST"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := "zh:\n  - 01_role.md\n  - 02_core.md\n"
	if err := os.WriteFile(filepath.Join(root, "parts", "manifest.yml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{RepoRoot: root, SystemPartsDir: "parts/zh"}

	got := assembleSystemPrompt(cfg)
	if got != "FIRST\n\nSECOND" {
		t.Fatalf("unexpected assembled prompt: %q", got)
	}
}

func TestAssembleSystemPromptFallsBackToSortedFiles(t *testing.T) {
	root := t.TempDir()
	partsDir := filepath.Join(root, "parts", "zh")
	if err := os.MkdirAll(partsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partsDir, "b.md"), []byte("B"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partsDir, "a.md"), []byte("A"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{RepoRoot: root, SystemPartsDir: "parts/zh"}

	got := assembleSystemPrompt(cfg)
	if got != "A\n\nB" {
		t.Fatalf("unexpected fallback order: %q", got)
	}
	if !strings.Contains(got, "A") || !strings.Contains(got, "B") {
		t.Fatalf("assembled prompt missing content: %q", got)
	}
}

func TestSystemPromptManifestNames(t *testing.T) {
	manifest := "zh:\n  - 01_role.md\n  - 02_core.md\n\nen:\n  - en_a.md\n"
	names := systemPromptManifestNames(manifest, "zh")
	if len(names) != 2 || names[0] != "01_role.md" || names[1] != "02_core.md" {
		t.Fatalf("unexpected manifest names: %#v", names)
	}
}

func TestSystemTemplateEnablesReportProtocol(t *testing.T) {
	cfg := &Config{RepoRoot: t.TempDir()}
	vars := systemTemplateVars(cfg, "[]")
	if enabled, ok := vars["enable_report"].(bool); !ok || !enabled {
		t.Fatalf("enable_report must be true: %#v", vars["enable_report"])
	}
}

func TestRepoSystemPromptRendersReportProtocol(t *testing.T) {
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(repoRoot, "config/config.json")
	if err != nil {
		t.Fatal(err)
	}
	got := BuildSystemPrompt(cfg)
	if !strings.Contains(got, "server 不会补标签、补正文") {
		t.Fatalf("report protocol was not rendered")
	}
	if strings.Contains(got, "不使用 `<report>`") {
		t.Fatalf("report protocol fell through to the disabled branch")
	}
}

func TestBuildSubtaskSystemPromptUsesWorkerContract(t *testing.T) {
	root := t.TempDir()
	templatePath := filepath.Join(root, "subtask-system.md")
	template := "# worker contract\n\nroot={{ REPO_ROOT }}\n{% if enable_skill_registry %}SKILLS{% endif %}\n"
	if err := os.WriteFile(templatePath, []byte(template), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{RepoRoot: root}
	cfg.Prompts.SubtaskSystemPath = "subtask-system.md"

	got := BuildSubtaskSystemPrompt(cfg)
	if !strings.Contains(got, "worker contract") || !strings.Contains(got, root) {
		t.Fatalf("subtask system prompt was not rendered: %q", got)
	}
	if strings.Contains(got, "SKILLS") {
		t.Fatalf("subtask contract must not embed the skill registry: %q", got)
	}
}

func TestRenderLocalSubtaskPromptUsesUserTemplate(t *testing.T) {
	root := t.TempDir()
	userPath := filepath.Join(root, "subtask-user.md")
	template := "TASK: {{ Task|safe }}\n{% if enable_skill_registry %}SKILLS{% endif %}\n"
	if err := os.WriteFile(userPath, []byte(template), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{RepoRoot: root}
	cfg.Prompts.SubtaskUserPath = "subtask-user.md"

	got, err := renderLocalSubtaskPrompt(cfg, "review the evidence")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "review the evidence") {
		t.Fatalf("delegated task missing from the user prompt: %q", got)
	}
	if strings.Contains(got, "<subtask_result>") {
		t.Fatalf("user prompt must not carry the result contract: %q", got)
	}
}

func TestRepoSubtaskPromptsSplitContractAndTask(t *testing.T) {
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(repoRoot, "config/config.json")
	if err != nil {
		t.Fatal(err)
	}
	system := BuildSubtaskSystemPrompt(cfg)
	if !strings.Contains(system, "子任务执行契约") || !strings.Contains(system, "<subtask_result>") {
		t.Fatalf("subtask system prompt missing contract: %q", system[:min(200, len(system))])
	}
	user, err := renderLocalSubtaskPrompt(cfg, "verify the report")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(user, "verify the report") {
		t.Fatalf("subtask user prompt missing the work package: %q", user)
	}
	if strings.Contains(user, "子任务执行契约") {
		t.Fatalf("contract leaked into the subtask user prompt: %q", user)
	}
}
