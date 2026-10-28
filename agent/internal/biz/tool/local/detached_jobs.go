package local

import (
	"io"
	"os/exec"
	"sync"
)

// detachedJobRegistry tracks detached background processes spawned by the
// bash tool so the bash_job tool can read their streams or kill them. One
// registry per process is fine; tool calls are serialized by the agent loop.
type detachedJob struct {
	cmd      *exec.Cmd
	stdout   io.Closer
	stderr   io.Closer
	cancelFn func() error
}

var (
	detachedMu  sync.Mutex
	detachedMap = make(map[string]*detachedJob)
)

func registerDetachedJob(id string, cmd *exec.Cmd, stdout, stderr io.Closer, cancel func() error) {
	if id == "" {
		return
	}
	detachedMu.Lock()
	defer detachedMu.Unlock()
	detachedMap[id] = &detachedJob{cmd: cmd, stdout: stdout, stderr: stderr, cancelFn: cancel}
}

func unregisterDetachedJob(id string) {
	if id == "" {
		return
	}
	detachedMu.Lock()
	defer detachedMu.Unlock()
	delete(detachedMap, id)
}

// lookupDetachedJob fetches a job without holding the lock across the call.
func lookupDetachedJob(id string) (*detachedJob, bool) {
	detachedMu.Lock()
	defer detachedMu.Unlock()
	job, ok := detachedMap[id]
	return job, ok
}

// killDetachedJob sends SIGKILL to the running process and unregisters it. Safe
// to call repeatedly; missing jobs are not an error.
func killDetachedJob(id string) error {
	detachedMu.Lock()
	job, ok := detachedMap[id]
	if ok {
		delete(detachedMap, id)
	}
	detachedMu.Unlock()
	if !ok {
		return nil
	}
	if job.cancelFn != nil {
		return job.cancelFn()
	}
	if job.cmd != nil && job.cmd.Process != nil {
		return terminateProcessTree(job.cmd.Process)
	}
	return nil
}
