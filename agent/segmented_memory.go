package main

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	segmentedMemoryDir             = "segmented"
	segmentedMemoryIndexDir        = "index"
	segmentedMemoryVolumeDir       = "volumes"
	segmentedMemorySessionsDir     = "sessions"
	segmentedMemoryInteractionsDir = "interactions"
	segmentedMemoryManifestFile    = "manifest.json"
	// segmentedMemorySegmentDir is kept for reading pre-hierarchy data. New
	// segments are stored under sessions/<YYYY-MM-DD>/interactions/<id>/.
	segmentedMemorySegmentDir = "segments"

	segmentKindUserRequest   = "user_request"
	segmentKindAssistant     = "assistant_reply"
	segmentKindToolOperation = "tool_operation"

	segmentStatusActive      = "active"
	segmentStatusInvalidated = "invalidated"
	segmentStatusDeleted     = "deleted"
)

var shanghaiTimeLocation = func() *time.Location {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err == nil {
		return location
	}
	// Windows installations may not ship the IANA timezone database. Mainland
	// China has used UTC+8 without DST since 1991, so this is an exact fallback.
	return time.FixedZone("Asia/Shanghai", 8*60*60)
}()

// SegmentedMemoryConfig controls the append-only episodic memory store. The
// legacy memory summarizer remains available for durable profile/digest files;
// this store is the low-latency, per-segment layer used during a live turn.
func applySegmentedMemoryDefaults(cfg *Config) {
	if cfg == nil {
		return
	}
	c := &cfg.SegmentedMemory
	if strings.TrimSpace(c.KeyExtraction) == "" {
		c.KeyExtraction = "llm"
	}
	if c.MaxSegmentChars <= 0 {
		c.MaxSegmentChars = 4000
	}
	if c.MaxSegmentSteps <= 0 {
		c.MaxSegmentSteps = 8
	}
	if c.MaxRootIndexEntries <= 0 {
		c.MaxRootIndexEntries = 80
	}
	if c.PromptKeyLimit <= 0 {
		c.PromptKeyLimit = 80
	}
	if c.KeyExtractionTimeout <= 0 {
		c.KeyExtractionTimeout = 20
	}
	if c.KeyWaitTimeout <= 0 {
		c.KeyWaitTimeout = 3
	}
	if c.MaxConcurrentKeys <= 0 {
		c.MaxConcurrentKeys = 2
	}
}

type segmentedMemoryKey struct {
	Key     string `json:"key"`
	Start   int    `json:"start,omitempty"`
	End     int    `json:"end,omitempty"`
	Primary bool   `json:"primary,omitempty"`
	Source  string `json:"source,omitempty"`
}

type segmentedMemorySegment struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	SessionID     string `json:"session_id,omitempty"`
	InteractionID string `json:"interaction_id,omitempty"`
	// TurnID is read-only compatibility for segments written before the
	// Session -> Interaction hierarchy existed. New records never emit it.
	TurnID        string               `json:"turn_id,omitempty"`
	Kind          string               `json:"kind"`
	StepStart     int                  `json:"step_start"`
	StepEnd       int                  `json:"step_end"`
	StartedAt     int64                `json:"started_at"`
	EndedAt       int64                `json:"ended_at"`
	PreviousID    string               `json:"previous_id,omitempty"`
	NextID        string               `json:"next_id,omitempty"`
	Content       string               `json:"content"`
	KeyStatus     string               `json:"key_status"`
	Keys          []segmentedMemoryKey `json:"keys,omitempty"`
	Status        string               `json:"status"`
	DeletedBy     string               `json:"deleted_by,omitempty"`
	DeletedAt     int64                `json:"deleted_at,omitempty"`
	Reason        string               `json:"reason,omitempty"`
	ReplacementID string               `json:"replacement_id,omitempty"`
}

