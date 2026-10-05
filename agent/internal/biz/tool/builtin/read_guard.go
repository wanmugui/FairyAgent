package builtin

import (
	"strings"
	"sync"
)

// readBeforeEdit tracks which files were successfully read in which session,
// so edit_file can enforce read-before-write (the schema says the model must
// read first; this makes it a hard check instead of a prompt-only rule).
// Keyed by session file; an empty session key is shared across sessions.
var (
	readBeforeEditMu    sync.Mutex
	readBeforeEditSeen = map[string]map[string]bool{}
)

func markFileRead(sessionFile, fullPath string) {
	readBeforeEditMu.Lock()
	defer readBeforeEditMu.Unlock()
	m, ok := readBeforeEditSeen[sessionFile]
	if !ok {
		m = map[string]bool{}
		readBeforeEditSeen[sessionFile] = m
	}
	m[fullPath] = true
}

func wasFileRead(sessionFile, fullPath string) bool {
	readBeforeEditMu.Lock()
	defer readBeforeEditMu.Unlock()
	return readBeforeEditSeen[sessionFile][fullPath]
}

// resolveReadSessionKey collapses an empty session file into one shared key
// so the check still works when the harness doesn't thread a session file.
func resolveReadSessionKey(sessionFile string) string {
	return strings.TrimSpace(sessionFile)
}
