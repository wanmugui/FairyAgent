package shared

import (
	"agentloop/agent/internal/dtypes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type ToolContext struct {
	Workspace   string
	SessionFile string
}

// ReadablePathRoot identifies the logical root used to resolve a read-only
// local file path. It mirrors the two sandbox roots exposed in production:
// session data and bundled skills.
type ReadablePathRoot string

const (
	ReadablePathWorkspace ReadablePathRoot = "workspace"
	ReadablePathSkills    ReadablePathRoot = "skills"
	ReadablePathMemory    ReadablePathRoot = "memory"
)

func ContextForInvocation(invocation dtypes.ToolInvocation) (ToolContext, error) {
	workspace := strings.TrimSpace(invocation.Workspace)
	if workspace == "" {
		return ToolContext{}, fmt.Errorf("workspace is required")
	}
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return ToolContext{}, fmt.Errorf("resolve workspace: %w", err)
	}
	return ToolContext{Workspace: filepath.Clean(absWorkspace), SessionFile: invocation.SessionFile}, nil
}

func DecodeArgs(invocation dtypes.ToolInvocation) (map[string]any, error) {
	raw := strings.TrimSpace(string(invocation.Args))
	if raw == "" {
		return map[string]any{}, nil
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, fmt.Errorf("parse tool arguments: %w", err)
	}
	if args == nil {
		return map[string]any{}, nil
	}
	return args, nil
}

func ResolveWorkspacePath(workspace, requested string) (string, error) {
	workspace = strings.TrimSpace(workspace)
	requested = strings.TrimSpace(requested)
	if workspace == "" {
		return "", fmt.Errorf("workspace is required")
	}
	if requested == "" {
		return "", fmt.Errorf("path is required")
	}
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	absWorkspace = filepath.Clean(absWorkspace)
	for _, prefix := range []string{"local://", "memory://", "knowledge://"} {
		if strings.HasPrefix(strings.ToLower(requested), prefix) {
			requested = requested[len(prefix):]
			break
		}
	}
	// /mnt/data is the production sandbox's workspace root.  Local process
	// tools receive the same logical paths from Skills, so accept it wherever a
	// workspace-relative path is accepted (including bash.working_dir).
	requested = strings.ReplaceAll(requested, "\\", "/")
	if relative, ok := virtualRootRelative(requested, "mnt/data"); ok {
		requested = relative
	}
	requested = filepath.FromSlash(requested)
	fullPath := requested
	if !filepath.IsAbs(fullPath) {
		fullPath = filepath.Join(absWorkspace, fullPath)
	}
	fullPath, err = filepath.Abs(filepath.Clean(fullPath))
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", requested, err)
	}
	// Post-loosen: absolute paths are accepted as-is so the agent can reach
	// any host directory the OS user can reach. Workspace containment still
	// applies to relative paths (joined above). Destructive operations are
	// policed at the bash layer, not here.
	return fullPath, nil
}

// ResolveReadablePath resolves a path accepted by read-only local tools.
//
// Post-loosen behavior:
//   - /skills/... still maps to the configured skills root (read-only).
//   - /mnt/data/... still maps to the workspace for production-skill compat.
//   - `local://...` accepts both virtual roots plus workspace-relative.
//   - Other absolute paths are accepted unchanged; the bash policy layer
//     is responsible for blocking destructive operations.
//   - `memory://` maps to the configured local memory root.
//   - `knowledge://` remains unsupported by this local backend.
// SplitRoots expands a root specification into individual roots.
//
// A single path behaves exactly as before. Several roots are joined with
// os.PathListSeparator (";" on Windows, ":" on POSIX), which lets every existing
// constructor keep its plain string parameter while still covering the
// user-level skill roots alongside the configured one. Windows drive paths
// contain ":" but never ";", so the separator is unambiguous.
// splitListKeepDriveLetter 补 filepath.SplitList 的一处跨平台缺陷。
//
// 上面的注释假设"Windows 盘符含 ':' 但从不含 ';'"，这只在 Windows 成立。
// 在 POSIX 上 filepath.SplitList 按 ':' 切分，于是 "D:\Fairy\skills" 会被切成
// ["D", "\Fairy\skills"]：盘符截断，配置里第二个 skill 根直接失效。
// 这里把被切开的盘符重新接回去，还原 SplitList 吃掉的那个列表分隔符。
// 只在"单字母 + 紧跟路径分隔符"这种明确的盘符形态上合并，
// 避免误伤 POSIX 里合法的 "a:b" 条目（后者下一段不以分隔符开头）。
func splitListKeepDriveLetter(spec string) []string {
	parts := filepath.SplitList(spec)
	merged := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(merged) > 0 {
			prev := merged[len(merged)-1]
			if isDriveLetter(prev) && part != "" && isPathSeparatorAny(part[0]) {
				// prev 是 "D"，part 是 "\Fairy\skills" 或 "/Fairy/skills"，
				// 中间被吃掉的正是 os.PathListSeparator（POSIX 上就是 ':'），
				// 还原它才能得到原样的 "D:\Fairy\skills"。
				merged[len(merged)-1] = prev + string(os.PathListSeparator) + part
				continue
			}
		}
		merged = append(merged, part)
	}
	return merged
}

