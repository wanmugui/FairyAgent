package main

import (
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
		dirRel := filepath.ToSlash(filepath.Dir(relative))
		name := filepath.Base(filepath.Dir(relative))
		if dirRel == "." {
			name = strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		}
		if name == "" || seen[name] {
			return nil
		}
		seen[name] = true
		registry = append(registry, readSkillReg(path, name, absPathLocation(path)))
		return nil
	})
	return registry
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
	primary := ""
	if strings.TrimSpace(cfg.SkillsDir) != "" {
		primary = cfg.ResolvePath(cfg.SkillsDir)
	}
	registry := mergeSkillRegistries(cfg.Skills, DiscoverSkillRegistry(primary))
	for _, root := range userLevelSkillRoots() {
		registry = mergeSkillRegistries(registry, discoverSkillRegistryAt(root))
	}
	return bridgeSkillRegistry(registry)
}
