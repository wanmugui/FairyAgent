package local

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func resolverForTest(goos string, env, commands map[string]string, files map[string]bool) localExecutableResolver {
	return localExecutableResolver{
		goos: goos,
		getenv: func(key string) string {
			return env[key]
		},
		lookPath: func(name string) (string, error) {
			if path := commands[name]; path != "" {
				return path, nil
			}
			return "", errors.New("not found")
		},
		isFile: func(path string) bool {
			return files[path]
		},
		pythonVersion: func(localExecutable) (localPythonVersion, error) {
			return localPythonVersion{Major: 3, Minor: 10}, nil
		},
	}
}

func TestLocalExecutableResolverPythonUsesEnvironmentBeforeConfig(t *testing.T) {
	resolver := resolverForTest("darwin", map[string]string{
		"AGENT_PYTHON_BIN": "/env/python",
	}, nil, map[string]bool{"/env/python": true, "/config/python": true})

	got, err := resolver.resolvePython("/config/python")
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "/env/python" || got.Source != "AGENT_PYTHON_BIN" {
		t.Fatalf("unexpected Python selection: %#v", got)
	}
}

func TestLocalExecutableResolverPythonUsesConfigBeforeLegacyEnvironment(t *testing.T) {
	resolver := resolverForTest("darwin", map[string]string{
		"PYTHON_BIN": "/legacy/python",
	}, nil, map[string]bool{"/config/python": true, "/legacy/python": true})

	got, err := resolver.resolvePython("/config/python")
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "/config/python" || got.Source != "tool_runtime.executables.python" {
		t.Fatalf("unexpected Python selection: %#v", got)
	}
}

func TestLocalExecutableResolverPythonUsesProjectVenvBeforeConfig(t *testing.T) {
	repoRoot := t.TempDir()
	projectPython := filepath.Join(repoRoot, ".tools", "venv", "bin", "python")
	resolver := resolverForTest("darwin", map[string]string{
		"AGENT_REPO_ROOT": repoRoot,
	}, nil, map[string]bool{projectPython: true, "/config/python": true})

	got, err := resolver.resolvePython("/config/python")
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != projectPython || got.Source != "project:.tools/venv" {
		t.Fatalf("unexpected Python selection: %#v", got)
	}
}

func TestLocalExecutableResolverExplicitInvalidPythonDoesNotFallback(t *testing.T) {
	resolver := resolverForTest("darwin", map[string]string{
		"AGENT_PYTHON_BIN": "/missing/python",
	}, map[string]string{"python3": "/usr/bin/python3"}, nil)

	_, err := resolver.resolvePython("")
	if err == nil || !strings.Contains(err.Error(), "AGENT_PYTHON_BIN") || !strings.Contains(err.Error(), "/missing/python") {
		t.Fatalf("expected authoritative explicit-path error, got %v", err)
	}
}

func TestLocalExecutableResolverPrefersActivatedPythonFromPath(t *testing.T) {
	resolver := resolverForTest("darwin", nil, map[string]string{
		"python3": "/usr/local/bin/python3",
		"python":  "/usr/local/bin/python",
	}, nil)

	got, err := resolver.resolvePython("")
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "/usr/local/bin/python" || got.Source != "PATH:python" {
		t.Fatalf("unexpected Python candidate: %#v", got)
	}
}

func TestLocalExecutableResolverExplicitPythonRequiresMinimumVersion(t *testing.T) {
	resolver := resolverForTest("darwin", map[string]string{
		"AGENT_PYTHON_BIN": "/env/python",
	}, nil, map[string]bool{"/env/python": true})
	resolver.pythonVersion = func(localExecutable) (localPythonVersion, error) {
		return localPythonVersion{Major: 3, Minor: 9}, nil
	}

	_, err := resolver.resolvePython("")
	if err == nil || !strings.Contains(err.Error(), "AGENT_PYTHON_BIN") || !strings.Contains(err.Error(), "Python 3.10+") {
		t.Fatalf("expected minimum-version error, got %v", err)
	}
}

