package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

// ErrAgentInterrupted is returned by RunAgentLoopCtx when the controller is
// cancelled between steps or mid-flight. The result returned alongside the
// error is still usable (Messages contains everything up to the interrupt
// point), so callers can persist it before deciding the next action.
var ErrAgentInterrupted = errors.New("agent interrupted")

// AgentController is the runtime handle a host process (HTTP server, voice
// page, CLI) uses to interrupt or inject messages into a running agent.
//
// Two distinct interrupt flavours:
//
//   - SoftStop(): the agent finishes the in-flight LLM/tool call, stops at
//     the next step boundary, then DRAINS any pending user messages into
//     a new round before exiting. The session never loses work.
//
//   - Cancel(): cancels the in-flight call mid-stream, then exits. Use when
//     you want to abandon the turn (no the user want to discard it).
//
// InjectMessage appends a user message to the pending queue and emits an
// injected_user SSE event immediately so the UI can echo it without race.
//
// Concurrency: all methods are safe to call from any goroutine.
type AgentController struct {
	cancel  context.CancelFunc
	ctx     context.Context
	mu      sync.Mutex
	pending []pendingUserMessage
	closed  bool
	soft    bool
}

type pendingUserMessage struct {
	id   int64
	text string
}

// NewAgentController builds a controller with its own derived context. The
// returned context is what every cancellable downstream call should adopt.
func NewAgentController(parent context.Context) *AgentController {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	return &AgentController{cancel: cancel, ctx: ctx}
}

// Context returns the cancellable context the loop should propagate.
func (c *AgentController) Context() context.Context { return c.ctx }

// Done returns the hard cancel-c channel.
func (c *AgentController) Done() <-chan struct{} { return c.ctx.Done() }

// SoftStopRequested reports whether SoftStop() has been signalled. The loop
// checks this between steps and exits gracefully after consuming any
// pending user messages, instead of cancelling mid-flight.
func (c *AgentController) SoftStopRequested() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.soft
}

// ResetSoftStop clears the soft-stop flag (used internally when the loop has
// fully drained its queued turns).
func (c *AgentController) ResetSoftStop() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.soft = false
	c.mu.Unlock()
}

// SoftStop signals the loop to stop at the next step boundary. Pending
// user messages are still consumed (drained into a new turn) before exit.
func (c *AgentController) SoftStop() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.soft = true
	c.mu.Unlock()
}

// Cancel triggers Done(). Canc Safe to call repeatedly.
func (c *AgentController) Cancel() {
	if c == nil {
		return
	}
	c.cancel()
}

// InjectMessage appends a user message to the pending queue and emits an
// "injected_user" SSE event immediately. The frontend must NOT also echo the
// message locally; one source of truth (the agent) avoids the duplicate
// rendering seen when both ends pushed simultaneously.
//
// Each emitted event carries a monotonic sequence id; the loop's step-
// boundary emit reuses the same id so the frontend can update in place
// rather than render twice.
func (c *AgentController) InjectMessage(text string) {
	if c == nil || text == "" {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	id := pendingEmitSeq.Add(1)
	c.pending = append(c.pending, pendingUserMessage{id: id, text: text})
	c.mu.Unlock()
	emitEvent("injected_user", map[string]interface{}{
		"id":      id,
		"content": text,
		"pending": true,
	})
}

var pendingEmitSeq atomic.Int64

// PopPending drains all queued messages in arrival order and returns them.
// The boolean ok is true if any message was drained.
func (c *AgentController) PopPending() (string, int64, bool) {
	if c == nil {
		return "", 0, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) == 0 {
		return "", 0, false
	}
	msg := c.pending[0]
	c.pending = c.pending[1:]
	return msg.text, msg.id, true
}

// PeekPending returns a snapshot copy of the queued messages without draining.
// The agent loop uses this to decide between a soft-stop-exit (queue empty)
// and a soft-stop-drain (queue non-empty) before each step.
func (c *AgentController) PeekPending() []string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) == 0 {
		return nil
	}
	out := make([]string, len(c.pending))
	for i, item := range c.pending {
		out[i] = item.text
	}
	return out
}

// Close releases the context. Callers must defer Close after creation.
func (c *AgentController) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	c.cancel()
	c.pending = nil
}
