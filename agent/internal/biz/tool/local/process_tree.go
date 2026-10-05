package local

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
)

func terminateProcessTree(process *os.Process) error {
	if process == nil {
		return nil
	}
	if runtime.GOOS == "windows" {
		kill := exec.Command("taskkill", "/PID", strconv.Itoa(process.Pid), "/T", "/F")
		if err := kill.Run(); err == nil {
			return nil
		}
	}
	return process.Kill()
}
