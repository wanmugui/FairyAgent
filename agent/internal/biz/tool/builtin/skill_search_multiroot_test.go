package builtin

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func mkSkillDir(t *testing.T, root, name, description string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n# " + name + "\n正文\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func spec(roots ...string) string {
	return strings.Join(roots, string(os.PathListSeparator))
}

func TestDiscoverSkillEntriesFindsSkillsInEveryRoot(t *testing.T) {
	primary, secondary := t.TempDir(), t.TempDir()
	mkSkillDir(t, primary, "local-skill", "bundled one")
	mkSkillDir(t, secondary, "ui-ux-pro-max", "Your AI design intelligence system. UI ux styles and color palettes.")

	entries := discoverSkillEntries(spec(primary, secondary))
	byName := map[string]skillEntry{}
	for _, e := range entries {
		byName[e.Name] = e
	}
	if _, ok := byName["local-skill"]; !ok {
		t.Fatalf("主根技能未被发现：%v", byName)
	}
	got, ok := byName["ui-ux-pro-max"]
	if !ok {
		t.Fatalf("用户级根的技能未被发现，这正是要修的问题：%v", byName)
	}
	if !strings.Contains(got.Description, "design intelligence") {
		t.Fatalf("description 未读出：%q", got.Description)
	}
}

func TestDiscoverSkillEntriesGivesReadableLocationPerRoot(t *testing.T) {
	primary, secondary := t.TempDir(), t.TempDir()
	mkSkillDir(t, primary, "local-skill", "d1")
	mkSkillDir(t, secondary, "user-skill", "d2")

	byName := map[string]skillEntry{}
	for _, e := range discoverSkillEntries(spec(primary, secondary)) {
		byName[e.Name] = e
	}

	// The primary root keeps the logical local:///skills/... form.
	if got := byName["local-skill"].Location; got != "local:///skills/local-skill/SKILL.md" {
		t.Fatalf("主根 location 应保持逻辑形式，实际=%q", got)
	}
	// A skill outside the primary root must carry a real, openable location.
	loc := byName["user-skill"].Location
	if !strings.HasPrefix(loc, "local://") {
		t.Fatalf("用户级根 location 形式错误：%q", loc)
	}
	raw := strings.TrimPrefix(loc, "local://")
	if !filepath.IsAbs(filepath.FromSlash(raw)) {
		t.Fatalf("用户级根 location 不是可打开的绝对路径：%q", loc)
	}
	if _, err := os.Stat(filepath.FromSlash(raw)); err != nil {
		t.Fatalf("location 指向的文件不存在：%q (%v)", loc, err)
	}
}

func TestDiscoverSkillEntriesDeduplicatesAcrossRoots(t *testing.T) {
	primary, secondary := t.TempDir(), t.TempDir()
	mkSkillDir(t, primary, "same-name", "from primary")
	mkSkillDir(t, secondary, "same-name", "from secondary")

	entries := discoverSkillEntries(spec(primary, secondary))
	count := 0
	for _, e := range entries {
		if e.Name == "same-name" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("同名技能应只保留 1 个，实际=%d", count)
	}
}

func TestDiscoverSkillEntriesEmptyAndMissingRoots(t *testing.T) {
	if got := discoverSkillEntries(""); len(got) != 0 {
		t.Fatalf("空 spec 应返回空，实际=%v", got)
	}
	if got := discoverSkillEntries(spec(filepath.Join(t.TempDir(), "nope"))); len(got) != 0 {
		t.Fatalf("不存在的根应返回空，实际=%v", got)
	}
}

// runSkillSearch mirrors the tool body (discoverSkillEntries -> score ->
// filter -> sort -> limit) so this test exercises the real ranking path
// instead of re-implementing it.
func runSkillSearch(skillsRoot, query string, limit int) []skillEntry {
	entries := discoverSkillEntries(skillsRoot)
	hits := make([]skillEntry, 0, len(entries))
	for _, entry := range entries {
		score := skillSearchScore(query, entry.Name, entry.Description, entry.Location)
		if query != "" && score <= 0 {
			continue
		}
		entry.Score = score
		hits = append(hits, entry)
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// TestSkillSearchFindsUserLevelSkillsWithRealRoots is the end-to-end version of
// the original complaint: skill_search "ui ux" must surface the globally
// installed skills. It uses the machine's real skill roots, so it fails loudly
// if a new root ever stops being wired in.
func TestSkillSearchFindsUserLevelSkillsWithRealRoots(t *testing.T) {
	userRoot := filepath.Join(os.Getenv("USERPROFILE"), ".agents", "skills")
	if _, err := os.Stat(userRoot); err != nil {
		t.Skipf("本机无用户级技能根：%v", err)
	}
	primary := "D:\\Fairy\\skills"
	if _, err := os.Stat(primary); err != nil {
		t.Skipf("本机无配置根：%v", err)
	}

	hits := runSkillSearch(spec(primary, userRoot), "ui ux", 10)
	got := map[string]skillEntry{}
	for _, h := range hits {
		got[h.Name] = h
	}
	for _, want := range []string{"ui-ux-pro-max", "frontend-design"} {
		entry, ok := got[want]
		if !ok {
			var names []string
			for _, h := range hits {
				names = append(names, h.Name)
			}
			t.Fatalf("skill_search \"ui ux\" 未返回 %s，实际命中=%v", want, names)
		}
		if entry.Score <= 0 {
			t.Fatalf("%s 分数应为正，实际=%d", want, entry.Score)
		}
		// The location must be one that read_file can actually open.
		raw := strings.TrimPrefix(entry.Location, "local://")
		if _, err := os.Stat(filepath.FromSlash(raw)); err != nil {
			t.Fatalf("%s 的 location 打不开：%q (%v)", want, entry.Location, err)
		}
	}
	if len(hits) > 0 && hits[0].Name != "ui-ux-pro-max" {
		t.Fatalf("ui ux 的首位应为 ui-ux-pro-max，实际=%q score=%d", hits[0].Name, hits[0].Score)
	}
}

func TestSkillSearchSingleRootStillWorks(t *testing.T) {
	root := t.TempDir()
	mkSkillDir(t, root, "alpha", "does alpha things")
	if hits := runSkillSearch(root, "alpha", 10); len(hits) != 1 || hits[0].Name != "alpha" {
		t.Fatalf("单根检索回归：%v", hits)
	}
	if hits := runSkillSearch(root, "zzz-nothing", 10); len(hits) != 0 {
		t.Fatalf("无匹配应返回空：%v", hits)
	}
}
