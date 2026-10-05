package httptool

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// newUUID returns a random UUID v4 string (RFC 4122 format).
func newUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("agent-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// sessionUUIDCache maps a session file to a stable UUID so every tool call in
// the same agent session reuses the same X-FAIRY-Session-ID (规范：按会话一个
// session id，不同会话不同)，而不是进程级共享一个。
var sessionUUIDCache = struct {
	sync.Mutex
	m map[string]string
}{m: map[string]string{}}

// EnsureSessionID ???????????? session UUID?UUID v4??
// ?????? JSON ????? session_id????????? agent ??????
// ?????????????????sessionFile ???????? UUID?????
func EnsureSessionID(sessionFile string) string {
	if sessionFile == "" {
		return newUUID()
	}
	if id := LoadSessionID(sessionFile); id != "" {
		return id
	}
	id := newUUID()
	saveSessionIDToFile(sessionFile, id)
	return id
}

// LoadSessionID ???? JSON ??? session_id ??????????????????
func LoadSessionID(sessionFile string) string {
	data, err := os.ReadFile(sessionFile)
	if err != nil {
		return ""
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return ""
	}
	if id, _ := doc["session_id"].(string); id != "" {
		return id
	}
	return ""
}

// saveSessionIDToFile ? session_id ????? JSON ???????? messages ?????
func saveSessionIDToFile(sessionFile, id string) {
	data, err := os.ReadFile(sessionFile)
	if err != nil {
		return
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return
	}
	doc["session_id"] = id
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return
	}
	tmp := sessionFile + ".session_id.tmp"
	if err := os.WriteFile(tmp, out, 0644); err != nil {
		return
	}
	_ = os.Rename(tmp, sessionFile)
}

// sessionIDFor returns the stable session UUID for the given session file.
func sessionIDFor(sessionFile string) string {
	if sessionFile == "" {
		return newUUID()
	}
	sessionUUIDCache.Lock()
	defer sessionUUIDCache.Unlock()
	if id, ok := sessionUUIDCache.m[sessionFile]; ok {
		return id
	}
	id := EnsureSessionID(sessionFile)
	sessionUUIDCache.m[sessionFile] = id
	return id
}

type HTTPTool struct {
	name           string
	endpoint       string
	schema         ToolDef
	client         *http.Client
	bearerToken    string
	defaultHeaders map[string]string
	retryCount     int
	retryBackoff   func(attempt int) time.Duration
}

type httpToolRequest struct {
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
	Arguments  string `json:"arguments"`
}

type httpToolResponse struct {
	ToolCallID string `json:"tool_call_id"`
	Result     string `json:"result"`
	Error      string `json:"error,omitempty"`
}

type wrappedHTTPToolResponse struct {
	Code    int              `json:"code"`
	Message string           `json:"message"`
	Details string           `json:"details"`
	Data    httpToolResponse `json:"data"`
}

type httpToolBackendError struct {
	Code         int    `json:"code"`
	Message      string `json:"message"`
	Details      string `json:"details"`
	StreamStatus int    `json:"stream_status,omitempty"`
}

func NewHTTPTool(name, endpoint string, schema ToolDef, timeoutSec int, bearerToken string, headers map[string]string, retryCount int, hostPin string) *HTTPTool {
	if timeoutSec <= 0 {
		timeoutSec = 120
	}
	if retryCount <= 0 {
		retryCount = 1
	}
	defaultHeaders := make(map[string]string, len(headers))
	for key, value := range headers {
		defaultHeaders[key] = value
	}
	pinMap := parseHostPin(hostPin)
	transport := &http.Transport{}
	if len(pinMap) > 0 {
		// DNS pin：把 host 解析到固定 IP（TLS SNI/Host 头保持原 host，证书照常校验），
		// 解决本地 DNS 解析不到 code-dev 等内网网关公网域名的问题。
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			if host, port, err := net.SplitHostPort(addr); err == nil {
				if ip, ok := pinMap[host]; ok {
					addr = net.JoinHostPort(ip, port)
				}
			}
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		}
	}
	return &HTTPTool{
		name:           name,
		endpoint:       endpoint,
		schema:         schema,
		client:         &http.Client{Timeout: time.Duration(timeoutSec) * time.Second, Transport: transport},
		bearerToken:    bearerToken,
		defaultHeaders: defaultHeaders,
		retryCount:     retryCount,
		retryBackoff: func(attempt int) time.Duration {
			return time.Duration(1<<attempt) * time.Second
		},
	}
}