type segmentedMemoryIndexEntry struct {
	Key           string `json:"key"`
	Target        string `json:"target"`
	Type          string `json:"type"`
	Kind          string `json:"kind,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	InteractionID string `json:"interaction_id,omitempty"`
	SegmentID     string `json:"segment_id,omitempty"`
	// TurnID is read-only compatibility for old index entries.
	TurnID        string `json:"turn_id,omitempty"`
	StepStart     int    `json:"step_start,omitempty"`
	StepEnd       int    `json:"step_end,omitempty"`
	KeyStatus     string `json:"key_status,omitempty"`
	Status        string `json:"status,omitempty"`
	DeletedBy     string `json:"deleted_by,omitempty"`
	DeletedAt     int64  `json:"deleted_at,omitempty"`
	Reason        string `json:"reason,omitempty"`
	ReplacementID string `json:"replacement_id,omitempty"`
	CreatedAt     int64  `json:"created_at"`
}

type segmentedMemoryManifest struct {
	SchemaVersion int            `json:"schema_version"`
	SessionID     string         `json:"session_id"`
	InteractionID string         `json:"interaction_id"`
	Status        string         `json:"status"`
	StartedAt     int64          `json:"started_at"`
	EndedAt       int64          `json:"ended_at,omitempty"`
	UpdatedAt     int64          `json:"updated_at"`
	SegmentCount  int            `json:"segment_count"`
	SegmentKinds  map[string]int `json:"segment_kinds,omitempty"`
	DeletedBy     string         `json:"deleted_by,omitempty"`
	DeletedAt     int64          `json:"deleted_at,omitempty"`
	Reason        string         `json:"reason,omitempty"`
}

type turnMemorySegmentBuilder struct {
	kind      string
	content   strings.Builder
	runeCount int
	stepStart int
	stepEnd   int
	startedAt int64
}

// TurnMemoryRecorder is a nil-safe, streaming observer. It does not participate
// in the model request or response path: callers append events as the agent
// loop runs, and segment sealing/key extraction happens independently.
type TurnMemoryRecorder struct {
	cfg           *Config
	root          string
	sessionID     string
	interactionID string
	startedAt     time.Time
	ctx           context.Context
	cancel        context.CancelFunc
	maxChars      int
	maxSteps      int
	keyWait       time.Duration

	mu           sync.Mutex
	current      *turnMemorySegmentBuilder
	sequence     int
	previousID   string
	segmentCount int
	segmentKinds map[string]int
	endedAt      int64
	status       string
	streamedStep map[int]bool
	tasks        sync.WaitGroup
	taskSlots    chan struct{}
	closed       bool
}

func NewTurnMemoryRecorder(cfg *Config, sessionFile string, subtask bool) *TurnMemoryRecorder {
	return newTurnMemoryRecorderAt(cfg, sessionFile, subtask, time.Now())
}

func newTurnMemoryRecorderAt(cfg *Config, sessionFile string, subtask bool, startedAt time.Time) *TurnMemoryRecorder {
	if cfg == nil || subtask || !cfg.SegmentedMemory.Enabled {
		return nil
	}
	applySegmentedMemoryDefaults(cfg)
	root := strings.TrimSpace(configuredMemoryRoot(cfg))
	if root == "" {
		return nil
	}
	root = filepath.Join(root, segmentedMemoryDir)
	sessionID, interactionID, err := newSegmentedMemoryIdentity(root, sessionFile, startedAt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[SegmentedMemory] WARN: reserve interaction: %v\n", err)
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	recorder := &TurnMemoryRecorder{
		cfg:           cfg,
		root:          root,
		sessionID:     sessionID,
		interactionID: interactionID,
		startedAt:     startedAt,
		ctx:           ctx,
		cancel:        cancel,
		maxChars:      cfg.SegmentedMemory.MaxSegmentChars,
		maxSteps:      cfg.SegmentedMemory.MaxSegmentSteps,
		keyWait:       time.Duration(cfg.SegmentedMemory.KeyWaitTimeout) * time.Second,
		streamedStep:  make(map[int]bool),
		segmentKinds:  make(map[string]int),
		status:        segmentStatusActive,
		taskSlots:     make(chan struct{}, cfg.SegmentedMemory.MaxConcurrentKeys),
	}
	if err := recorder.writeManifestLocked(); err != nil {
		fmt.Fprintf(os.Stderr, "[SegmentedMemory] WARN: write manifest %s: %v\n", interactionID, err)
	}
	return recorder
}

func segmentedMemorySessionID(at time.Time) string {
	if at.IsZero() {
		at = time.Now()
	}
	return at.In(shanghaiTimeLocation).Format("2006-01-02")
}

func newSegmentedMemoryIdentity(root, sessionFile string, startedAt time.Time) (string, string, error) {
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	sessionID := segmentedMemorySessionID(startedAt)
	interactionRoot := filepath.Join(root, segmentedMemorySessionsDir, sessionID, segmentedMemoryInteractionsDir)
	if err := os.MkdirAll(interactionRoot, 0o755); err != nil {
		return "", "", err
	}
	seed := sessionFile + "|" + startedAt.UTC().Format(time.RFC3339Nano)
	sum := sha1.Sum([]byte(seed))
	suffix := fmt.Sprintf("%x", sum)[:8]
	for sequence := 1; sequence <= 9999; sequence++ {
		interactionID := fmt.Sprintf("req-%s-%03d", strings.ReplaceAll(sessionID, "-", ""), sequence)
		path := filepath.Join(interactionRoot, interactionID)
		if err := os.Mkdir(path, 0o755); err == nil {
			return sessionID, interactionID, nil
		} else if !os.IsExist(err) {
			return "", "", err
		}
	}
	// High-volume fallback; Mkdir is atomic, so this remains collision-safe.
	for attempt := 0; attempt < 100; attempt++ {
		interactionID := fmt.Sprintf("req-%s-%s-%02d", strings.ReplaceAll(sessionID, "-", ""), suffix, attempt)
		if err := os.Mkdir(filepath.Join(interactionRoot, interactionID), 0o755); err == nil {
			return sessionID, interactionID, nil
		} else if !os.IsExist(err) {
			return "", "", err
		}
	}
	return "", "", fmt.Errorf("could not allocate interaction id for %s", sessionID)
}

func (r *TurnMemoryRecorder) InteractionID() string {
	if r == nil {
		return ""
	}
	return r.interactionID
}

func (r *TurnMemoryRecorder) TagMessage(message *Message) {
	if r == nil || message == nil {
		return
	}
	message.SessionID = r.sessionID
	message.InteractionID = r.interactionID
}

func (r *TurnMemoryRecorder) RecordUserRequest(content string) {
	if r == nil || strings.TrimSpace(content) == "" {
		return
	}
	content = stripRequestContextPrefix(content)
	if strings.TrimSpace(content) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushCurrentLocked()
	r.startLocked(segmentKindUserRequest, 0)
	r.appendTextLocked(content, 0)
	r.flushCurrentLocked()
}

func (r *TurnMemoryRecorder) AppendAssistantDelta(delta string, step int) {
	if r == nil || delta == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current == nil || r.current.kind != segmentKindAssistant {
		r.flushCurrentLocked()
		r.startLocked(segmentKindAssistant, step)
	}
	r.streamedStep[step] = true
	r.appendTextLocked(delta, step)
}

// RecordAssistantSnapshot is a fallback for providers/mock paths that do not
// emit streaming deltas. It never duplicates a step that already streamed.
func (r *TurnMemoryRecorder) RecordAssistantSnapshot(step int, content string, hasToolCalls bool) {
	if r == nil || strings.TrimSpace(content) == "" {
		return
	}
	r.mu.Lock()
	if r.streamedStep[step] {
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()

	visible := strings.TrimSpace(GetUserVisibleText(stripThinking(content)))
	if visible == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current == nil || r.current.kind != segmentKindAssistant {
		r.flushCurrentLocked()
		r.startLocked(segmentKindAssistant, step)
	}
	r.streamedStep[step] = true
	r.appendTextLocked(visible, step)
	if !hasToolCalls {
		r.flushCurrentLocked()
	}
}

func (r *TurnMemoryRecorder) RecordToolCall(step int, name, arguments string) {
	if r == nil || strings.TrimSpace(name) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushCurrentLocked()
	r.startLocked(segmentKindToolOperation, step)
	text := "工具调用: " + name
	if strings.TrimSpace(arguments) != "" {
		text += "\n参数: " + arguments
	}
	r.appendTextLocked(text, step)
}

func (r *TurnMemoryRecorder) RecordToolResult(step int, name, callID, content string) {
	if r == nil || strings.TrimSpace(name) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current == nil || r.current.kind != segmentKindToolOperation {
		r.flushCurrentLocked()
		r.startLocked(segmentKindToolOperation, step)
	}
	text := "工具结果: " + name
	if strings.TrimSpace(callID) != "" {
		text += " (" + callID + ")"
	}
	text += "\n" + content
	r.appendTextLocked(text, step)
}

func (r *TurnMemoryRecorder) EndStep(step int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushCurrentLocked()
}

func (r *TurnMemoryRecorder) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.flushCurrentLocked()
	r.closed = true
	r.endedAt = time.Now().UnixMilli()
	if err := r.writeManifestLocked(); err != nil {
		fmt.Fprintf(os.Stderr, "[SegmentedMemory] WARN: finalize manifest %s: %v\n", r.interactionID, err)
	}
	r.mu.Unlock()

	done := make(chan struct{})
	go func() {
		r.tasks.Wait()
		close(done)
	}()
	if r.keyWait > 0 {
		select {
		case <-done:
		case <-time.After(r.keyWait):
		}
	}
	r.cancel()
}

func (r *TurnMemoryRecorder) startLocked(kind string, step int) {
	r.current = &turnMemorySegmentBuilder{
		kind:      kind,
		stepStart: step,
		stepEnd:   step,
		startedAt: time.Now().UnixMilli(),
	}
}

func (r *TurnMemoryRecorder) appendTextLocked(text string, step int) {
	if text == "" {
		return
	}
	if r.current == nil {
		r.startLocked(segmentKindAssistant, step)
	}
	if r.current.stepStart == 0 && r.current.kind != segmentKindUserRequest {
		r.current.stepStart = step
	}
	if step != r.current.stepEnd && step-r.current.stepStart+1 > r.maxSteps {
		kind := r.current.kind
		r.flushCurrentLocked()
		r.startLocked(kind, step)
	}
	r.current.stepEnd = step

	for text != "" {
		if r.current.runeCount >= r.maxChars {
			kind := r.current.kind
			r.flushCurrentLocked()
			r.startLocked(kind, step)
		}
		available := r.maxChars - r.current.runeCount
		if available <= 0 {
			available = 1
		}
		part, rest := splitMemoryText(text, available)
		if part == "" {
			part, rest = splitMemoryText(text, 1)
		}
		r.current.content.WriteString(part)
		r.current.runeCount += len([]rune(part))
		text = rest
	}
}

func (r *TurnMemoryRecorder) flushCurrentLocked() {
	if r.current == nil {
		return
	}
	content := r.current.content.String()
	if strings.TrimSpace(content) == "" {
		r.current = nil
		return
	}
	r.sequence++
	segment := &segmentedMemorySegment{
		SchemaVersion: 1,
		ID:            fmt.Sprintf("s%04d", r.sequence),
		SessionID:     r.sessionID,
		InteractionID: r.interactionID,
		Kind:          r.current.kind,
		StepStart:     r.current.stepStart,
		StepEnd:       r.current.stepEnd,
		StartedAt:     r.current.startedAt,
		EndedAt:       time.Now().UnixMilli(),
		PreviousID:    r.previousID,
		Content:       content,
		KeyStatus:     "fallback",
		Keys:          fallbackSegmentKeys(content),
		Status:        segmentStatusActive,
	}
	segment.NextID = ""
	r.previousID = segment.ID
	r.current = nil
	r.segmentCount++
	r.segmentKinds[segment.Kind]++
	r.endedAt = segment.EndedAt

	if err := writeSegmentedMemorySegment(r.root, segment); err != nil {
		fmt.Fprintf(os.Stderr, "[SegmentedMemory] WARN: write segment %s: %v\n", segment.ID, err)
		return
	}
	if err := linkPreviousSegment(r.root, segment, segment.PreviousID, segment.ID); err != nil {
		fmt.Fprintf(os.Stderr, "[SegmentedMemory] WARN: link segment %s: %v\n", segment.ID, err)
	}
	if err := appendSegmentKeysToIndex(r.root, r.cfg, segment, segment.Keys); err != nil {
		fmt.Fprintf(os.Stderr, "[SegmentedMemory] WARN: index segment %s: %v\n", segment.ID, err)
	}
	if r.cfg.SegmentedMemory.KeyExtraction == "llm" && !r.cfg.UseMock {
		r.tasks.Add(1)
		segmentCopy := *segment
		go r.extractKeysAsync(segmentCopy)
	}
	if err := r.writeManifestLocked(); err != nil {
		fmt.Fprintf(os.Stderr, "[SegmentedMemory] WARN: update manifest %s: %v\n", r.interactionID, err)
	}
}

func (r *TurnMemoryRecorder) writeManifestLocked() error {
	if r == nil || strings.TrimSpace(r.root) == "" || strings.TrimSpace(r.interactionID) == "" {
		return nil
	}
	manifest := segmentedMemoryManifest{
		SchemaVersion: 1,
		SessionID:     r.sessionID,
		InteractionID: r.interactionID,
		Status:        r.status,
		StartedAt:     r.startedAt.UnixMilli(),
		EndedAt:       r.endedAt,
		UpdatedAt:     time.Now().UnixMilli(),
		SegmentCount:  r.segmentCount,
		SegmentKinds:  cloneSegmentKindCounts(r.segmentKinds),
	}
	path := filepath.Join(r.root, segmentedMemorySessionsDir, r.sessionID, segmentedMemoryInteractionsDir, r.interactionID, segmentedMemoryManifestFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func cloneSegmentKindCounts(in map[string]int) map[string]int {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]int, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func (r *TurnMemoryRecorder) extractKeysAsync(segment segmentedMemorySegment) {
	defer r.tasks.Done()
	select {
	case r.taskSlots <- struct{}{}:
	case <-r.ctx.Done():
		return
	}
	defer func() { <-r.taskSlots }()

	ctx, cancel := context.WithTimeout(r.ctx, time.Duration(r.cfg.SegmentedMemory.KeyExtractionTimeout)*time.Second)
	defer cancel()
	keys, err := extractSegmentedMemoryKeys(ctx, r.cfg, segment.Kind, segment.Content)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[SegmentedMemory] WARN: key extraction %s: %v\n", segment.ID, err)
		return
	}
	if len(keys) == 0 {
		return
	}
	// The model may invalidate or delete this segment while KEY extraction is
	// still in flight. Re-read the canonical segment before appending keys so
	// a stale async result cannot resurrect deleted memory.
	var current segmentedMemorySegment
	if path := segmentedMemorySegmentPath(r.root, &segment); path != "" {
		data, readErr := os.ReadFile(path)
		if readErr != nil || json.Unmarshal(data, &current) != nil || !segmentedSegmentIsActive(&current) {
			return
		}
	}
	segment.Keys = mergeSegmentedMemoryKeys(segment.Keys, keys)
	segment.KeyStatus = "llm"
	if err := writeSegmentedMemorySegment(r.root, &segment); err != nil {
		fmt.Fprintf(os.Stderr, "[SegmentedMemory] WARN: update segment %s: %v\n", segment.ID, err)
		return
	}
	if err := appendSegmentKeysToIndex(r.root, r.cfg, &segment, keys); err != nil {
		fmt.Fprintf(os.Stderr, "[SegmentedMemory] WARN: index keys %s: %v\n", segment.ID, err)
	}
}

func splitMemoryText(text string, limit int) (string, string) {
	if limit <= 0 || text == "" {
		return "", text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text, ""
	}
	cut := limit
	minBoundary := limit * 3 / 4
	for i := limit - 1; i >= minBoundary && i >= 0; i-- {
		switch runes[i] {
		case '\n', '。', '！', '？', '.', '!', '?', ';', '；':
			cut = i + 1
			i = -1
		}
	}
	return string(runes[:cut]), string(runes[cut:])
}

func fallbackSegmentKeys(content string) []segmentedMemoryKey {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || line == "```" || strings.HasPrefix(line, "```") {
			continue
		}
		runes := []rune(line)
		if len(runes) > 72 {
			line = string(runes[:72])
		}
		start := strings.Index(content, line)
		if start < 0 {
			continue
		}
		return []segmentedMemoryKey{{
			Key:     line,
			Start:   start,
			End:     start + len(line),
			Primary: true,
			Source:  "fallback",
		}}
	}
	if content == "" {
		return nil
	}
	runes := []rune(content)
	if len(runes) > 72 {
		content = string(runes[:72])
	}
	return []segmentedMemoryKey{{Key: content, Start: 0, End: len(content), Primary: true, Source: "fallback"}}
}

func mergeSegmentedMemoryKeys(existing, incoming []segmentedMemoryKey) []segmentedMemoryKey {
	out := append([]segmentedMemoryKey(nil), existing...)
	seen := make(map[string]struct{}, len(out))
	for _, key := range out {
		seen[key.Key] = struct{}{}
	}
	for _, key := range incoming {
		if _, ok := seen[key.Key]; ok {
			continue
		}
		key.Primary = len(out) == 0
		out = append(out, key)
		seen[key.Key] = struct{}{}
	}
	return out
}

func extractSegmentedMemoryKeys(ctx context.Context, cfg *Config, kind, content string) ([]segmentedMemoryKey, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil config")
	}
	system := segmentedMemoryKeyInstruction(cfg)
	user := "分段类型: " + kind + "\n\n内容:\n" + content
	resp, err := CallConfiguredLLMStreamCtx(ctx, cfg, []Message{
		NewMessage("system", system, nil, "", ""),
		NewMessage("user", user, nil, "", ""),
	}, nil, nil)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("empty response")
	}
	keys, err := parseSegmentedMemoryKeys(resp.Content)
	if err != nil {
		return nil, err
	}
	valid := make([]segmentedMemoryKey, 0, len(keys))
	seen := map[string]struct{}{}
	for _, raw := range keys {
		key := strings.TrimSpace(raw)
		if key == "" || len([]rune(key)) > 120 {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		start := strings.Index(content, key)
		if start < 0 {
			continue
		}
		seen[key] = struct{}{}
		valid = append(valid, segmentedMemoryKey{
			Key:     key,
			Start:   start,
			End:     start + len(key),
			Primary: len(valid) == 0,
			Source:  "llm",
		})
		if len(valid) >= 5 {
			break
		}
	}
	if len(valid) == 0 {
		return nil, fmt.Errorf("model returned no literal key")
	}
	return valid, nil
}

func segmentedMemoryKeyInstruction(cfg *Config) string {
	if instruction := strings.TrimSpace(ReadModulePrompt(cfg, "segmented_memory_key", "zh")); instruction != "" {
		return instruction
	}
	return "从给定分段原文中原样复制 1 到 5 个能定位该段的关键句作为 KEY；不得概括或改写。只输出 JSON：{\"keys\":[\"原文关键句\"]}"
}

func parseSegmentedMemoryKeys(content string) ([]string, error) {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```JSON")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)
	if keys, err := decodeSegmentedMemoryKeysJSON(content); err == nil {
		return keys, nil
	}
	if keys := parseSegmentedMemoryKeysLoose(content); len(keys) > 0 {
		return keys, nil
	}
	_, err := decodeSegmentedMemoryKeysJSON(content)
	return nil, fmt.Errorf("parse keys: %w", err)
}

