package local

import (
	"path/filepath"
	"strings"
)

// commandForShell applies a small, deterministic preamble to PowerShell
// invocations. The tool keeps the name `bash` for compatibility, but on
// Windows it is a PowerShell contract; fixing the output encoding here avoids
// the mojibake that otherwise makes native stderr unreadable.
func commandForShell(shell localExecutable, command string) string {
	if !isPowerShellExecutable(shell.Path) {
		return command
	}
	const preamble = "try { [Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false) } catch { }; " +
		"$OutputEncoding = [System.Text.UTF8Encoding]::new($false); "
	return preamble + command
}

func isPowerShellExecutable(path string) bool {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(path)))
	return base == "pwsh" || base == "pwsh.exe" || base == "powershell" || base == "powershell.exe"
}