// parseHostPin parses "host=ip,host2=ip2" into a map.
func parseHostPin(s string) map[string]string {
	m := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		if kv := strings.SplitN(strings.TrimSpace(part), "=", 2); len(kv) == 2 {
			m[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}
	return m
}

func (t *HTTPTool) Name() string {
	return t.name
}

func (t *HTTPTool) Schema() ToolDef {
	return t.schema
}

func isNonRetryableScriptTool(name string) bool {
	return name == "bash"
}

func (t *HTTPTool) Execute(ctx context.Context, invocation ToolInvocation) (ToolResult, error) {
	payload := httpToolRequest{
		ToolCallID: invocation.CallID,
		ToolName:   t.name,
		Arguments:  string(invocation.Args),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ToolResult{}, fmt.Errorf("marshal HTTP tool request: %w", err)
	}

	requestCtx := ctx
	cancel := func() {}
	client := *t.client
	if invocation.Timeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, invocation.Timeout)
		client.Timeout = invocation.Timeout
	}
	defer cancel()

	attempts := t.retryCount
	if attempts <= 0 || isNonRetryableScriptTool(t.name) {
		attempts = 1
	}

	for attempt := 0; attempt < attempts; attempt++ {
		if err := requestCtx.Err(); err != nil {
			return ToolResult{}, err
		}

		req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, t.endpoint, bytes.NewReader(body))
		if err != nil {
			return ToolResult{}, fmt.Errorf("create HTTP tool request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if t.bearerToken != "" {
			req.Header.Set("Authorization", "Bearer "+t.bearerToken)
		}
		for key, value := range t.defaultHeaders {
			req.Header.Set(key, value)
		}
		for key, value := range invocation.Metadata {
			req.Header.Set(key, value)
		}
		// Unified tool gateway expects a session context header; add one if the
		// caller did not already supply it (image_generate etc. require it).
		if req.Header.Get("X-FAIRY-Session-ID") == "" {
			req.Header.Set("X-FAIRY-Session-ID", sessionIDFor(invocation.SessionFile))
		}

		resp, err := client.Do(req)
		if err != nil {
			if requestCtx.Err() != nil {
				return ToolResult{}, requestCtx.Err()
			}
			if attempt == attempts-1 {
				return ToolResult{}, fmt.Errorf("HTTP tool %q request failed after %d attempt(s): %w", t.name, attempts, err)
			}
			if err := t.waitBeforeRetry(requestCtx, attempt); err != nil {
				return ToolResult{}, err
			}
			continue
		}

		respBody, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			if attempt == attempts-1 {
				return ToolResult{}, fmt.Errorf("read HTTP tool %q response after %d attempt(s): %w", t.name, attempts, readErr)
			}
			if err := t.waitBeforeRetry(requestCtx, attempt); err != nil {
				return ToolResult{}, err
			}
			continue
		}

		if resp.StatusCode == http.StatusOK {
			return t.parseSuccessResponse(invocation.CallID, respBody)
		}
		if resp.StatusCode == http.StatusGone {
			return parseHTTPGoneResult(respBody), nil
		}

		retryableStatus := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		if retryableStatus && attempt < attempts-1 {
			if err := t.waitBeforeRetry(requestCtx, attempt); err != nil {
				return ToolResult{}, err
			}
			continue
		}

		errorText := strings.TrimSpace(string(respBody))
		if errorText == "" {
			errorText = fmt.Sprintf("HTTP tool returned status %d", resp.StatusCode)
		}
		return ToolResult{
			Value:   map[string]any{"error": errorText, "status": resp.StatusCode},
			IsError: true,
		}, nil
	}

	return ToolResult{}, fmt.Errorf("HTTP tool %q exhausted retries", t.name)
}

func (t *HTTPTool) waitBeforeRetry(ctx context.Context, attempt int) error {
	delay := time.Duration(0)
	if t.retryBackoff != nil {
		delay = t.retryBackoff(attempt)
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (t *HTTPTool) parseSuccessResponse(requestCallID string, body []byte) (ToolResult, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ToolResult{}, fmt.Errorf("parse HTTP tool %q response: %w", t.name, err)
	}

	var response httpToolResponse
	if data, ok := envelope["data"]; ok {
		var wrapped wrappedHTTPToolResponse
		if err := json.Unmarshal(body, &wrapped); err != nil {
			return ToolResult{}, fmt.Errorf("parse wrapped HTTP tool %q response: %w", t.name, err)
		}
		if wrapped.Code != 0 {
			message := strings.TrimSpace(wrapped.Message)
			if wrapped.Details != "" {
				message = strings.TrimSpace(message + ": " + wrapped.Details)
			}
			if message == "" {
				message = fmt.Sprintf("HTTP tool business error code %d", wrapped.Code)
			}
			return ToolResult{Value: map[string]any{"error": message, "code": wrapped.Code}, IsError: true}, nil
		}
		if len(data) == 0 || string(data) == "null" {
			return ToolResult{}, fmt.Errorf("wrapped HTTP tool %q response has no data", t.name)
		}
		response = wrapped.Data
	} else if err := json.Unmarshal(body, &response); err != nil {
		return ToolResult{}, fmt.Errorf("parse direct HTTP tool %q response: %w", t.name, err)
	}

	if response.ToolCallID != requestCallID {
		fmt.Fprintf(os.Stderr, "[harness] WARN: HTTP tool %s call ID mismatch: request=%s response=%s\n", t.name, requestCallID, response.ToolCallID)
	}
	if response.Error != "" {
		return ToolResult{Value: map[string]any{"error": response.Error}, IsError: true}, nil
	}
	return normalizeHTTPToolResult(response.Result), nil
}

func normalizeHTTPToolResult(result string) ToolResult {
	var object map[string]any
	if err := json.Unmarshal([]byte(result), &object); err == nil && object != nil {
		return ToolResult{Value: object}
	}
	return ToolResult{Value: map[string]any{"result": result}}
}

func parseHTTPGoneResult(body []byte) ToolResult {
	var backendErr httpToolBackendError
	if err := json.Unmarshal(body, &backendErr); err == nil {
		message := strings.TrimSpace(backendErr.Message)
		if backendErr.Details != "" {
			message = strings.TrimSpace(message + ": " + backendErr.Details)
		}
		if message == "" {
			message = strings.TrimSpace(string(body))
		}
		return ToolResult{
			Value:        map[string]any{"error": message, "status": http.StatusGone},
			IsError:      true,
			UpstreamCode: backendErr.StreamStatus,
		}
	}
	return ToolResult{
		Value:        map[string]any{"error": strings.TrimSpace(string(body)), "status": http.StatusGone},
		IsError:      true,
		UpstreamCode: http.StatusGone,
	}
}