func TestLocalExecutableResolverSkipsUnsupportedPythonFromPath(t *testing.T) {
	resolver := resolverForTest("darwin", nil, map[string]string{
		"python":  "/usr/local/bin/python",
		"python3": "/usr/local/bin/python3",
	}, nil)
	resolver.pythonVersion = func(python localExecutable) (localPythonVersion, error) {
		if python.Path == "/usr/local/bin/python" {
			return localPythonVersion{Major: 3, Minor: 9}, nil
		}
		return localPythonVersion{Major: 3, Minor: 10}, nil
	}

	got, err := resolver.resolvePython("")
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "/usr/local/bin/python3" || got.Source != "PATH:python3" {
		t.Fatalf("unexpected Python candidate: %#v", got)
	}
}

func TestLocalExecutableResolverWindowsPythonLauncherAddsVersionArgument(t *testing.T) {
	resolver := resolverForTest("windows", nil, map[string]string{
		"py.exe": `C:\Windows\py.exe`,
	}, nil)

	got, err := resolver.resolvePython("")
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != `C:\Windows\py.exe` || !reflect.DeepEqual(got.PrefixArgs, []string{"-3"}) {
		t.Fatalf("unexpected Windows Python launcher: %#v", got)
	}
}

func TestLocalExecutableResolverSelectsPlatformShell(t *testing.T) {
	unixResolver := resolverForTest("darwin", nil, map[string]string{"zsh": "/bin/zsh"}, nil)
	unixShell, err := unixResolver.resolveShell("")
	if err != nil {
		t.Fatal(err)
	}
	if unixShell.Path != "/bin/zsh" || !reflect.DeepEqual(unixShell.PrefixArgs, []string{"-lc"}) {
		t.Fatalf("unexpected Unix shell: %#v", unixShell)
	}

	windowsResolver := resolverForTest("windows", nil, map[string]string{"powershell.exe": `C:\Windows\powershell.exe`}, nil)
	windowsShell, err := windowsResolver.resolveShell("")
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command"}
	if windowsShell.Path != `C:\Windows\powershell.exe` || !reflect.DeepEqual(windowsShell.PrefixArgs, wantArgs) {
		t.Fatalf("unexpected Windows shell: %#v", windowsShell)
	}
}

func TestLocalExecutableResolverPrefersPowerShell7OnWindows(t *testing.T) {
	resolver := resolverForTest("windows", nil, map[string]string{
		"pwsh.exe":       `C:\Program Files\PowerShell\7\pwsh.exe`,
		"powershell.exe": `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`,
	}, nil)

	got, err := resolver.resolveShell("")
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != `C:\Program Files\PowerShell\7\pwsh.exe` || !reflect.DeepEqual(got.PrefixArgs, []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command"}) {
		t.Fatalf("expected PowerShell 7 first, got %#v", got)
	}
}

func TestLocalExecutableResolverFindsMacBrowserApplication(t *testing.T) {
	chrome := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	resolver := resolverForTest("darwin", nil, nil, map[string]bool{chrome: true})

	got, err := resolver.resolveBrowser("")
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != chrome || !strings.Contains(got.Source, "Google Chrome") {
		t.Fatalf("unexpected browser: %#v", got)
	}
}

func TestLocalExecutableResolverUnavailableListsCheckedCandidates(t *testing.T) {
	resolver := resolverForTest("linux", nil, nil, nil)

	_, err := resolver.resolveBrowser("")
	var unavailable *localCapabilityError
	if !errors.As(err, &unavailable) {
		t.Fatalf("expected localCapabilityError, got %T %v", err, err)
	}
	if unavailable.Capability != "browser" || len(unavailable.Checked) == 0 {
		t.Fatalf("unexpected unavailable details: %#v", unavailable)
	}
}
