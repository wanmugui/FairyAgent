package local

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newPartFile 造一个符合分片命名形态的暂存文件。
func newPartFile(t *testing.T, dir, name string, size int64, mod time.Time) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestLooksLikeUploadPartFile 锁死识别规则：只认
// ".<原名>.<32位hex uuid>.part"，不误伤普通文件。
func TestLooksLikeUploadPartFile(t *testing.T) {
	yes := []string{
		// 真实形态：crypto.randomUUID() 产出带连字符的 36 字符 UUID
		".img0003 (1).jpg.357bb1f3-2eab-42db-aac8-9bf56a046544.part",
		".Evangelion.mp4.81de49ad-2e23-426a-a5cf-c038571796a7.part",
		// 无连字符的也要认
		".movie.mp4.81de49ad8c9448d4bb2e0f3a1c6d7e8f.part",
	}
	for _, n := range yes {
		if !LooksLikeUploadPartFile(n) {
			t.Errorf("应识别为分片暂存: %s", n)
		}
	}
	no := []string{
		"movie.mp4",             // 普通文件
		".part",                 // 只有后缀
		".movie.mp4.part",       // 缺 uuid 段
		".movie.mp4.short.part", // uuid 长度不对
		".movie.mp4.zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz.part", // 非 hex
		"movie.mp4.81de49ad8c9448d4bb2e0f3a1c6d7e8f.part",  // 未以 . 开头
	}
	for _, n := range no {
		if LooksLikeUploadPartFile(n) {
			t.Errorf("不应识别为分片暂存: %s", n)
		}
	}
}

// TestActiveUploadScannerFindsRecentParts 近期写入的才算活跃。
func TestActiveUploadScannerFindsRecentParts(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	newPartFile(t, dir, ".recent.mp4.81de49ad8c9448d4bb2e0f3a1c6d7e8f.part", 1024, now.Add(-2*time.Minute))
	newPartFile(t, dir, ".stale.mp4.91de49ad8c9448d4bb2e0f3a1c6d7e8f.part", 1024, now.Add(-3*time.Hour))

	s := ActiveUploadScanner{Roots: []string{dir}, Now: func() time.Time { return now }}
	got := s.Scan()
	if len(got) != 1 {
		t.Fatalf("只应识别近期写入的 1 个，实际 %d: %+v", len(got), got)
	}
	if got[0].Target != "recent.mp4" {
		t.Errorf("目标文件名还原错误: %q", got[0].Target)
	}
	// 带连字符的真实 UUID 也必须能正确还原目标名
	if got := UploadPartTarget(".img0003 (1).jpg.357bb1f3-2eab-42db-aac8-9bf56a046544.part"); got != "img0003 (1).jpg" {
		t.Errorf("带连字符UUID 的目标名还原错误: %q", got)
	}
}

// TestPolicyNamesActiveUploadInDenial 核心场景：
// 磁盘上有活跃上传时，拒绝理由必须点名具体文件与大小，
// 让模型能拿去如实告知主人会损失什么。
func TestPolicyNamesActiveUploadInDenial(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	newPartFile(t, dir, ".Evangelion.mp4.81de49ad8c9448d4bb2e0f3a1c6d7e8f.part", 4096, now.Add(-time.Minute))

	p := DefaultBashPolicy()
	p.ActiveUploadScanner = &ActiveUploadScanner{Roots: []string{dir}, Now: func() time.Time { return now }}

	for _, cmd := range []string{`pkill -f "server.cjs 8100"`, "restart fairy", "kill -9 740122"} {
		d := p.Check(cmd)
		if d.Allowed {
			t.Fatalf("%q 应被拦截", cmd)
		}
		if !contains(d.Reason, "Evangelion.mp4") {
			t.Errorf("%q 的理由未点名正在上传的文件: %s", cmd, d.Reason)
		}
		if !contains(d.Reason, "Concretely") {
			t.Errorf("%q 的理由缺少具体影响段落: %s", cmd, d.Reason)
		}
	}
}

// TestPolicySilentWhenNoActiveUpload 没有活跃上传时不该渲染空话，
// 也不该影响正常拦截流程。
func TestPolicySilentWhenNoActiveUpload(t *testing.T) {
	p := DefaultBashPolicy()
	p.ActiveUploadScanner = &ActiveUploadScanner{Roots: []string{t.TempDir()}}

	d := p.Check(`pkill -f node`)
	if d.Allowed {
		t.Fatal("仍应被拦截")
	}
	if contains(d.Reason, "Concretely") {
		t.Errorf("无活跃上传时不该附加具体影响段落: %s", d.Reason)
	}
}

// TestPolicyScannerFailureNeverBlocks 扫描器 panic 不能把命令拦死 ——
// 诊断功能永远不能反过来阻断放行判断。
func TestPolicyScannerFailureNeverBlocks(t *testing.T) {
	p := DefaultBashPolicy()
	p.ActiveUploadScanner = &ActiveUploadScanner{Roots: []string{"/definitely/not/exist/xxx"}}
	d := p.Check(`pkill -f node`)
	if d.Allowed {
		t.Error("仍应被拦截（扫描失败不应放行）")
	}
	if d.Reason == "" {
		t.Error("拒绝理由不应为空")
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		(func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		})()
}