// isDriveLetter 判断是否为形如 "D" 的 Windows 盘符本体（不含冒号）。
func isDriveLetter(s string) bool {
	if len(s) != 1 {
		return false
	}
	c := s[0]
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

// isPathSeparatorAny 不依赖宿主 OS 地判断路径分隔符。
// os.IsPathSeparator('\\') 在 POSIX 上恒为 false，但配置里可能带着
// Windows 风格的根（从别的机器拷来的），所以两种分隔符都认。
func isPathSeparatorAny(c byte) bool {
	return c == '/' || c == '\\'
}

// normalizePathForCompare 生成去重用的比较键：
// 统一分隔符后再 Clean，这样 "D:/Fairy/skills" 与 "d:\fairy\skills"
// 会被认成同一个根（大小写与分隔符风格都不敏感）。
func normalizePathForCompare(p string) string {
	slashed := strings.Map(func(r rune) rune {
		if r == '\\' {
			return '/'
		}
		return r
	}, p)
	return filepath.Clean(slashed)
}

func SplitRoots(spec string) []string {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" {
		return nil
	}
	parts := splitListKeepDriveLetter(trimmed)
	var roots []string
	seen := map[string]bool{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		key := strings.ToLower(normalizePathForCompare(part))
		if part == "" || seen[key] {
			continue
		}
		seen[key] = true
		roots = append(roots, part)
	}
	return roots
}

func ResolveReadablePath(workspace, skillsRoot, requested string) (string, ReadablePathRoot, error) {
	return ResolveReadablePathWithMemory(workspace, skillsRoot, "", requested)
}

func ResolveReadablePathWithMemory(workspace, skillsRoot, memoryRoot, requested string) (string, ReadablePathRoot, error) {
	raw := strings.TrimSpace(requested)
	if raw == "" {
		return "", "", fmt.Errorf("path is required")
	}

	lower := strings.ToLower(raw)
	hasLocalScheme := false
	switch {
	case strings.HasPrefix(lower, "knowledge://"):
		return "", "", fmt.Errorf("path %q uses an unsupported filesystem; this local runtime supports only local:// workspace files, memory:// memories, and bundled skills", requested)
	case strings.HasPrefix(lower, "memory://"):
		if strings.TrimSpace(memoryRoot) == "" {
			return "", "", fmt.Errorf("path %q refers to local memory, but no memory directory is configured", requested)
		}
		raw = strings.TrimLeft(raw[len("memory://"):], "/")
		fullPath, err := resolvePathWithinRoot(memoryRoot, raw)
		if err != nil {
			return "", "", err
		}
		return fullPath, ReadablePathMemory, nil
	case strings.HasPrefix(lower, "local://"):
		raw = raw[len("local://"):]
		hasLocalScheme = true
	}

	// Treat protocol paths as slash-separated regardless of the host OS. This
	// accepts both production-compatible spellings: local://skills/... and
	// local:///skills/....
	raw = strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/")
	if hasLocalScheme || isAbsolutePath(raw) {
		if relative, ok := virtualRootRelative(raw, "skills"); ok {
			roots := SplitRoots(skillsRoot)
			if len(roots) == 0 {
				return "", "", fmt.Errorf("path %q refers to bundled skills, but no skills directory is configured", requested)
			}
			// resolvePathWithinRoot only rejects path escapes, it does not verify
			// that the file exists. So the first root that actually holds the
			// file wins; otherwise fall back to the first root, which preserves
			// the pre-multi-root behaviour exactly.
			var fallback string
			var lastErr error
			for _, root := range roots {
				fullPath, err := resolvePathWithinRoot(root, relative)
				if err != nil {
					lastErr = err
					continue
				}
				if _, statErr := os.Stat(fullPath); statErr == nil {
					return fullPath, ReadablePathSkills, nil
				}
				if fallback == "" {
					fallback = fullPath
				}
			}
			if fallback != "" {
				return fallback, ReadablePathSkills, nil
			}
			if lastErr == nil {
				lastErr = fmt.Errorf("path %q was not found under any configured skills directory", requested)
			}
			return "", "", lastErr
		}
		if relative, ok := virtualRootRelative(raw, "mnt/data"); ok {
			fullPath, err := resolvePathWithinRoot(workspace, relative)
			if err != nil {
				return "", "", err
			}
			return fullPath, ReadablePathWorkspace, nil
		}
		// Any other absolute path: accept it literally. Pick ReadablePathSkills
		// only when the resolved file actually lives under one of the skills
		// roots; the workspace tag is the safe default for everything else.
		if hasLocalScheme || isAbsolutePath(raw) {
			cleaned, err := filepath.Abs(filepath.FromSlash(raw))
			if err != nil {
				return "", "", fmt.Errorf("resolve path %q: %w", raw, err)
			}
			for _, root := range SplitRoots(skillsRoot) {
				if rel, relErr := filepath.Rel(root, cleaned); relErr == nil &&
					rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
					return cleaned, ReadablePathSkills, nil
				}
			}
			return cleaned, ReadablePathWorkspace, nil
		}
	}

	fullPath, err := resolvePathWithinRoot(workspace, raw)
	if err != nil {
		return "", "", err
	}
	return fullPath, ReadablePathWorkspace, nil
}

