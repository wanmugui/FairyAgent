package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testSegmentedMemoryConfig(root string) *Config {
	cfg := &Config{
		RepoRoot:  root,
		MemoryDir: "memory",
		SegmentedMemory: SegmentedMemoryConfig{
			Enabled:             true,
			KeyExtraction:       "local",
			MaxSegmentChars:     18,
			MaxSegmentSteps:     4,
			MaxRootIndexEntries: 100,
			PromptKeyLimit:      100,
			KeyWaitTimeout:      1,
			MaxConcurrentKeys:   1,
		},
	}
	applySegmentedMemoryDefaults(cfg)
	return cfg
}

func TestTurnMemoryRecorderSegmentsStreamingReplyAndToolOperation(t *testing.T) {
	cfg := testSegmentedMemoryConfig(t.TempDir())
	recorder := NewTurnMemoryRecorder(cfg, filepath.Join(cfg.RepoRoot, "session.json"), false)
	if recorder == nil {
		t.Fatal("recorder is nil")
	}

	recorder.RecordUserRequest("请修复登录问题")
	reply := "先检查登录代码。然后定位刷新 token。最后补充失败重试。"
	recorder.AppendAssistantDelta(reply[:len("先检查登录代码。")], 1)
	recorder.AppendAssistantDelta(reply[len("先检查登录代码。"):], 1)
	recorder.EndStep(1)
	recorder.RecordToolCall(2, "read_file", `{"file_path":"auth.ts"}`)
	recorder.RecordToolResult(2, "read_file", "call_1", `{"content":"refresh token"}`)
	recorder.EndStep(2)
	recorder.Close()

	segmentDir := filepath.Join(cfg.RepoRoot, "memory", segmentedMemoryDir, segmentedMemorySessionsDir)
	segments := readTestSegments(t, segmentDir)
	kinds := map[string]int{}
	var replyParts, toolParts []string
	linked := false
	for _, segment := range segments {
		kinds[segment.Kind]++
		if segment.NextID != "" {
			linked = true
		}
		if segment.Kind == segmentKindAssistant {
			replyParts = append(replyParts, segment.Content)
		}
		if segment.Kind == segmentKindToolOperation {
			toolParts = append(toolParts, segment.Content)
		}
		for _, key := range segment.Keys {
			if !strings.Contains(segment.Content, key.Key) {
				t.Fatalf("key %q is not a literal substring of segment %s", key.Key, segment.ID)
			}
		}
	}
	if kinds[segmentKindUserRequest] != 1 {
		t.Fatalf("user request segments = %d, want 1", kinds[segmentKindUserRequest])
	}
	if kinds[segmentKindAssistant] < 2 {
		t.Fatalf("assistant reply should be split by max chars, got %d segment(s)", kinds[segmentKindAssistant])
	}
	if kinds[segmentKindToolOperation] < 1 {
		t.Fatalf("tool operation segment missing: %#v", kinds)
	}
	if !linked {
		t.Fatal("segment chain has no forward link")
	}
	if got := strings.Join(replyParts, ""); got != reply {
		t.Fatalf("assistant reply was not preserved across segments:\n got=%q\nwant=%q", got, reply)
	}
	if !strings.Contains(strings.Join(toolParts, ""), "refresh token") {
		t.Fatalf("tool result missing from operation segments: %q", strings.Join(toolParts, ""))
	}

	rootIndex := filepath.Join(cfg.RepoRoot, "memory", segmentedMemoryDir, segmentedMemoryIndexDir, "root.jsonl")
	if _, err := os.Stat(rootIndex); err != nil {
		t.Fatalf("root index not written: %v", err)
	}
}

func TestSegmentedMemoryIndexRotatesToVolume(t *testing.T) {
	cfg := testSegmentedMemoryConfig(t.TempDir())
	cfg.SegmentedMemory.MaxRootIndexEntries = 2
	root := filepath.Join(cfg.RepoRoot, "memory", segmentedMemoryDir)
	segment := &segmentedMemorySegment{
		ID:        "turn-s0001",
		TurnID:    "turn",
		Kind:      segmentKindUserRequest,
		KeyStatus: "fallback",
	}
	for i, key := range []string{"第一条记忆", "第二条记忆", "第三条记忆"} {
		if err := appendSegmentKeysToIndex(root, cfg, segment, []segmentedMemoryKey{{Key: key, Primary: true}}); err != nil {
			t.Fatalf("append key %d: %v", i, err)
		}
	}
	rootPath := filepath.Join(root, segmentedMemoryIndexDir, "root.jsonl")
	lines, err := readSegmentedIndexLines(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("root should be a volume pointer plus latest entry, got %d lines: %#v", len(lines), lines)
	}
	if !strings.Contains(lines[0], `"type":"volume"`) {
		t.Fatalf("first root entry should be a volume pointer: %s", lines[0])
	}
	volumeDir := filepath.Join(root, segmentedMemoryIndexDir, segmentedMemoryVolumeDir)
	entries, err := os.ReadDir(volumeDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one volume file, entries=%v err=%v", entries, err)
	}
	volumeLines, err := readSegmentedIndexLines(filepath.Join(volumeDir, entries[0].Name()))
	if err != nil || len(volumeLines) != 2 {
		t.Fatalf("volume should contain first two entries, lines=%d err=%v", len(volumeLines), err)
	}
}

