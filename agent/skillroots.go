package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// userLevelSkillRoots lists the skill roots that agent runtimes share on this
// machine.
//
// `npx skills add <repo>@<skill> -g -y` installs into %USERPROFILE%\.agents\skills.
// DiscoverSkillRegistry only ever walked the single cfg.SkillsDir root, so every
// globally installed skill was invisible to the router, to the system-prompt
// registry and to the coverage audit: installed, but unusable.
//
// These roots are additive. cfg.SkillsDir keeps priority, so existing setups
// behave exactly as before when none of these directories exist.
func userLevelSkillRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return nil
	}
	candidates := []string{
		filepath.Join(home, ".fairy", "skills"),
		filepath.Join(home, ".agents", "skills"),
		filepath.Join(home, ".claude", "skills"),
	}
	var roots []string
	seen := map[string]bool{}
	for _, root := range candidates {
		abs, absErr := filepath.Abs(root)
		if absErr != nil || seen[strings.ToLower(abs)] {
			continue
		}
		if info, statErr := os.Stat(abs); statErr != nil || !info.IsDir() {
			continue
		}
		seen[strings.ToLower(abs)] = true
		roots = append(roots, abs)
	}
	return roots
}

// discoverSkillRegistryAt walks one user-level skills root.
//
// It intentionally mirrors DiscoverSkillRegistry but reports an absolute
// location. The configured root can keep the "local:///skills/..." scheme
// because that prefix resolves against SkillsRoot; a skill living outside
// SkillsRoot would produce a location pointing at the wrong directory, and the
// caller could not then read the file it was just told to load.
func discoverSkillRegistryAt(root string) []SkillReg {
	return walkSkillDir(root, func(path, _ string) string { return absPathLocation(path) })
}

// absPathLocation normalises a discovered file into a local:// form the tool
// layer accepts, so callers can hand the location straight to read_file without
// needing to know which root the skill came from.
//
// The host path is appended verbatim: on Windows that yields local://C:/... and
// on POSIX local:///home/.... Prefixing an extra slash unconditionally would
// produce \C:\... on Windows, which filepath.IsAbs rejects.
func absPathLocation(path string) string {
	return "local://" + filepath.ToSlash(path)
}

// DiscoverSkillRegistriesAll builds the effective skill registry from every
// known root, applies the configured enable/disable overrides, and enriches each
// entry with the Chinese alias bridge so English-authored skills can be recalled
// by Chinese requests.
func DiscoverSkillRegistriesAll(cfg *Config) []SkillReg {
	if cfg == nil {
		return nil
	}
	// configuredSkillsRoot returns a joined path-list spec, which a single-root
	// walk cannot parse. Walk the configured root on its own here, then add the
	// user-level roots one by one.
	//
	// config.Skills 是**唯一真相源**：注册表的条目集合只由它决定。
	// 目录扫描只做一件事 —— 给已登记的条目补 metadata（description/tags/triggers）。
	// 它**不再**独立新增条目。上一版是 merge(cfg.Skills, 磁盘扫描)，
	// 磁盘上多出来的目录会静默混进注册表，于是主人改 config 的分类 / 核心 /
	// 启用状态对这些新条目完全无效，「config 是 config」也就名不副实。
	// 现在把那个 merge 换成 backfill：条目集合由 config 说了算。
	primary := ""
	if strings.TrimSpace(cfg.SkillsDir) != "" {
		primary = cfg.ResolvePath(cfg.SkillsDir)
	}
	if len(cfg.Skills) > 0 {
		disk := DiscoverSkillRegistry(primary)
		for _, root := range userLevelSkillRoots() {
			disk = append(disk, discoverSkillRegistryAt(root)...)
		}
		return bridgeSkillRegistry(backfillSkillMetadata(cfg.Skills, disk))
	}
	// 没有显式登记时保持旧行为：纯目录发现（首次使用 / 纯工具态）。
	registry := DiscoverSkillRegistry(primary)
	for _, root := range userLevelSkillRoots() {
		registry = mergeSkillRegistries(registry, discoverSkillRegistryAt(root))
	}
	return bridgeSkillRegistry(registry)
}

