package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// runInjectStdinReader consumes JSON-line commands from os.Stdin and forwards
// them to controller. Wire format (one JSON object per line, '\n' terminated):
//   {"op":"inject","text":"..."}
//   {"op":"cancel"}
//
// The reader blocks on its own loop until ctx is cancelled or stdin returns
// EOF. Empty lines and malformed JSON are skipped silently so a noisy caller
// cannot wedge the agent loop.
//
// This replaced the earlier UDP scheme because dgram.bind's async-not-sync
// semantics leaked EBADF into the host process's socket pool.
func runInjectStdinReader(ctx context.Context, controller *AgentController) error {
	if !stdinInjectFlag {
		return nil
	}
	go func() {
		reader := bufio.NewReader(os.Stdin)
		for {
			if ctx.Err() != nil {
				return
			}
			line, readErr := reader.ReadString('\n')
			if line != "" {
				cmd := parseCommandLine(strings.TrimRight(line, "\r\n"))
				switch cmd.Op {
				case "inject":
					if cmd.Text != "" {
						controller.InjectMessage(cmd.Text)
					}
				case "stop":
					// Soft stop: finish the current step and drain any
					// queued user messages into a fresh turn before exiting.
					controller.SoftStop()
				case "cancel":
					controller.Cancel()
				}
			}
			if readErr != nil {
				if readErr == io.EOF {
					return
				}
				// Treat any other read error as fatal for the listener.
				fmt.Fprintf(os.Stderr, "[harness] WARN: inject stdin read: %v\n", readErr)
				return
			}
		}
	}()
	return nil
}

type injectCommand struct {
	Op   string `json:"op"`
	Text string `json:"text"`
}

// stdinInjectFlag is set by main.go from the -InjectStdin flag.
var stdinInjectFlag bool

func parseCommandLine(line string) injectCommand {
	var cmd injectCommand
	if err := json.Unmarshal([]byte(line), &cmd); err != nil {
		return injectCommand{}
	}
	return cmd
}