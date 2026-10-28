package local

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withTempHistory 把历史文件指到临时目录，避免污染真实数据。
func withTempHistory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "restart-history.jsonl")

	h := &restartHistory{path: p, now: func() time.Time { return time.Date(2026, 10, 6, 18, 0, 0, 0, time.UTC) }}
	old := defaultRestartHistory
	defaultRestartHistory = h
	t.Cleanup(func() { defaultRestartHistory = old })
	return p
}

func TestRestartHistoryRoundTrip(t *testing.T) {
	withTempHistory(t)

	RecordRestart(RestartRecord{
		Command:     `pkill -f "server.cjs 8100"`,
		MatchedDeny: "pkill",
		Impact:      "1 unfinished upload session",
		Note:        "user approved after being told 3.5GB at risk",
	})

	got := RecentRestarts(10)
	if len(got) != 1 {
		t.Fatalf("应读到 1 条，实际 %d", len(got))
	}
	if !strings.Contains(got[0].Command, "pkill") {
		t.Errorf("命令未正确落盘: %+v", got[0])
	}
	if got[0].Impact == "" || got[0].Note == "" {
		t.Errorf("影响与备注应一并记录: %+v", got[0])
	}
}

func TestRestartHistoryNewestFirst(t *testing.T) {
	withTempHistory(t)
	now := time.Date(2026, 10, 6, 18, 0, 0, 0, time.UTC)
	h := defaultRestartHistory
	for i := 0; i < 3; i++ {
		h.now = func() time.Time { return now.Add(time.Duration(i) * time.Minute) }
		RecordRestart(RestartRecord{Command: "cmd-" + string(rune('a'+i))})
	}
	got := RecentRestarts(10)
	if len(got) != 3 {
		t.Fatalf("应读到 3 条，实际 %d", len(got))
	}
	if got[0].Time.Before(got[2].Time) {
		t.Errorf("应按时间倒序，实际首条=%v 末条=%v", got[0].Time, got[2].Time)
	}
}

func TestRestartHistorySkipsCorruptLines(t *testing.T) {
	p := withTempHistory(t)
	if err := os.WriteFile(p, []byte(
		"{\"time\":\"2026-10-06T18:00:00Z\",\"command\":\"good-1\"}\n"+
			"this is not json\n"+
			"{\"time\":\"2026-10-06T18:05:00Z\",\"command\":\"good-2\"}\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	got := RecentRestarts(10)
	if len(got) != 2 {
		t.Fatalf("坏行应被跳过，只读出 2 条，实际 %d: %+v", len(got), got)
	}
}

func TestRestartHistoryMissingFileIsEmpty(t *testing.T) {
	withTempHistory(t)
	if got := RecentRestarts(10); len(got) != 0 {
		t.Errorf("文件不存在时应返回空，实际 %+v", got)
	}
	if s := RecentRestartSummary(10); s != "" {
		t.Errorf("空历史不应输出摘要，实际 %q", s)
	}
}

func TestRestartHistoryRespectsLimit(t *testing.T) {
	withTempHistory(t)
	now := time.Date(2026, 10, 6, 18, 0, 0, 0, time.UTC)
	h := defaultRestartHistory
	for i := 0; i < 5; i++ {
		h.now = func() time.Time { return now.Add(time.Duration(i) * time.Minute) }
		RecordRestart(RestartRecord{Command: "c"})
	}
	if got := RecentRestarts(3); len(got) != 3 {
		t.Errorf("limit=3 应只返回 3 条，实际 %d", len(got))
	}
}
