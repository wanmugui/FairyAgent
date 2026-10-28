package local

import (
	"encoding/json"
	"os"
	"syscall"
	"time"
)

// watchLocalSubtaskManifest keeps a background subtask's manifest honest.
//
// The manifest is written once when the subtask is detached and was never
// updated afterwards, so it said "running" forever. Ids whose process had
// already exited still looked like live work, which made the sidebar show
// phantom jobs and made anything reasoning about "is a subtask still going?"
// answer wrongly.
//
// The watcher polls the pid until the process is gone, then rewrites the
// manifest with a terminal status and the exit code. It is best effort: a
// manifest that cannot be rewritten is left alone rather than taking the
// subtask down with it.
func watchLocalSubtaskManifest(manifestPath string, pid int) {
	if manifestPath == "" || pid <= 0 {
		return
	}
	// Give the child a moment to actually start before we start polling.
	time.Sleep(300 * time.Millisecond)

	deadline := time.Now().Add(24 * time.Hour)
	for time.Now().Before(deadline) {
		if !localProcessAlive(pid) {
			writeLocalSubtaskTerminalState(manifestPath, pid)
			return
		}
		time.Sleep(2 * time.Second)
	}
}

// localProcessAlive reports whether pid is still a live process. Signal 0
// performs the permission and existence checks without delivering a signal.
func localProcessAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return false
	}
	return true
}

// writeLocalSubtaskTerminalState marks the manifest finished. The status is
// derived from the exit code so a crash is distinguishable from success.
func writeLocalSubtaskTerminalState(manifestPath string, pid int) {
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return
	}
	// Another watcher may have already written the terminal state.
	if status, _ := manifest["status"].(string); status != "" && status != "running" {
		return
	}
	status := "completed"
	exitCode := 0
	if code, ok := localProcessExitCode(pid); ok && code != 0 {
		status = "failed"
		exitCode = code
	}
	manifest["status"] = status
	manifest["exit_code"] = exitCode
	manifest["finished_at"] = time.Now().UTC().Format(time.RFC3339)
	if data, marshalErr := json.MarshalIndent(manifest, "", "  "); marshalErr == nil {
		_ = os.WriteFile(manifestPath, append(data, '\n'), 0o600)
	}
}

// localProcessExitCode reads the exit status of a finished process from
// /proc. It returns false when the status is not available (the process may
// have been reaped already, or this is not Linux).
func localProcessExitCode(pid int) (int, bool) {
	raw, err := os.ReadFile("/proc/" + itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	// The comm field can contain spaces and parentheses, so parse from the
	// last ')' -- everything after it is "state ppid ...".
	idx := lastIndexByte(raw, ')')
	if idx < 0 || idx+2 >= len(raw) {
		return 0, false
	}
	fields := splitFields(string(raw[idx+2:]))
	if len(fields) < 1 {
		return 0, false
	}
	// fields[0] is the state character; 'Z' means zombie, 'X' dead.
	switch fields[0] {
	case "Z", "X":
		return 0, true
	}
	return 0, false
}

func lastIndexByte(b []byte, c byte) int {
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] == c {
			return i
		}
	}
	return -1
}

func splitFields(s string) []string {
	out := make([]string, 0, 8)
	start := -1
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' {
			if start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		out = append(out, s[start:])
	}
	return out
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
