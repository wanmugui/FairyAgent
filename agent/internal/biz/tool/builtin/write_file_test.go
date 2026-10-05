package builtin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalWriteFileCreatesParentAndReturnsRelativePath(t *testing.T) {
	workspace := t.TempDir()
	result := executeLocalFileTool(t, NewLocalWriteFileTool(localFileTestSchema("write_file")), workspace, `{"file_path":"nested/out.txt","content":"hello"}`)
	if result.IsError {
		t.Fatalf("unexpected error result: %#v", result.Value)
	}
	if got, want := result.Value["path"], "nested/out.txt"; got != want {
		t.Fatalf("unexpected result path: got %#v want %q", got, want)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, "nested", "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "hello" {
		t.Fatalf("unexpected file content: %q", raw)
	}
}

func TestLocalWriteFileAllowsBundledSkillPath(t *testing.T) {
	// Post-loosen: bundled-skill paths are writable when the agent supplies an
	// absolute /skills target. The skill-vs-workspace distinction is now
	// informational, not a containment boundary.
	//
	// 之前这里硬编码 local:///skills/...，于是测试真的往系统根目录 /skills 写：
	// 非 root 直接 permission denied，root 跑则会在 /skills 留下真实文件并污染
	// 宿主机。改为用 t.TempDir() 造一个临时 skills 根，既合法又不碰宿主机。
	skillsRoot := filepath.Join(t.TempDir(), "skills")
	pkgDir := filepath.Join(skillsRoot, "ppt-maker")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("prepare bundled skill dir: %v", err)
	}
	payload, err := json.Marshal(map[string]string{
		"file_path": filepath.ToSlash(pkgDir) + "/SKILL.md",
		"content":   "changed",
	})
	if err != nil {
		t.Fatal(err)
	}
	result := executeLocalFileTool(t, NewLocalWriteFileTool(localFileTestSchema("write_file")), t.TempDir(), string(payload))
	if result.IsError {
		t.Fatalf("expected bundled skill write to succeed, got %#v", result.Value)
	}
	raw, err := os.ReadFile(filepath.Join(pkgDir, "SKILL.md"))
	if err != nil {
		t.Fatalf("bundled skill file not written: %v", err)
	}
	if string(raw) != "changed" {
		t.Fatalf("unexpected bundled skill content: %q", raw)
	}
}
