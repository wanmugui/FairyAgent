package local

import (
	"bytes"
	"fmt"
	"os"
)

const defaultLocalOutputLimitBytes int64 = 64 * 1024

type localCapturedOutput struct {
	Text      string
	Truncated bool
	Path      string
	Bytes     int64
}

// boundedOutputCapture keeps a bounded tail in memory while preserving the
// complete stream in a spill file. Agent-facing output should be useful even
// when a command produces hundreds of megabytes.
type boundedOutputCapture struct {
	limit    int64
	buffer   bytes.Buffer
	total    int64
	file     *os.File
	path     string
	spillErr error
}

func newBoundedOutputCapture(limit int64) *boundedOutputCapture {
	return &boundedOutputCapture{limit: limit}
}

func (c *boundedOutputCapture) Write(p []byte) (int, error) {
	c.total += int64(len(p))
	if c.limit <= 0 {
		_, _ = c.buffer.Write(p)
		return len(p), nil
	}

	_, _ = c.buffer.Write(p)
	if c.total <= c.limit {
		return len(p), nil
	}

	// The first byte beyond the in-memory budget is the point where we create
	// the spill file. The buffer still contains every byte seen so far, so it
	// can be flushed intact before it is trimmed to the retained tail.
	if c.file == nil {
		file, err := os.CreateTemp("", "fairy-bash-*.output")
		if err != nil {
			c.spillErr = err
		} else {
			c.file = file
			c.path = file.Name()
			if _, err := c.file.Write(c.buffer.Bytes()); err != nil && c.spillErr == nil {
				c.spillErr = err
			}
		}
	} else if c.file != nil {
		if _, err := c.file.Write(p); err != nil && c.spillErr == nil {
			c.spillErr = err
		}
	}

	data := c.buffer.Bytes()
	drop := int64(len(data)) - c.limit
	kept := append([]byte(nil), data[drop:]...)
	c.buffer.Reset()
	_, _ = c.buffer.Write(kept)
	return len(p), nil
}

func (c *boundedOutputCapture) Result() localCapturedOutput {
	text := c.buffer.String()
	truncated := c.limit > 0 && c.total > c.limit
	path := ""
	if c.file != nil {
		_ = c.file.Sync()
		_ = c.file.Close()
		c.file = nil
		path = c.path
	}

	if truncated {
		if path != "" {
			text = fmt.Sprintf("[output truncated; showing last %d bytes; full output: %s]\n%s", c.limit, path, text)
		} else {
			text = "[output truncated; full output unavailable]\n" + text
		}
	} else if path != "" {
		_ = os.Remove(path)
		path = ""
	}
	return localCapturedOutput{Text: text, Truncated: truncated, Path: path, Bytes: c.total}
}
