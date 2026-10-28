package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildSystemPromptForVoiceAppendsVoiceChannelContract(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "config", "modules", "channels", "voice"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "config", "system"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "config", "modules", "channels", "voice", "zh.md"),
		[]byte("VOICE_CHANNEL_CONTRACT"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "config", "system", "zh.md"),
		[]byte("BASE_SYSTEM_PROMPT"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{RepoRoot: root}
	got := BuildSystemPromptForSurface(cfg, "voice")

	if !strings.Contains(got, "BASE_SYSTEM_PROMPT") {
		t.Fatalf("base system prompt missing: %q", got)
	}
	if !strings.Contains(got, "VOICE_CHANNEL_CONTRACT") {
		t.Fatalf("voice channel contract missing: %q", got)
	}
	if strings.Index(got, "VOICE_CHANNEL_CONTRACT") < strings.Index(got, "BASE_SYSTEM_PROMPT") {
		t.Fatalf("voice channel contract must follow the base prompt: %q", got)
	}
}

func TestBuildSystemPromptForChatDoesNotAppendVoiceContract(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "config", "modules", "channels", "voice"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "config", "system"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "config", "modules", "channels", "voice", "zh.md"),
		[]byte("VOICE_CHANNEL_CONTRACT"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "config", "system", "zh.md"),
		[]byte("BASE_SYSTEM_PROMPT"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	got := BuildSystemPromptForSurface(&Config{RepoRoot: root}, "chat")

	if strings.Contains(got, "VOICE_CHANNEL_CONTRACT") {
		t.Fatalf("chat prompt must not receive the voice contract: %q", got)
	}
}