// isAbsolutePath reports whether raw (already slash-normalized) is an
// absolute path on the host OS: POSIX-style leading "/" (incl. virtual roots
// such as /skills/... and /mnt/data/...), and Windows drive-letter paths such
// as "C:/project" (which filepath.IsAbs only recognizes when running on Windows).
func isAbsolutePath(raw string) bool {
	if strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "\\") {
		return true
	}
	if filepath.IsAbs(filepath.FromSlash(raw)) {
		return true
	}
	if len(raw) >= 3 {
		c := raw[0]
		if ((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) && raw[1] == ':' && (raw[2] == '/' || raw[2] == '\\') {
			return true
		}
	}
	return false
}

func virtualRootRelative(raw, root string) (string, bool) {
	trimmed := strings.TrimLeft(raw, "/")
	if trimmed == root {
		return "", true
	}
	if strings.HasPrefix(trimmed, root+"/") {
		return strings.TrimPrefix(trimmed, root+"/"), true
	}
	return "", false
}

func resolvePathWithinRoot(root, requested string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", fmt.Errorf("root directory is required")
	}
	if requested == "" {
		requested = "."
	}
	requested = filepath.FromSlash(requested)
	if filepath.IsAbs(requested) {
		return "", fmt.Errorf("path %q is outside allowed root", requested)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}
	absRoot = filepath.Clean(absRoot)
	fullPath := filepath.Clean(filepath.Join(absRoot, requested))
	relative, err := filepath.Rel(absRoot, fullPath)
	if err != nil {
		return "", fmt.Errorf("check path %q: %w", requested, err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("path %q is outside allowed root", requested)
	}
	return fullPath, nil
}

func RelativePath(workspace, fullPath string) string {
	relative, err := filepath.Rel(workspace, fullPath)
	if err != nil {
		return filepath.ToSlash(fullPath)
	}
	return filepath.ToSlash(relative)
}

func ResolveMemoryPath(memoryRoot, requested string) (string, error) {
	lower := strings.ToLower(strings.TrimSpace(requested))
	if !strings.HasPrefix(lower, "memory://") {
		return "", fmt.Errorf("path %q does not use memory://", requested)
	}
	if strings.TrimSpace(memoryRoot) == "" {
		return "", fmt.Errorf("memory root is not configured")
	}
	raw := strings.TrimLeft(strings.TrimSpace(requested)[len("memory://"):], "/")
	return resolvePathWithinRoot(memoryRoot, raw)
}

func ErrorResult(toolName string, err error) dtypes.ToolResult {
	return dtypes.ToolResult{Value: map[string]any{"tool": toolName, "error": err.Error()}, IsError: true}
}

func StringArg(args map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := args[key].(string); ok {
			return value
		}
	}
	return ""
}

func IntArg(args map[string]any, key string, defaultValue int) int {
	value, ok := args[key].(float64)
	if !ok {
		return defaultValue
	}
	return int(value)
}

// IntMetadata reads an integer tool-invocation metadata value. Missing or
// malformed values fall back to defaultValue.
func IntMetadata(invocation dtypes.ToolInvocation, key string, defaultValue int) int {
	if invocation.Metadata == nil {
		return defaultValue
	}
	raw, ok := invocation.Metadata[key]
	if !ok {
		return defaultValue
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return defaultValue
	}
	return value
}

func BoolArg(args map[string]any, key string) bool {
	value, _ := args[key].(bool)
	return value
}

func WriteJSONFileAtomically(path string, value any) error {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create parent directory: %w", err)
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON: %w", err)
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(parent, ".agent-tool-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary JSON file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("protect temporary JSON file: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary JSON file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temporary JSON file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary JSON file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			return fmt.Errorf("replace JSON file: %w (remove destination: %v)", err, removeErr)
		}
		if retryErr := os.Rename(tmpPath, path); retryErr != nil {
			return fmt.Errorf("rename temporary JSON file: %w", retryErr)
		}
	}
	return nil
}
