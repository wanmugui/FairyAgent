package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SessionEventLog is an append-only JSONL log of durable session events
// (user/assistant/tool/summary). It mirrors the harness-style session log:
// events are written as they happen and can be replayed to reconstruct
// messages even if the snapshot session JSON is lost or truncated.
type SessionEventLog struct {
	path string
	file *os.File
}

// OpenSessionEventLog opens (creating if needed) <session dir>/events.jsonl in
// append mode.
func OpenSessionEventLog(sessionFile string) (*SessionEventLog, error) {
	dir := filepath.Dir(sessionFile)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "events.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &SessionEventLog{path: path, file: f}, nil
}

// Path returns the on-disk location ("" for a nil receiver).
func (l *SessionEventLog) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Append writes one event as a JSON line and flushes it to disk. Nil-safe.
func (l *SessionEventLog) Append(event map[string]any) error {
	if l == nil || l.file == nil {
		return nil
	}
	if event == nil {
		event = map[string]any{}
	}
	if _, ok := event["ts"]; !ok {
		event["ts"] = time.Now().UnixMilli()
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := l.file.Write(append(data, '\n')); err != nil {
		return err
	}
	return l.file.Sync()
}

// Close flushes and closes the underlying file. Nil-safe.
func (l *SessionEventLog) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// ReplaySessionEvents reconstructs flat messages from the append-only event
// log. Only "message" events with a role are replayed; other event kinds
// (tool/summary/turn_end) are skipped.
func ReplaySessionEvents(sessionFile string) ([]Message, error) {
	path := filepath.Join(filepath.Dir(sessionFile), "events.jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var messages []Message
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ev map[string]any
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if ev["type"] != "message" {
			continue
		}
		role, _ := ev["role"].(string)
		content, _ := ev["content"].(string)
		if role != "" {
			m := NewMessage(role, content, nil, "", "")
			if step, ok := ev["step"].(float64); ok {
				m.Step = int(step)
			}
			switch v := ev["ts"].(type) {
			case float64:
				m.Ts = int64(v)
			case string:
				if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
					m.Ts = t.UnixMilli()
				}
			}
			messages = append(messages, m)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return messages, nil
}

// SaveTrace appends this run's trace events to <session>.trace.jsonl.
//
// That file is what the workbench's trace panel reads. Nothing wrote it any
// more - only readers survived a refactor - so the panel had nothing to show:
// an operator could watch a conversation compact its context and never see a
// single entry about it. Events accumulate across runs (each run appends), which
// is what the readers assume.
func SaveTrace(sessionFile string, trace []map[string]interface{}) error {
	if strings.TrimSpace(sessionFile) == "" || len(trace) == 0 {
		return nil
	}
	dir := filepath.Dir(sessionFile)
	base := strings.TrimSuffix(filepath.Base(sessionFile), filepath.Ext(sessionFile))
	path := filepath.Join(dir, base+".trace.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := bufio.NewWriter(file)
	for _, event := range trace {
		if event == nil {
			continue
		}
		data, err := json.Marshal(event)
		if err != nil {
			continue
		}
		if _, err := writer.Write(data); err != nil {
			return err
		}
		if err := writer.WriteByte('\n'); err != nil {
			return err
		}
	}
	return writer.Flush()
}
