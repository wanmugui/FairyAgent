package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type stdioTransport struct {
	name    string
	command *exec.Cmd
	stdin   io.WriteCloser

	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[string]chan rpcResponse
	nextID  int64
	done    chan struct{}
	closed  bool
	waitCh  chan error
	close   sync.Once
}

func newStdioTransport(options Options) (*stdioTransport, error) {
	if strings.TrimSpace(options.Command) == "" {
		return nil, fmt.Errorf("MCP server %q requires command for stdio transport", options.Name)
	}
	cmd := exec.Command(options.Command, options.Args...)
	if strings.TrimSpace(options.Cwd) != "" {
		cmd.Dir = options.Cwd
	}
	cmd.Env = mergeEnvironment(options.Env)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open MCP server %q stdin: %w", options.Name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open MCP server %q stdout: %w", options.Name, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("open MCP server %q stderr: %w", options.Name, err)
	}
	transport := &stdioTransport{
		name:    options.Name,
		command: cmd,
		stdin:   stdin,
		pending: make(map[string]chan rpcResponse),
		done:    make(chan struct{}),
		waitCh:  make(chan error, 1),
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start MCP server %q: %w", options.Name, err)
	}
	go transport.readLoop(stdout)
	go transport.logStderr(stderr)
	go func() { transport.waitCh <- cmd.Wait() }()
	return transport, nil
}

func mergeEnvironment(extra map[string]string) []string {
	env := append([]string(nil), os.Environ()...)
	keys := make([]string, 0, len(extra))
	for key := range extra {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		env = append(env, key+"="+extra[key])
	}
	return env
}

func (t *stdioTransport) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := rpcRequestID(atomic.AddInt64(&t.nextID, 1))
	responseCh := make(chan rpcResponse, 1)
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, fmt.Errorf("MCP server %q is closed", t.name)
	}
	t.pending[id] = responseCh
	t.mu.Unlock()

	if err := t.write(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		t.removePending(id)
		return nil, err
	}

	select {
	case response := <-responseCh:
		return unwrapRPCResponse(response)
	case <-ctx.Done():
		t.removePending(id)
		return nil, ctx.Err()
	case <-t.done:
		// If the response arrived at the same time as shutdown, prefer the
		// queued response instead of racing into a false disconnect.
		select {
		case response := <-responseCh:
			return unwrapRPCResponse(response)
		default:
			t.removePending(id)
			return nil, fmt.Errorf("MCP server %q disconnected", t.name)
		}
	}
}

func (t *stdioTransport) Notify(ctx context.Context, method string, params any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t.mu.Lock()
	closed := t.closed
	t.mu.Unlock()
	if closed {
		return fmt.Errorf("MCP server %q is closed", t.name)
	}
	return t.write(rpcRequest{JSONRPC: "2.0", Method: method, Params: params})
}

func unwrapRPCResponse(response rpcResponse) (json.RawMessage, error) {
	if response.Error != nil {
		return nil, response.Error
	}
	return response.Result, nil
}

func (t *stdioTransport) write(request rpcRequest) error {
	payload, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("marshal MCP request: %w", err)
	}
	payload = append(payload, '\n')
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if _, err := t.stdin.Write(payload); err != nil {
		return fmt.Errorf("write MCP request to %q: %w", t.name, err)
	}
	return nil
}

func (t *stdioTransport) readLoop(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var response rpcResponse
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			log.Printf("[mcp:%s] ignored invalid JSON-RPC message: %v", t.name, err)
			continue
		}
		key := rpcIDKey(response.ID)
		if key == "" {
			continue
		}
		t.mu.Lock()
		responseCh := t.pending[key]
		delete(t.pending, key)
		t.mu.Unlock()
		if responseCh != nil {
			responseCh <- response
		}
	}
	if err := scanner.Err(); err != nil {
		t.shutdown(fmt.Errorf("read MCP server %q stdout: %w", t.name, err))
		return
	}
	t.shutdown(fmt.Errorf("MCP server %q stdout closed", t.name))
}

func (t *stdioTransport) logStderr(stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 16*1024), 1024*1024)
	for scanner.Scan() {
		log.Printf("[mcp:%s] %s", t.name, scanner.Text())
	}
}

func (t *stdioTransport) removePending(id string) {
	t.mu.Lock()
	delete(t.pending, id)
	t.mu.Unlock()
}

func (t *stdioTransport) shutdown(err error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	close(t.done)
	for id := range t.pending {
		delete(t.pending, id)
	}
	t.mu.Unlock()
	if err != nil {
		log.Printf("[mcp:%s] %v", t.name, err)
	}
}

func (t *stdioTransport) Close() error {
	var closeErr error
	t.close.Do(func() {
		t.shutdown(nil)
		_ = t.stdin.Close()
		select {
		case <-t.waitCh:
		case <-time.After(2 * time.Second):
			if t.command.Process != nil {
				closeErr = t.command.Process.Kill()
			}
			<-t.waitCh
		}
	})
	return closeErr
}
