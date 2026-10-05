package local

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// The Windows computer bridge is compiled from C# instead of being written in
// PowerShell. PowerShell cannot reliably align three coordinate spaces at once
// (screenshot pixels, window frames, cursor position) on a DPI-scaled desktop,
// which is what made clicks land in the wrong place.
//
//go:embed computer_broker.cs
var computerBrokerSource string

const (
	computerBrokerExecutableName = "FairyComputerBroker.exe"
	computerBrokerSourceName     = "FairyComputerBroker.cs"
	computerBrokerStaleName      = "source.sha256"
)

// resolveWindowsComputerBroker is a test seam. Production code compiles and
// caches the broker under <repo>/.tools/computer-broker/.
var resolveWindowsComputerBroker = ensureWindowsComputerBroker

// executeWindowsBroker runs the compiled C# broker. The boolean reports whether
// the broker produced a usable result; when it is false the caller falls back to
// the legacy PowerShell bridge so desktop control never becomes hard-dependant
// on a C# toolchain being present.
func (t *localComputerTool) executeWindowsBroker(ctx context.Context, payload []byte, workspace string, invocation ToolInvocation) (ToolResult, bool) {
	if strings.TrimSpace(computerBrokerSource) == "" {
		return ToolResult{}, false
	}
	repoRoot := ""
	if t.cfg != nil {
		repoRoot = t.cfg.RepoRoot
	}
	brokerPath, err := resolveWindowsComputerBroker(repoRoot, runtime.GOOS)
	if err != nil {
		return ToolResult{}, false
	}

	timeout := localToolTimeout(invocation, 30, 5*time.Second, 2*time.Minute)
	processResult, err := t.runner.Run(ctx, localProcessRequest{
		Path:    brokerPath,
		Dir:     workspace,
		Stdin:   payload,
		Timeout: timeout,
	})
	if err != nil {
		return ToolResult{}, false
	}
	if processResult.TimedOut {
		return ToolResult{Value: map[string]any{
			"tool":      t.name,
			"ok":        false,
			"code":      "timeout",
			"error":     fmt.Sprintf("computer action timed out after %s", timeout),
			"timed_out": true,
			"stdout":    strings.TrimSpace(processResult.Stdout),
			"stderr":    strings.TrimSpace(processResult.Stderr),
		}, IsError: true}, true
	}

	value, parseErr := parseCuaAutoResult(processResult.Stdout)
	if parseErr != nil {
		return ToolResult{}, false
	}
	value["tool"] = t.name
	ok, _ := value["ok"].(bool)
	return ToolResult{Value: value, IsError: !ok}, true
}

// ensureWindowsComputerBroker compiles computer_broker.cs into the repo-local
// tool cache when the embedded source changed, then returns the executable path.
func ensureWindowsComputerBroker(repoRoot string, goos string) (string, error) {
	if goos != "windows" {
		return "", errors.New("the computer broker is Windows-only")
	}
	root := strings.TrimSpace(repoRoot)
	if root == "" {
		return "", errors.New("repo root is unavailable, so the computer broker cannot be compiled")
	}
	directory := filepath.Join(root, ".tools", "computer-broker")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", fmt.Errorf("create computer broker directory: %w", err)
	}
	executable := filepath.Join(directory, computerBrokerExecutableName)
	stamp := filepath.Join(directory, computerBrokerStaleName)

	expected := computerBrokerSourceHash()
	if cached, err := os.ReadFile(stamp); err == nil && strings.TrimSpace(string(cached)) == expected {
		if info, statErr := os.Stat(executable); statErr == nil && info.Size() > 0 {
			return executable, nil
		}
	}

	source := filepath.Join(directory, computerBrokerSourceName)
	if err := os.WriteFile(source, []byte(computerBrokerSource), 0o644); err != nil {
		return "", fmt.Errorf("write computer broker source: %w", err)
	}
	if err := compileWindowsComputerBroker(source, executable); err != nil {
		return "", err
	}
	if err := os.WriteFile(stamp, []byte(expected), 0o644); err != nil {
		return "", fmt.Errorf("write computer broker stamp: %w", err)
	}
	return executable, nil
}

func computerBrokerSourceHash() string {
	sum := sha256.Sum256([]byte(computerBrokerSource))
	return hex.EncodeToString(sum[:])
}

func compileWindowsComputerBroker(sourcePath string, executablePath string) error {
	compiler, err := findCSharpCompiler()
	if err != nil {
		return err
	}
	references, err := csharpComputerBrokerReferences()
	if err != nil {
		return err
	}
	args := []string{"/nologo", "/target:exe", "/platform:anycpu", "/optimize+", "/out:" + executablePath}
	for _, reference := range references {
		args = append(args, "/r:"+reference)
	}
	args = append(args, sourcePath)

	command := exec.Command(compiler, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("compile computer broker: %s", message)
	}
	info, statErr := os.Stat(executablePath)
	if statErr != nil || info.Size() == 0 {
		return errors.New("compile computer broker: compiler produced no executable")
	}
	return nil
}

func findCSharpCompiler() (string, error) {
	for _, directory := range frameworkDirectories() {
		candidate := filepath.Join(directory, "csc.exe")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	if path, err := exec.LookPath("csc.exe"); err == nil {
		return path, nil
	}
	return "", errors.New("csc.exe is unavailable; .NET Framework 4.x is required to build the computer broker")
}

func frameworkDirectories() []string {
	windir := strings.TrimSpace(os.Getenv("WINDIR"))
	if windir == "" {
		windir = `C:\Windows`
	}
	return []string{
		filepath.Join(windir, "Microsoft.NET", "Framework64", "v4.0.30319"),
		filepath.Join(windir, "Microsoft.NET", "Framework", "v4.0.30319"),
	}
}

func csharpComputerBrokerReferences() ([]string, error) {
	directories := frameworkDirectories()
	frameworkDirectory := ""
	for _, directory := range directories {
		if info, err := os.Stat(directory); err == nil && info.IsDir() {
			frameworkDirectory = directory
			break
		}
	}
	if frameworkDirectory == "" {
		return nil, errors.New("no .NET Framework directory was found")
	}

	references := make([]string, 0, 8)
	// csc.exe already pulls these in through csc.rsp, but listing them keeps the
	// build working when a machine ships a trimmed response file.
	for _, name := range []string{
		"System.dll",
		"System.Core.dll",
		"System.Drawing.dll",
		"System.Windows.Forms.dll",
		"System.Web.Extensions.dll",
	} {
		candidate := filepath.Join(frameworkDirectory, name)
		if _, err := os.Stat(candidate); err == nil {
			references = append(references, candidate)
		}
	}

	windir := strings.TrimSpace(os.Getenv("WINDIR"))
	if windir == "" {
		windir = `C:\Windows`
	}
	gac := filepath.Join(windir, "Microsoft.NET", "assembly", "GAC_MSIL")
	for _, name := range []string{"UIAutomationClient", "UIAutomationTypes", "WindowsBase"} {
		matches, err := filepath.Glob(filepath.Join(gac, name, "v4.0_*", name+".dll"))
		if err != nil {
			return nil, fmt.Errorf("locate reference assembly %s: %w", name, err)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("reference assembly %s.dll was not found under %s", name, gac)
		}
		sort.Strings(matches)
		references = append(references, matches[len(matches)-1])
	}
	return references, nil
}