type segmentedMemoryKeysPayload struct {
	Keys []string `json:"keys"`
}

func decodeSegmentedMemoryKeysJSON(content string) ([]string, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, fmt.Errorf("empty response")
	}

	var firstErr error
	for start := 0; start < len(content); start++ {
		switch content[start] {
		case '{', '[':
		default:
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(content[start:]))
		var payload segmentedMemoryKeysPayload
		if err := decoder.Decode(&payload); err == nil && len(payload.Keys) > 0 {
			return payload.Keys, nil
		}
		var keys []string
		decoder = json.NewDecoder(strings.NewReader(content[start:]))
		if err := decoder.Decode(&keys); err == nil && len(keys) > 0 {
			return keys, nil
		}
		if firstErr == nil {
			var probe any
			probeDecoder := json.NewDecoder(strings.NewReader(content[start:]))
			firstErr = probeDecoder.Decode(&probe)
		}
	}
	if firstErr == nil {
		firstErr = fmt.Errorf("no JSON object or array found")
	}
	return nil, firstErr
}

func parseSegmentedMemoryKeysLoose(content string) []string {
	start := findSegmentedMemoryKeysArray(content)
	if start < 0 {
		return nil
	}
	keys := make([]string, 0, 5)
	for i := start + 1; i < len(content); {
		for i < len(content) && (content[i] == ',' || content[i] == ' ' || content[i] == '\t' || content[i] == '\r' || content[i] == '\n') {
			i++
		}
		if i >= len(content) || content[i] == ']' {
			break
		}
		if content[i] != '"' && content[i] != '\'' {
			next := i
			for next < len(content) && content[next] != ',' && content[next] != ']' {
				next++
			}
			if key := strings.TrimSpace(content[i:next]); key != "" {
				keys = append(keys, key)
			}
			i = next + 1
			continue
		}
		key, next, ok := readLooseSegmentedMemoryString(content, i)
		if !ok {
			break
		}
		if key = strings.TrimSpace(key); key != "" {
			keys = append(keys, key)
		}
		i = next
	}
	return keys
}

