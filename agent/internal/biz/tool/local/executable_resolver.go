package local

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

type localExecutable struct {
	Path       string
	PrefixArgs []string
	Source     string
}

type localPythonVersion struct {
	Major int
	Minor int
}

const (
	minimumPythonMajor = 3
	minimumPythonMinor = 10
)

type localCapabilityError struct {
	Capability string
	Checked    []string
	Reason     string
}

func (e *localCapabilityError) Error() string {
	if e == nil {
		return "local capability is unavailable"
	}
	message := fmt.Sprintf("local %s capability is unavailable", e.Capability)
	if strings.TrimSpace(e.Reason) != "" {
		message += ": " + e.Reason
	}
	if len(e.Checked) > 0 {
		message += "; checked " + strings.Join(e.Checked, ", ")
	}
	return message
}

type localExecutableResolver struct {
	goos          string
	getenv        func(string) string
	lookPath      func(string) (string, error)
	isFile        func(string) bool
	pythonVersion func(localExecutable) (localPythonVersion, error)
}

func newLocalExecutableResolver() localExecutableResolver {
	return localExecutableResolver{
		goos:     runtime.GOOS,
		getenv:   os.Getenv,
		lookPath: exec.LookPath,
		isFile: func(path string) bool {
			info, err := os.Stat(path)
			return err == nil && !info.IsDir()
		},
		pythonVersion: inspectLocalPythonVersion,
	}
}

func toolRuntimeExecutables(cfg *Config) LocalExecutableConfig {
	if cfg == nil || cfg.ToolRuntime == nil {
		return LocalExecutableConfig{}
	}
	return cfg.ToolRuntime.Executables
}

func (r localExecutableResolver) withDefaults() localExecutableResolver {
	if r.goos == "" {
		r.goos = runtime.GOOS
	}
	if r.getenv == nil {
		r.getenv = os.Getenv
	}
	if r.lookPath == nil {
		r.lookPath = exec.LookPath
	}
	if r.isFile == nil {
		r.isFile = func(path string) bool {
			info, err := os.Stat(path)
			return err == nil && !info.IsDir()
		}
	}
	if r.pythonVersion == nil {
		r.pythonVersion = inspectLocalPythonVersion
	}
	return r
}

func inspectLocalPythonVersion(python localExecutable) (localPythonVersion, error) {
	args := append([]string{}, python.PrefixArgs...)
	args = append(args, "-c", "import sys; print(f'{sys.version_info.major}.{sys.version_info.minor}')")
	output, err := exec.Command(python.Path, args...).Output()
	if err != nil {
		return localPythonVersion{}, err
	}
	parts := strings.Split(strings.TrimSpace(string(output)), ".")
	if len(parts) != 2 {
		return localPythonVersion{}, fmt.Errorf("unexpected Python version output %q", strings.TrimSpace(string(output)))
	}
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	if majorErr != nil || minorErr != nil {
		return localPythonVersion{}, fmt.Errorf("unexpected Python version output %q", strings.TrimSpace(string(output)))
	}
	return localPythonVersion{Major: major, Minor: minor}, nil
}

func supportsLocalMinimumPython(version localPythonVersion) bool {
	return version.Major > minimumPythonMajor ||
		(version.Major == minimumPythonMajor && version.Minor >= minimumPythonMinor)
}

func (r localExecutableResolver) lookup(value string) (string, bool) {
	r = r.withDefaults()
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	if path, err := r.lookPath(value); err == nil && strings.TrimSpace(path) != "" {
		return path, true
	}
	if r.isFile(value) {
		return value, true
	}
	return "", false
}

func (r localExecutableResolver) resolveExplicit(capability, source, value string, prefixArgs []string) (localExecutable, error) {
	if path, ok := r.lookup(value); ok {
		return localExecutable{Path: path, PrefixArgs: prefixArgs, Source: source}, nil
	}
	return localExecutable{}, &localCapabilityError{
		Capability: capability,
		Checked:    []string{source + "=" + strings.TrimSpace(value)},
		Reason:     "the explicitly configured executable was not found",
	}
}

func (r localExecutableResolver) resolveCandidates(capability string, candidates []localExecutable) (localExecutable, error) {
	checked := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		checked = append(checked, candidate.Source)
		if path, ok := r.lookup(candidate.Path); ok {
			candidate.Path = path
			return candidate, nil
		}
	}
	return localExecutable{}, &localCapabilityError{
		Capability: capability,
		Checked:    checked,
		Reason:     "no supported executable was found",
	}
}

