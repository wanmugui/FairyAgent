package local

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// RestartRecord 记录一次「被授权执行」的高危命令。
//
// 【为什么需要它】今天的事故暴露了一个盲区：模型在动手前
// 完全不知道「这个服务刚重启过」「主人正在用它」。
// 把这些事实沉淀成可查询的历史，下次动手前就能先查一遍。
//
// 只记录**实际执行**的（即走了升级授权放行的），
// 被拒绝的尝试不写 —— 否则日志会被模型的试探刷满，失去意义。
type RestartRecord struct {
	Time        time.Time `json:"time"`
	Command     string    `json:"command"`
	MatchedDeny string    `json:"matched_deny,omitempty"`
	// Impact 是执行前扫描到的活动影响，供事后复盘。
	Impact string `json:"impact,omitempty"`
	// Note 是调用方补充的上下文（例如是谁批准了什么）。
	Note string `json:"note,omitempty"`
}

// restartHistory 是进程内的追加式历史。
// 用 JSONL 落盘而非数据库：体量极小（一天可能就几条），
// 追加写天然适合，机器可读，出问题能直接 cat 出来看。
type restartHistory struct {
	mu   sync.Mutex
	path string
	now  func() time.Time
}

var defaultRestartHistory = &restartHistory{}

// DefaultRestartHistoryPath 返回历史文件位置。
func DefaultRestartHistoryPath() string {
	base := ""
	if d := os.Getenv("FAIRY_DATA_DIR"); d != "" {
		base = d
	} else if home, err := os.UserHomeDir(); err == nil && home != "" {
		base = filepath.Join(home, ".local", "share", "Fairy")
	}
	if base == "" {
		return ""
	}
	return filepath.Join(base, "restart-history.jsonl")
}

// RecordRestart 追加一条记录。失败静默 —— 历史写不进去不该影响命令执行。
func RecordRestart(record RestartRecord) {
	h := defaultRestartHistory
	if h.path == "" {
		h.path = DefaultRestartHistoryPath()
	}
	if h.path == "" {
		return
	}
	if h.now != nil {
		record.Time = h.now()
	} else if record.Time.IsZero() {
		record.Time = time.Now()
	}
	line, err := json.Marshal(record)
	if err != nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(h.path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(h.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// RecentRestarts 返回最近 limit 条记录，按时间倒序。
// 读取失败返回空切片 —— 查不到历史不该阻断流程。
func RecentRestarts(limit int) []RestartRecord {
	if limit <= 0 {
		limit = 10
	}
	path := defaultRestartHistory.path
	if path == "" {
		path = DefaultRestartHistoryPath()
	}
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []RestartRecord
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var rec RestartRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue // 坏行跳过，不让一行脏数据毁掉整个历史
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// RecentRestartSummary 给人看的简表，用于「这个服务最近被我动过吗」这类自查。
func RecentRestartSummary(limit int) string {
	records := RecentRestarts(limit)
	if len(records) == 0 {
		return ""
	}
	var b []byte
	b = append(b, "recent high-impact commands that were executed (authorised):\n"...)
	for _, r := range records {
		b = append(b, "  - "...)
		b = append(b, r.Time.Format("2006-01-02 15:04:05")...)
		b = append(b, "  "...)
		b = append(b, r.Command...)
		if r.Impact != "" {
			b = append(b, "  [impact: "...)
			b = append(b, r.Impact...)
			b = append(b, ']')
		}
		b = append(b, '\n')
	}
	return string(b)
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, c := range data {
		if c == '\n' {
			if i > start {
				lines = append(lines, data[start:i])
			}
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}