func findSegmentedMemoryKeysArray(content string) int {
	lower := strings.ToLower(content)
	for offset := 0; offset < len(lower); {
		idx := strings.Index(lower[offset:], "keys")
		if idx < 0 {
			return -1
		}
		idx += offset
		beforeOK := idx == 0 || !isJSONIdentifierByte(lower[idx-1])
		after := idx + len("keys")
		afterOK := after >= len(lower) || !isJSONIdentifierByte(lower[after])
		if !beforeOK || !afterOK {
			offset = after
			continue
		}
		pos := after
		for pos < len(content) && (content[pos] == ' ' || content[pos] == '\t' || content[pos] == '\r' || content[pos] == '\n' || content[pos] == '"' || content[pos] == '\'') {
			pos++
		}
		if pos < len(content) && content[pos] == ':' {
			pos++
			for pos < len(content) && (content[pos] == ' ' || content[pos] == '\t' || content[pos] == '\r' || content[pos] == '\n') {
				pos++
			}
			if pos < len(content) && content[pos] == '[' {
				return pos
			}
		}
		offset = after
	}
	return -1
}

func isJSONIdentifierByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '-'
}

func readLooseSegmentedMemoryString(content string, start int) (string, int, bool) {
	quote := content[start]
	var out strings.Builder
	for i := start + 1; i < len(content); i++ {
		ch := content[i]
		if ch == '\\' {
			if i+1 >= len(content) {
				return "", start, false
			}
			next := content[i+1]
			switch next {
			case '"', '\'', '\\', '/':
				out.WriteByte(next)
				i++
			case 'n':
				out.WriteByte('\n')
				i++
			case 'r':
				out.WriteByte('\r')
				i++
			case 't':
				out.WriteByte('\t')
				i++
			case 'b':
				out.WriteByte('\b')
				i++
			case 'f':
				out.WriteByte('\f')
				i++
			case 'u':
				if i+5 >= len(content) {
					return "", start, false
				}
				code, err := strconv.ParseUint(content[i+2:i+6], 16, 16)
				if err != nil {
					return "", start, false
				}
				out.WriteRune(rune(code))
				i += 5
			default:
				out.WriteByte(next)
				i++
			}
			continue
		}
		if ch == quote {
			next := i + 1
			for next < len(content) && (content[next] == ' ' || content[next] == '\t' || content[next] == '\r' || content[next] == '\n') {
				next++
			}
			if next >= len(content) || content[next] == ',' || content[next] == ']' || content[next] == '}' {
				return out.String(), next, true
			}
		}
		out.WriteByte(ch)
	}
	return "", start, false
}