// backfillSkillMetadata 用磁盘扫描结果补全 configured 条目里缺失的描述性字段。
//
// configured 优先：enabled / priority / category 这些**人工设定永不**被磁盘覆盖
// （第一版反向覆盖，导致主人把 skill 关掉后又被目录扫描打开）。
func backfillSkillMetadata(configured, disk []SkillReg) []SkillReg {
	byName := make(map[string]SkillReg, len(disk))
	for _, d := range disk {
		if d.Name != "" {
			byName[d.Name] = d
		}
	}
	out := make([]SkillReg, 0, len(configured))
	var missing []string
	for _, c := range configured {
		if c.Name == "" {
			continue
		}
		d, ok := byName[c.Name]
		if !ok {
			missing = append(missing, c.Name)
		}
		merged := mergeSkillMetadata(c, d)
		if !ok {
			merged.Enabled = c.Enabled // 磁盘没有这个条目，enabled 只能来自 config
		}
		out = append(out, merged)
	}
	if len(missing) > 0 {
		fmt.Printf("[skill] config 登记但磁盘无对应文件（按登记保留）: %v\n", missing)
	}
	return out
}


// skillDeclaredName 读取技能文件里声明的 name（SKILL.md 走 frontmatter，
// plugin.json / manifest.json 走 JSON 的 name 字段）。读不到就返回 ""，由调用方
// 退回目录名。
func skillDeclaredName(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	content := strings.TrimPrefix(string(data), "\uFEFF")
	if strings.EqualFold(filepath.Ext(path), ".json") {
		var meta map[string]any
		if json.Unmarshal(data, &meta) != nil {
			return ""
		}
		if s, ok := meta["name"].(string); ok {
			return strings.TrimSpace(s)
		}
		return ""
	}
	return strings.TrimSpace(frontmatterScalar(frontmatterBlock(content), "name"))
}

// walkSkillDir 扫描一个技能根目录并构建注册表，是发现逻辑的唯一实现。
//
// locationFn 让两个调用方保留各自原有的 location 格式：
//   - DiscoverSkillRegistry 用 local:///skills/<相对路径>
//   - discoverSkillRegistryAt 用 local://<绝对路径>
//
// 之前这两份扫描逻辑是各写一遍的，修一处漏一处（声明名优先只在其中一份里生效，
// 导致测试走的路径仍然把 vendor 注册成 vendor）。合成一份才能避免再次漂移。
func walkSkillDir(root string, locationFn func(path, relative string) string) []SkillReg {
	trimmed := strings.TrimSpace(root)
	if trimmed == "" {
		return nil
	}
	absRoot, err := filepath.Abs(trimmed)
	if err != nil {
		return nil
	}
	var registry []SkillReg
	seen := map[string]bool{}
	_ = filepath.WalkDir(absRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return nil
		}
		base := strings.ToLower(entry.Name())
		if base != "skill.md" && base != "plugin.json" && base != "manifest.json" {
			return nil
		}
		relative, relErr := filepath.Rel(absRoot, path)
		if relErr != nil {
			return nil
		}
		// JSON 形态的技能清单必须自带 name 才会被注册。
		//
		// 为什么单独加这条：skills/mv-render/vendor/template/engine/fonts/manifest.json
		// 是**字体清单**（顶层是数组，没有 name 字段），以前 skillDeclaredName 解析失败
		// 返回空串，就回退用目录名，于是它被注册成一个叫 "fonts" 的 skill——
		// 既污染 skill_search（会搜到一个空技能），也让 TestSkillMetadataCoverage
		// 报 "skill \"fonts\" missing tags/triggers/priority"。
		//
		// 注意这里**不能**改成「跳过整个 vendor 目录」：vendor/SKILL.md 声明的
		// vocaloid-style-mv 是有意注册的（见下面 name 的注释），跳过会让它消失。
		if base != "skill.md" {
			if declared := skillDeclaredName(path); declared == "" {
				return nil
			}
		}
		name := filepath.Base(filepath.Dir(relative))
		if filepath.ToSlash(filepath.Dir(relative)) == "." {
			name = strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		}
		// 优先用文件里声明的 name。目录名对嵌套目录毫无意义：
		// skills/mv-render/vendor/SKILL.md 声明的是 vocaloid-style-mv，
		// 按目录名会注册成 "vendor"，既搜不到也会和别的 vendor 目录撞名被去重丢掉。
		if declared := skillDeclaredName(path); declared != "" {
			name = declared
		}
		if name == "" || seen[name] {
			return nil
		}
		seen[name] = true
		registry = append(registry, readSkillReg(path, name, locationFn(path, relative)))
		return nil
	})
	return registry
}