func TestParseSegmentedMemoryKeysRejectsNonLiteralKey(t *testing.T) {
	content := "修改 auth.ts，并补充刷新 token 的失败重试。"
	keys, err := parseSegmentedMemoryKeys(`{"keys":["修改 auth.ts","不存在的概括"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("parse returned %d keys", len(keys))
	}
	if !strings.Contains(content, keys[0]) || strings.Contains(content, keys[1]) {
		t.Fatalf("unexpected parsed keys: %#v", keys)
	}
}

func TestParseSegmentedMemoryKeysToleratesMalformedModelOutput(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "trailing json values",
			raw:  `{"keys":["修改 auth.ts"]}` + "\n" + `{"keys":["补充失败重试"]}`,
			want: []string{"修改 auth.ts"},
		},
		{
			name: "unescaped newline in string",
			raw:  "{\"keys\":[\"修改 auth.ts\n补充刷新 token 的失败重试\"]}",
			want: []string{"修改 auth.ts\n补充刷新 token 的失败重试"},
		},
		{
			name: "unquoted object key",
			raw:  `{keys:["修改 auth.ts","补充失败重试"]}`,
			want: []string{"修改 auth.ts", "补充失败重试"},
		},
		{
			name: "prose around json",
			raw:  `结果如下：` + "\n" + `{"keys":["定位刷新 token"]}` + "\n" + `以上。`,
			want: []string{"定位刷新 token"},
		},
		{
			name: "single quoted payload",
			raw:  `{keys:['定位登录失败','补充重试']}`,
			want: []string{"定位登录失败", "补充重试"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSegmentedMemoryKeys(tt.raw)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("parsed %d keys, want %d: %#v", len(got), len(tt.want), got)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("key %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestBuildSegmentedMemoryPromptBlock(t *testing.T) {
	cfg := testSegmentedMemoryConfig(t.TempDir())
	cfg.SegmentedMemory.PromptKeyLimit = 1
	root := filepath.Join(cfg.RepoRoot, "memory", segmentedMemoryDir)
	segment := &segmentedMemorySegment{
		ID:            "s0001",
		SessionID:     "2026-09-18",
		InteractionID: "req-20260918-001",
		Kind:          segmentKindUserRequest,
		KeyStatus:     "llm",
	}
	if err := appendSegmentKeysToIndex(root, cfg, segment, []segmentedMemoryKey{{Key: "修复登录问题", Primary: true}}); err != nil {
		t.Fatal(err)
	}
	currentSessionSegment := &segmentedMemorySegment{
		ID:            "s0002",
		SessionID:     "2026-09-21",
		InteractionID: "req-20260921-001",
		Kind:          segmentKindAssistant,
		KeyStatus:     "llm",
	}
	if err := appendSegmentKeysToIndex(root, cfg, currentSessionSegment, []segmentedMemoryKey{{Key: "当前会话不应自动注入", Primary: true}}); err != nil {
		t.Fatal(err)
	}
	block := buildSegmentedMemoryPromptBlock(cfg, "2026-09-21")
	if !strings.Contains(block, `["修复登录问题"]`) || !strings.Contains(block, "memory_search") {
		t.Fatalf("segmented index block missing compact key list or search hint: %q", block)
	}
	if strings.Contains(block, "当前会话不应自动注入") {
		t.Fatalf("current-session key leaked into the system prompt: %q", block)
	}
	if strings.Contains(block, "memory://") {
		t.Fatalf("segmented index block must not expose backend paths: %q", block)
	}
}

func TestSegmentedMemoryKeyInstructionLoadsModule(t *testing.T) {
	cfg := testSegmentedMemoryConfig(t.TempDir())
	moduleDir := filepath.Join(cfg.RepoRoot, "config", "modules", "segmented_memory_key")
	if err := os.MkdirAll(moduleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := "只从原文中选择可定位的关键句。"
	if err := os.WriteFile(filepath.Join(moduleDir, "zh.md"), []byte(want+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := segmentedMemoryKeyInstruction(cfg); got != want {
		t.Fatalf("key instruction = %q, want %q", got, want)
	}
}

func readTestSegments(t *testing.T, root string) []segmentedMemorySegment {
	t.Helper()
	var out []segmentedMemorySegment
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".json" || filepath.Base(path) == segmentedMemoryManifestFile {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		var segment segmentedMemorySegment
		if jsonErr := json.Unmarshal(data, &segment); jsonErr != nil {
			return jsonErr
		}
		out = append(out, segment)
		return nil
	})
	if err != nil {
		t.Fatalf("walk segments: %v", err)
	}
	return out
}