func writeSegmentedMemorySegment(root string, segment *segmentedMemorySegment) error {
	if segment == nil {
		return nil
	}
	path := segmentedMemorySegmentPath(root, segment)
	if path == "" {
		return fmt.Errorf("segment path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(segment, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func linkPreviousSegment(root string, segment *segmentedMemorySegment, previousID, nextID string) error {
	if segment == nil || strings.TrimSpace(previousID) == "" || strings.TrimSpace(nextID) == "" {
		return nil
	}
	previousTarget := *segment
	previousTarget.ID = previousID
	path := segmentedMemorySegmentPath(root, &previousTarget)
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var previous segmentedMemorySegment
	if err := json.Unmarshal(data, &previous); err != nil {
		return err
	}
	previous.NextID = nextID
	updated, err := json.MarshalIndent(previous, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(updated, '\n'), 0o644)
}

func segmentedSegmentIsActive(segment *segmentedMemorySegment) bool {
	if segment == nil {
		return false
	}
	return strings.TrimSpace(segment.Status) == "" || strings.EqualFold(segment.Status, segmentStatusActive)
}

func segmentedMemoryInteractionDir(root, sessionID, interactionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	interactionID = strings.TrimSpace(interactionID)
	if sessionID == "" || interactionID == "" {
		return ""
	}
	return filepath.Join(root, segmentedMemorySessionsDir, sessionID, segmentedMemoryInteractionsDir, interactionID)
}

func segmentedMemorySegmentPath(root string, segment *segmentedMemorySegment) string {
	if segment == nil || strings.TrimSpace(segment.ID) == "" {
		return ""
	}
	interactionID := segmentedMemorySegmentInteractionID(segment)
	if sessionID := strings.TrimSpace(segment.SessionID); sessionID != "" && interactionID != "" {
		return filepath.Join(segmentedMemoryInteractionDir(root, sessionID, interactionID), segment.ID+".json")
	}
	// Legacy layout: memory/segmented/segments/<turn-id>/<segment-id>.json.
	if interactionID != "" {
		return filepath.Join(root, segmentedMemorySegmentDir, interactionID, segment.ID+".json")
	}
	return ""
}

func segmentedMemorySegmentInteractionID(segment *segmentedMemorySegment) string {
	if segment == nil {
		return ""
	}
	if id := strings.TrimSpace(segment.InteractionID); id != "" {
		return id
	}
	return strings.TrimSpace(segment.TurnID)
}

func appendSegmentKeysToIndex(root string, cfg *Config, segment *segmentedMemorySegment, keys []segmentedMemoryKey) error {
	if segment == nil || len(keys) == 0 {
		return nil
	}
	path := segmentedMemorySegmentPath(root, segment)
	if path == "" {
		return fmt.Errorf("segment path is empty")
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	target := "memory://" + filepath.ToSlash(filepath.Join(segmentedMemoryDir, relative))
	entries := make([]segmentedMemoryIndexEntry, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, segmentedMemoryIndexEntry{
			Key:           key.Key,
			Target:        target,
			Type:          "segment",
			Kind:          segment.Kind,
			SessionID:     segment.SessionID,
			InteractionID: segmentedMemorySegmentInteractionID(segment),
			SegmentID:     segment.ID,
			StepStart:     segment.StepStart,
			StepEnd:       segment.StepEnd,
			KeyStatus:     segment.KeyStatus,
			Status:        segmentStatusActive,
			CreatedAt:     time.Now().UnixMilli(),
		})
	}
	for _, entry := range entries {
		if err := appendSegmentedIndexEntry(root, cfg, entry); err != nil {
			return err
		}
	}
	return nil
}

var segmentedIndexMu sync.Mutex

func appendSegmentedIndexEntry(root string, cfg *Config, entry segmentedMemoryIndexEntry) error {
	segmentedIndexMu.Lock()
	defer segmentedIndexMu.Unlock()

	path := filepath.Join(root, segmentedMemoryIndexDir, "root.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	lines, _ := readSegmentedIndexLines(path)
	if cfg != nil && cfg.SegmentedMemory.MaxRootIndexEntries > 0 && len(lines) >= cfg.SegmentedMemory.MaxRootIndexEntries {
		if err := rotateSegmentedIndex(root, path, lines); err != nil {
			return err
		}
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

func rotateSegmentedIndex(root, rootPath string, lines []string) error {
	if len(lines) == 0 {
		return nil
	}
	volumeDir := filepath.Join(root, segmentedMemoryIndexDir, segmentedMemoryVolumeDir)
	if err := os.MkdirAll(volumeDir, 0o755); err != nil {
		return err
	}
	name := "volume-" + time.Now().Format("20060102T150405.000000000") + ".jsonl"
	volumePath := filepath.Join(volumeDir, name)
	if err := os.Rename(rootPath, volumePath); err != nil {
		return err
	}
	lastKey := ""
	for i := len(lines) - 1; i >= 0; i-- {
		var entry segmentedMemoryIndexEntry
		if json.Unmarshal([]byte(lines[i]), &entry) == nil && strings.TrimSpace(entry.Key) != "" {
			lastKey = entry.Key
			break
		}
	}
	if lastKey == "" {
		lastKey = "历史记忆分卷"
	}
	pointer := segmentedMemoryIndexEntry{
		Key:       lastKey,
		Target:    "memory://" + filepath.ToSlash(filepath.Join(segmentedMemoryDir, segmentedMemoryIndexDir, segmentedMemoryVolumeDir, name)),
		Type:      "volume",
		KeyStatus: "pointer",
		Status:    segmentStatusActive,
		CreatedAt: time.Now().UnixMilli(),
	}
	data, err := json.Marshal(pointer)
	if err != nil {
		return err
	}
	return os.WriteFile(rootPath, append(data, '\n'), 0o644)
}

func readSegmentedIndexLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, raw := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

func readSegmentedMemoryIndexEntries(cfg *Config, limit int, excludeSessionID ...string) []segmentedMemoryIndexEntry {
	if cfg == nil || !cfg.SegmentedMemory.Enabled {
		return nil
	}
	excludedSession := ""
	if len(excludeSessionID) > 0 {
		excludedSession = strings.TrimSpace(excludeSessionID[0])
	}
	root := strings.TrimSpace(configuredMemoryRoot(cfg))
	if root == "" {
		return nil
	}
	path := filepath.Join(root, segmentedMemoryDir, segmentedMemoryIndexDir, "root.jsonl")
	lines, err := readSegmentedIndexLines(path)
	if err != nil || len(lines) == 0 {
		return nil
	}
	entries := make([]segmentedMemoryIndexEntry, 0, len(lines))
	for _, line := range lines {
		var entry segmentedMemoryIndexEntry
		if json.Unmarshal([]byte(line), &entry) != nil || strings.TrimSpace(entry.Key) == "" || !segmentedIndexEntryIsActive(&entry) {
			continue
		}
		// Current-session keys are deliberately excluded from the system
		// prompt. Same-session history continues through the normal working
		// context and summary path, while explicit search remains available.
		if excludedSession != "" && strings.TrimSpace(entry.SessionID) == excludedSession {
			continue
		}
		entries = append(entries, entry)
	}
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return entries
}

func segmentedIndexEntryIsActive(entry *segmentedMemoryIndexEntry) bool {
	if entry == nil {
		return false
	}
	return strings.TrimSpace(entry.Status) == "" || strings.EqualFold(entry.Status, segmentStatusActive)
}

func buildSegmentedMemoryPromptBlock(cfg *Config, currentSessionID string) string {
	if cfg == nil || !cfg.SegmentedMemory.Enabled {
		return ""
	}
	entries := readSegmentedMemoryIndexEntries(cfg, cfg.SegmentedMemory.PromptKeyLimit, currentSessionID)
	if len(entries) == 0 {
		return ""
	}
	tpl := ReadModulePrompt(cfg, "segmented_memory", "zh")
	if strings.TrimSpace(tpl) == "" {
		tpl = "## 历史 KEY\n{{ SEGMENTED_MEMORY_INDEX }}\n需要细节时调用 memory_search(query=KEY)。"
	}
	keys := make([]string, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		key := strings.TrimSpace(entry.Key)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return ""
	}
	encoded, err := json.Marshal(keys)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.ReplaceAll(tpl, "{{ SEGMENTED_MEMORY_INDEX }}", string(encoded)))
}
