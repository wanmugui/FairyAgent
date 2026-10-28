package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type httpTransport struct {
	name            string
	endpoint        string
	headers         map[string]string
	client          *http.Client
	nextID          int64
	sessionMu       sync.RWMutex
	sessionID       string
	protocolMu      sync.RWMutex
	protocolVersion string
}

func newHTTPTransport(options Options) (*httpTransport, error) {
	if strings.TrimSpace(options.URL) == "" {
		return nil, fmt.Errorf("MCP server %q requires url for HTTP transport", options.Name)
	}
	return &httpTransport{
		name:     options.Name,
		endpoint: strings.TrimSpace(options.URL),
		headers:  options.Headers,
		client:   &http.Client{},
	}, nil
}

func (t *httpTransport) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := rpcRequestID(atomic.AddInt64(&t.nextID, 1))
	response, err := t.send(ctx, rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}, true)
	if err != nil {
		return nil, err
	}
	if response.Error != nil {
		return nil, response.Error
	}
	return response.Result, nil
}

func (t *httpTransport) Notify(ctx context.Context, method string, params any) error {
	_, err := t.send(ctx, rpcRequest{JSONRPC: "2.0", Method: method, Params: params}, false)
	return err
}

func (t *httpTransport) send(ctx context.Context, request rpcRequest, expectResponse bool) (rpcResponse, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return rpcResponse{}, fmt.Errorf("marshal MCP HTTP request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(payload))
	if err != nil {
		return rpcResponse{}, fmt.Errorf("create MCP HTTP request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json, text/event-stream")
	t.protocolMu.RLock()
	if t.protocolVersion != "" {
		httpRequest.Header.Set("MCP-Protocol-Version", t.protocolVersion)
	}
	t.protocolMu.RUnlock()
	for key, value := range t.headers {
		httpRequest.Header.Set(key, value)
	}
	t.sessionMu.RLock()
	if t.sessionID != "" {
		httpRequest.Header.Set("Mcp-Session-Id", t.sessionID)
	}
	t.sessionMu.RUnlock()

	response, err := t.client.Do(httpRequest)
	if err != nil {
		return rpcResponse{}, fmt.Errorf("call MCP HTTP server %q: %w", t.name, err)
	}
	defer response.Body.Close()
	if sessionID := strings.TrimSpace(response.Header.Get("Mcp-Session-Id")); sessionID != "" {
		t.sessionMu.Lock()
		t.sessionID = sessionID
		t.sessionMu.Unlock()
	}
	if response.StatusCode == http.StatusAccepted || !expectResponse {
		if response.StatusCode >= 400 {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 32*1024*1024))
			return rpcResponse{}, fmt.Errorf("MCP HTTP server %q returned status %d: %s", t.name, response.StatusCode, strings.TrimSpace(string(body)))
		}
		return rpcResponse{}, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 32*1024*1024))
		return rpcResponse{}, fmt.Errorf("MCP HTTP server %q returned status %d: %s", t.name, response.StatusCode, strings.TrimSpace(string(body)))
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if strings.Contains(contentType, "text/event-stream") {
		return readSSEResponse(response.Body, request.ID)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 32*1024*1024))
	if err != nil {
		return rpcResponse{}, fmt.Errorf("read MCP HTTP response from %q: %w", t.name, err)
	}
	var rpcResponse rpcResponse
	if err := json.Unmarshal(body, &rpcResponse); err != nil {
		return rpcResponse, fmt.Errorf("decode MCP HTTP response from %q: %w", t.name, err)
	}
	return rpcResponse, nil
}

func (t *httpTransport) setProtocolVersion(version string) {
	t.protocolMu.Lock()
	t.protocolVersion = strings.TrimSpace(version)
	t.protocolMu.Unlock()
}

func readSSEResponse(reader io.Reader, requestID any) (rpcResponse, error) {
	expected := fmt.Sprint(requestID)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var dataLines []string
	processEvent := func() (rpcResponse, bool, error) {
		if len(dataLines) == 0 {
			return rpcResponse{}, false, nil
		}
		payload := strings.TrimSpace(strings.Join(dataLines, "\n"))
		dataLines = dataLines[:0]
		if payload == "" || payload == "[DONE]" {
			return rpcResponse{}, false, nil
		}
		var response rpcResponse
		if err := json.Unmarshal([]byte(payload), &response); err != nil {
			return rpcResponse{}, false, nil
		}
		if rpcIDKey(response.ID) == expected {
			return response, true, nil
		}
		return rpcResponse{}, false, nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			response, found, err := processEvent()
			if err != nil {
				return rpcResponse{}, err
			}
			if found {
				return response, nil
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	response, found, err := processEvent()
	if err != nil {
		return rpcResponse{}, err
	}
	if found {
		return response, nil
	}
	if err := scanner.Err(); err != nil {
		return rpcResponse{}, err
	}
	return rpcResponse{}, fmt.Errorf("MCP HTTP response did not contain JSON-RPC id %s", expected)
}

func (t *httpTransport) Close() error {
	t.sessionMu.RLock()
	sessionID := t.sessionID
	t.sessionMu.RUnlock()
	if sessionID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, t.endpoint, nil)
	if err != nil {
		return nil
	}
	request.Header.Set("Mcp-Session-Id", sessionID)
	t.protocolMu.RLock()
	if t.protocolVersion != "" {
		request.Header.Set("MCP-Protocol-Version", t.protocolVersion)
	}
	t.protocolMu.RUnlock()
	response, err := t.client.Do(request)
	if err == nil && response != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
	}
	return nil
}