func (r localExecutableResolver) isSupportedPython(python localExecutable) bool {
	r = r.withDefaults()
	version, err := r.pythonVersion(python)
	return err == nil && supportsLocalMinimumPython(version)
}

func (r localExecutableResolver) resolveExplicitPython(source, value string, prefixArgs []string) (localExecutable, error) {
	python, err := r.resolveExplicit("python", source, value, prefixArgs)
	if err != nil {
		return localExecutable{}, err
	}
	if r.isSupportedPython(python) {
		return python, nil
	}
	return localExecutable{}, &localCapabilityError{
		Capability: "python",
		Checked:    []string{source + "=" + strings.TrimSpace(value)},
		Reason:     fmt.Sprintf("the explicitly configured executable must provide Python %d.%d+", minimumPythonMajor, minimumPythonMinor),
	}
}

func (r localExecutableResolver) resolvePythonCandidates(candidates []localExecutable) (localExecutable, error) {
	checked := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		checked = append(checked, candidate.Source)
		if path, ok := r.lookup(candidate.Path); ok {
			candidate.Path = path
			if r.isSupportedPython(candidate) {
				return candidate, nil
			}
		}
	}
	return localExecutable{}, &localCapabilityError{
		Capability: "python",
		Checked:    checked,
		Reason:     fmt.Sprintf("no Python %d.%d+ executable was found", minimumPythonMajor, minimumPythonMinor),
	}
}

func (r localExecutableResolver) resolvePython(configured string) (localExecutable, error) {
	r = r.withDefaults()
	return r.resolvePythonForRepo(configured, r.getenv("AGENT_REPO_ROOT"))
}

func (r localExecutableResolver) resolvePythonForRepo(configured, repoRoot string) (localExecutable, error) {
	r = r.withDefaults()
	if value := strings.TrimSpace(r.getenv("AGENT_PYTHON_BIN")); value != "" {
		return r.resolveExplicitPython("AGENT_PYTHON_BIN", value, nil)
	}
	if repoRoot := strings.TrimSpace(repoRoot); repoRoot != "" {
		projectPython := filepath.Join(repoRoot, ".tools", "venv", "bin", "python")
		if r.goos == "windows" {
			projectPython = filepath.Join(repoRoot, ".tools", "venv", "Scripts", "python.exe")
		}
		if path, ok := r.lookup(projectPython); ok {
			python := localExecutable{Path: path, Source: "project:.tools/venv"}
			if r.isSupportedPython(python) {
				return python, nil
			}
		}
	}
	if value := strings.TrimSpace(configured); value != "" {
		return r.resolveExplicitPython("tool_runtime.executables.python", value, nil)
	}
	if value := strings.TrimSpace(r.getenv("PYTHON_BIN")); value != "" {
		return r.resolveExplicitPython("PYTHON_BIN", value, nil)
	}

	candidates := []localExecutable{
		{Path: "python", Source: "PATH:python"},
		{Path: "python3", Source: "PATH:python3"},
	}
	if r.goos == "windows" {
		candidates = []localExecutable{
			{Path: "py.exe", PrefixArgs: []string{"-3"}, Source: "PATH:py.exe"},
			{Path: "python.exe", Source: "PATH:python.exe"},
			{Path: "python3.exe", Source: "PATH:python3.exe"},
		}
	}
	return r.resolvePythonCandidates(candidates)
}

func shellPrefixArgs(goos, path string) []string {
	if goos != "windows" {
		return []string{"-lc"}
	}
	base := strings.ToLower(filepath.Base(path))
	if strings.Contains(base, "bash") || strings.Contains(base, "zsh") || base == "sh" || base == "sh.exe" {
		return []string{"-lc"}
	}
	if base == "cmd" || base == "cmd.exe" {
		return []string{"/D", "/S", "/C"}
	}
	return []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command"}
}

func (r localExecutableResolver) resolveShell(configured string) (localExecutable, error) {
	r = r.withDefaults()
	if value := strings.TrimSpace(r.getenv("AGENT_SHELL_BIN")); value != "" {
		return r.resolveExplicit("shell", "AGENT_SHELL_BIN", value, shellPrefixArgs(r.goos, value))
	}
	if value := strings.TrimSpace(configured); value != "" {
		return r.resolveExplicit("shell", "tool_runtime.executables.shell", value, shellPrefixArgs(r.goos, value))
	}

	var names []string
	if r.goos == "windows" {
		// Prefer PowerShell 7: it supports modern syntax such as && and has
		// reliable UTF-8 behavior. Windows PowerShell 5.1 remains a fallback
		// for hosts that do not have pwsh installed.
		names = []string{"pwsh.exe", "powershell.exe"}
	} else {
		names = []string{"zsh", "bash", "sh"}
	}
	candidates := make([]localExecutable, 0, len(names))
	for _, name := range names {
		candidates = append(candidates, localExecutable{
			Path:       name,
			PrefixArgs: shellPrefixArgs(r.goos, name),
			Source:     "PATH:" + name,
		})
	}
	return r.resolveCandidates("shell", candidates)
}

