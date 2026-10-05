package shared

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSplitRootsSingleAndMultiple(t *testing.T) {
	single := filepath.Join("D:", "Fairy", "skills")
	if got := SplitRoots(single); len(got) != 1 || got[0] != single {
		t.Fatalf("单根应原样返回，got=%v", got)
	}
	multi := strings.Join([]string{"D:\\Fairy\\skills", "C:\\Users\\x\\.agents\\skills"}, string(os.PathListSeparator))
	got := SplitRoots(multi)
	if len(got) != 2 {
		t.Fatalf("应拆成 2 个根，got=%v", got)
	}
	if got[0] != "D:\\Fairy\\skills" || got[1] != "C:\\Users\\x\\.agents\\skills" {
		t.Fatalf("顺序或内容错误，got=%v", got)
	}
}

func TestSplitRootsWindowsDriveColonsAreNotSeparators(t *testing.T) {
	// The whole point of using os.PathListSeparator instead of ":" is that a
	// Windows drive letter already contains a colon. Splitting on ":" would
	// shred "C:\Users" into "C" and "\Users".
	spec := "D:\\Fairy\\skills" + string(os.PathListSeparator) + "C:\\Users\\Admin\\.agents\\skills"
	got := SplitRoots(spec)
	if len(got) != 2 {
		t.Fatalf("盘符冒号被误当分隔符，got=%v", got)
	}
	if !strings.HasPrefix(got[1], "C:") {
		t.Fatalf("第二个根被破坏：%q", got[1])
	}
}

func TestSplitRootsEmptyAndDedupe(t *testing.T) {
	if got := SplitRoots("   "); got != nil {
		t.Fatalf("空白应返回 nil，got=%v", got)
	}
	dup := strings.Join([]string{"D:/Fairy/skills", "d:\\fairy\\skills", "E:/x"}, string(os.PathListSeparator))
	got := SplitRoots(dup)
	if len(got) != 2 {
		t.Fatalf("大小写不同的同一路径应去重，got=%v", got)
	}
}

func mkSkill(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(file, []byte("---\nname: "+name+"\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestResolveReadablePathPrefersFirstRootThatHasTheFile(t *testing.T) {
	base := t.TempDir()
	primary, secondary := filepath.Join(base, "primary"), filepath.Join(base, "secondary")
	mkSkill(t, primary, "only-in-primary")
	mkSkill(t, secondary, "only-in-secondary")
	mkSkill(t, primary, "in-both")
	mkSkill(t, secondary, "in-both")
	spec := strings.Join([]string{primary, secondary}, string(os.PathListSeparator))

	// Lives only in the second root: must NOT be silently mapped into the
	// first root, which would produce a path that does not exist.
	got, root, err := ResolveReadablePath("", spec, "local:///skills/only-in-secondary/SKILL.md")
	if err != nil {
		t.Fatalf("第二个根的技能应可读：%v", err)
	}
	if root != ReadablePathSkills {
		t.Fatalf("root 标记错误：%v", root)
	}
	if !strings.Contains(got, filepath.Join("secondary", "only-in-secondary")) {
		t.Fatalf("未命中第二个根，实际=%q", got)
	}
	if _, statErr := os.Stat(got); statErr != nil {
		t.Fatalf("解析结果不存在：%q (%v)", got, statErr)
	}

	// Exists in both: the configured (first) root must win.
	got, _, err = ResolveReadablePath("", spec, "local:///skills/in-both/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, filepath.Join("primary", "in-both")) {
		t.Fatalf("应优先命中第一个根，实际=%q", got)
	}
}

func TestResolveReadablePathRejectsPathEscapeAcrossAllRoots(t *testing.T) {
	primary, secondary := t.TempDir(), t.TempDir()
	spec := strings.Join([]string{primary, secondary}, string(os.PathListSeparator))
	if _, _, err := ResolveReadablePath("", spec, "local:///skills/../../etc/passwd"); err == nil {
		t.Fatal("越界路径必须被拒绝，遍历所有根也不行")
	}
}

func TestResolveReadablePathSingleRootBehaviourUnchanged(t *testing.T) {
	root := t.TempDir()
	mkSkill(t, root, "solo")
	if _, _, err := ResolveReadablePath("", root, "local:///skills/solo/SKILL.md"); err != nil {
		t.Fatalf("单根行为必须与改造前一致：%v", err)
	}
	// resolvePathWithinRoot guards against escape only; a missing file must
	// still resolve to a path, exactly as it did before multi-root support.
	got, root2, err := ResolveReadablePath("", root, "local:///skills/missing/SKILL.md")
	if err != nil {
		t.Fatalf("缺失文件不应在此层报错：%v", err)
	}
	if root2 != ReadablePathSkills || !strings.Contains(got, "missing") {
		t.Fatalf("缺失文件解析结果异常：%q %v", got, root2)
	}
}

func TestResolveReadablePathTagsUserLevelFileAsSkills(t *testing.T) {
	primary, secondary := t.TempDir(), t.TempDir()
	file := mkSkill(t, secondary, "tagged")
	spec := strings.Join([]string{primary, secondary}, string(os.PathListSeparator))

	_, root, err := ResolveReadablePath("", spec, "local://"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	if root != ReadablePathSkills {
		t.Fatalf("用户级根下的文件应标记为 skills，实际=%v", root)
	}
}

func TestResolveReadablePathStillAllowsWorkspacePaths(t *testing.T) {
	ws := t.TempDir()
	spec := strings.Join([]string{ws + "-skills", ws + "-more"}, string(os.PathListSeparator))
	file := filepath.Join(ws, "note.md")
	if err := os.WriteFile(file, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, root, err := ResolveReadablePath(ws, spec, "note.md")
	if err != nil {
		t.Fatalf("工作区路径必须仍可读：%v", err)
	}
	if root != ReadablePathWorkspace {
		t.Fatalf("工作区文件不应被标成 skills，实际=%v", root)
	}
}