func (r localExecutableResolver) resolveBrowser(configured string) (localExecutable, error) {
	r = r.withDefaults()
	if value := strings.TrimSpace(r.getenv("AGENT_BROWSER_BIN")); value != "" {
		return r.resolveExplicit("browser", "AGENT_BROWSER_BIN", value, nil)
	}
	if value := strings.TrimSpace(configured); value != "" {
		return r.resolveExplicit("browser", "tool_runtime.executables.browser", value, nil)
	}

	candidates := browserCandidates(r.goos, r.getenv)
	return r.resolveCandidates("browser", candidates)
}

// resolveNodeForRepo finds the Node runtime used by the embedded Playwright
// bridge. The bridge is resolved from the repository root, so prefer the
// project-local runtime before falling back to the host PATH.
func (r localExecutableResolver) resolveNodeForRepo(configured, repoRoot string) (localExecutable, error) {
	r = r.withDefaults()
	if value := strings.TrimSpace(r.getenv("AGENT_NODE_BIN")); value != "" {
		return r.resolveExplicit("node", "AGENT_NODE_BIN", value, nil)
	}
	if repoRoot := strings.TrimSpace(repoRoot); repoRoot != "" {
		projectNode := filepath.Join(repoRoot, ".tools", "node", "node")
		if r.goos == "windows" {
			projectNode = filepath.Join(repoRoot, ".tools", "node", "node.exe")
		}
		if path, ok := r.lookup(projectNode); ok {
			return localExecutable{Path: path, Source: "project:.tools/node"}, nil
		}
	}
	if value := strings.TrimSpace(configured); value != "" {
		return r.resolveExplicit("node", "tool_runtime.executables.node", value, nil)
	}
	if value := strings.TrimSpace(r.getenv("NODE_BIN")); value != "" {
		return r.resolveExplicit("node", "NODE_BIN", value, nil)
	}
	return r.resolveCandidates("node", []localExecutable{
		{Path: "node.exe", Source: "PATH:node.exe"},
		{Path: "node", Source: "PATH:node"},
	})
}

func browserCandidates(goos string, getenv func(string) string) []localExecutable {
	if getenv == nil {
		getenv = os.Getenv
	}
	var names []string
	switch goos {
	case "windows":
		names = []string{"chrome.exe", "msedge.exe", "chromium.exe"}
	case "darwin":
		names = []string{"google-chrome", "chromium", "microsoft-edge"}
	default:
		names = []string{"google-chrome", "chromium", "chromium-browser", "microsoft-edge", "microsoft-edge-stable"}
	}
	candidates := make([]localExecutable, 0, len(names)+8)
	for _, name := range names {
		candidates = append(candidates, localExecutable{Path: name, Source: "PATH:" + name})
	}

	var paths []string
	switch goos {
	case "darwin":
		paths = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
	case "windows":
		roots := []string{getenv("ProgramFiles"), getenv("ProgramFiles(x86)"), getenv("LOCALAPPDATA")}
		for _, root := range roots {
			if strings.TrimSpace(root) == "" {
				continue
			}
			paths = append(paths,
				filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe"),
				filepath.Join(root, "Microsoft", "Edge", "Application", "msedge.exe"),
			)
		}
	default:
		paths = []string{
			"/usr/bin/google-chrome",
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
			"/usr/bin/microsoft-edge",
		}
	}
	for _, path := range paths {
		candidates = append(candidates, localExecutable{Path: path, Source: "file:" + path})
	}
	return candidates
}

func localUnavailableResult(toolName string, err error) ToolResult {
	value := map[string]any{
		"tool":  toolName,
		"ok":    false,
		"code":  "unavailable",
		"error": err.Error(),
	}
	var capabilityErr *localCapabilityError
	if errors.As(err, &capabilityErr) {
		value["checked"] = append([]string(nil), capabilityErr.Checked...)
	}
	return ToolResult{Value: value, IsError: true}
}
