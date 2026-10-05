package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// PlanItem is a single executable step in the model's plan. The model
// emits a list of these via update_plan; the agent loop reads it back
// each step to keep the model on track.
type PlanItem struct {
	ID                 string             `json:"id"`     // short stable key, e.g. "step-1"
	Status             string             `json:"status"` // pending | in_progress | interrupted | done | skipped
	Action             string             `json:"action"` // what to do (verb phrase)
	Details            string             `json:"details,omitempty"`
	DoneWhen           string             `json:"done_when"` // executable completion condition
	DependsOn          []string           `json:"depends_on,omitempty"`
	ParentID           string             `json:"parent_id,omitempty"`
	EvidenceRefs       []string           `json:"evidence_refs,omitempty"`
	Evidence           []PlanEvidence     `json:"evidence,omitempty"`
	AuditStatus        string             `json:"audit_status,omitempty"`
	AuditConfidence    float64            `json:"audit_confidence,omitempty"`
	AuditProbabilities map[string]float64 `json:"audit_probabilities,omitempty"`
	AuditError         string             `json:"audit_error,omitempty"`
	AuditDegraded      bool               `json:"audit_degraded,omitempty"`
	AuditedAt          int64              `json:"audited_at,omitempty"`
}

// PlanEvidence links a plan item to a concrete, inspectable artifact. ToolCallID
// lets the runtime pin the corresponding tool result so cleanup cannot remove
// the evidence before final acceptance.
type PlanEvidence struct {
	Kind       string `json:"kind"` // tool_result | command | test | file | artifact
	Ref        string `json:"ref"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Summary    string `json:"summary,omitempty"`
}

// planDoc is the on-disk shape of the plan file. We keep it as plain JSON
// so the model (and humans) can read it directly with read_file / cat.
type planDoc struct {
	Question          string     `json:"question"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	Items             []PlanItem `json:"items"`
	Accepted          bool       `json:"accepted,omitempty"`
	AcceptedAt        time.Time  `json:"accepted_at,omitempty"`
	AcceptanceSummary string     `json:"acceptance_summary,omitempty"`
}

var planFileMu sync.Mutex // only one writer per agent process

// NewLocalPlanTool returns a tool that maintains a plan file scoped to the
// invocation's session (stored under the session's plans/ directory, falling
// back to <workspace>/.plan.json when no session is bound). The plan is the
// agent's TODO list for the whole conversation; it persists across turns so
// the user (or a future turn) can inspect it.
func NewLocalPlanTool(schema ToolDef, workspace string) Tool {
	return newLocalStructuredTool("plan", schema, func(ctx context.Context, invocation ToolInvocation) (ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return ToolResult{}, err
		}
		args, err := decodeLocalToolArgs(invocation)
		if err != nil {
			return localErrorResult("plan", err), nil
		}
		planFile := planFilePath(workspace)
		sessionFile := ""
		if localContext, ctxErr := localToolContext(invocation); ctxErr == nil {
			sessionFile = localContext.SessionFile
			if scoped := PlanFilePathFor(localContext.Workspace, localContext.SessionFile); scoped != "" {
				planFile = scoped
			}
		}
		if planFile == "" {
			return localErrorResult("plan", fmt.Errorf("workspace is not configured")), nil
		}
		action := strings.ToLower(strings.TrimSpace(localStringArg(args, "action")))
		switch action {
		case "", "view":
			return planView(planFile)
		case "reset":
			return planReset(planFile, strings.TrimSpace(localStringArg(args, "question")))
		case "update", "add", "set":
			raw, err := localJSONArg(args, "items")
			if err != nil {
				return localErrorResult("plan", err), nil
			}
			if raw == "" {
				return localErrorResult("plan", fmt.Errorf("items is required for action=update (JSON array)")), nil
			}
			return planUpdate(planFile, strings.TrimSpace(localStringArg(args, "question")), raw)
		case "mark":
			id := strings.TrimSpace(localStringArg(args, "id"))
			status := strings.TrimSpace(localStringArg(args, "status"))
			evidence, evidenceErr := localJSONArg(args, "evidence")
			if evidenceErr != nil {
				return localErrorResult("plan", evidenceErr), nil
			}
			evidenceRefs, refsErr := localJSONArg(args, "evidence_refs")
			if refsErr != nil {
				return localErrorResult("plan", refsErr), nil
			}
			return planMark(planFile, id, status, evidence, evidenceRefs)
		case "audit":
			id := strings.TrimSpace(localStringArg(args, "id"))
			return planAudit(ctx, planFile, sessionFile, id)
		case "accept":
			return planAccept(planFile, strings.TrimSpace(localStringArg(args, "summary")))
		default:
			return localErrorResult("plan", fmt.Errorf("unknown action %q (use view/reset/update/mark/audit/accept)", action)), nil
		}
	})
}

func localJSONArg(args map[string]any, key string) (string, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return "", nil
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text), nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("%s must be a JSON array: %w", key, err)
	}
	return string(data), nil
}

func planFilePath(workspace string) string {
	if strings.TrimSpace(workspace) == "" {
		return ""
	}
	return filepath.Join(workspace, ".plan.json")
}

// PlanFilePathFor returns the plan file for a workspace. When a session file
// is bound the plan is scoped to that session so two chats never share one
// plan; otherwise it falls back to <workspace>/.plan.json.
func PlanFilePathFor(workspace, sessionFile string) string {
	if session := strings.TrimSpace(sessionFile); session != "" {
		dir := filepath.Dir(session)
		base := strings.TrimSuffix(filepath.Base(session), filepath.Ext(session))
		return filepath.Join(dir, "plans", base+".plan.json")
	}
	return planFilePath(workspace)
}

func planView(path string) (ToolResult, error) {
	planFileMu.Lock()
	defer planFileMu.Unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ToolResult{Value: map[string]any{
				"ok":    true,
				"empty": true,
				"hint":  "no plan yet. Call plan(action=update, items=[...]) to create one.",
			}}, nil
		}
		return localErrorResult("plan", err), nil
	}
	var doc planDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return localErrorResult("plan", fmt.Errorf("plan file is corrupted: %w", err)), nil
	}
	// Roll-up summary so the model sees progress at a glance.
	counts := map[string]int{}
	for _, it := range doc.Items {
		counts[it.Status]++
	}
	return ToolResult{Value: map[string]any{
		"ok":         true,
		"empty":      false,
		"question":   doc.Question,
		"updated_at": doc.UpdatedAt,
		"items":      doc.Items,
		"total":      len(doc.Items),
		"by_status":  counts,
		"accepted":   doc.Accepted,
		"hint":       "Keep items atomic and verifiable. Every item requires done_when. Record evidence_refs as soon as verification output arrives so cleanup pins it. Marking done requires a short evidence summary. The model owns acceptance; action=audit is optional external review when available.",
	}}, nil
}

func planReset(path, question string) (ToolResult, error) {
	planFileMu.Lock()
	defer planFileMu.Unlock()
	now := time.Now()
	doc := planDoc{Question: question, CreatedAt: now, UpdatedAt: now}
	if err := writePlan(path, &doc); err != nil {
		return localErrorResult("plan", err), nil
	}
	return ToolResult{Value: map[string]any{"ok": true, "items": []PlanItem{}, "hint": "plan reset. Add items with plan(action=update, items=[...])"}}, nil
}

func planUpdate(path, question, itemsJSON string) (ToolResult, error) {
	planFileMu.Lock()
	defer planFileMu.Unlock()
	var newItems []PlanItem
	if err := json.Unmarshal([]byte(itemsJSON), &newItems); err != nil {
		return localErrorResult("plan", fmt.Errorf("items must be a JSON array of plan items: %w", err)), nil
	}
	if len(newItems) == 0 {
		return localErrorResult("plan", fmt.Errorf("at least one item is required")), nil
	}

	existing := readPlanOrEmpty(path)
	question = strings.TrimSpace(question)
	if question != "" {
		previousQuestion := strings.TrimSpace(existing.Question)
		switch {
		case previousQuestion == "":
			existing.Question = question
		case normalizePlanQuestion(question) != normalizePlanQuestion(previousQuestion):
			// A new question is a new plan, not a merge into the previous
			// project's checklist.
			now := time.Now()
			existing = planDoc{Question: question, CreatedAt: now, UpdatedAt: now}
		}
	}

	// Inherit previous status when the model re-sends an item without one,
	// so completed work isn't silently reset to pending. Do this BEFORE the
	// default backfill, which would otherwise fill in "pending" first.
	byID := map[string]PlanItem{}
	for _, it := range existing.Items {
		byID[it.ID] = it
	}
	for i := range newItems {
		if prev, ok := byID[newItems[i].ID]; ok {
			if newItems[i].Status == "" {
				newItems[i].Status = prev.Status
			}
			if len(newItems[i].EvidenceRefs) == 0 {
				newItems[i].EvidenceRefs = append([]string(nil), prev.EvidenceRefs...)
			}
			if len(newItems[i].Evidence) == 0 {
				newItems[i].Evidence = append([]PlanEvidence(nil), prev.Evidence...)
			}
			verificationUnchanged := newItems[i].DoneWhen == prev.DoneWhen &&
				sameStringSlice(newItems[i].EvidenceRefs, prev.EvidenceRefs) &&
				samePlanEvidence(newItems[i].Evidence, prev.Evidence)
			if newItems[i].AuditStatus == "" && verificationUnchanged {
				newItems[i].AuditStatus = prev.AuditStatus
				newItems[i].AuditConfidence = prev.AuditConfidence
				newItems[i].AuditProbabilities = prev.AuditProbabilities
				newItems[i].AuditError = prev.AuditError
				newItems[i].AuditDegraded = prev.AuditDegraded
				newItems[i].AuditedAt = prev.AuditedAt
			}
		}
	}
	// Backfill IDs and default status so the model can leave them out.
	for i := range newItems {
		if strings.TrimSpace(newItems[i].ID) == "" {
			newItems[i].ID = fmt.Sprintf("step-%d", i+1)
		}
		if strings.TrimSpace(newItems[i].Status) == "" {
			newItems[i].Status = "pending"
		}
	}

	// Rebuild merged: newItems order first (fresh list = current intent),
	// then any preserved old items the new list didn't mention.
	merged := make([]PlanItem, 0, len(newItems)+len(existing.Items))
	added := map[string]bool{}
	for _, it := range newItems {
		merged = append(merged, it)
		added[it.ID] = true
	}
	for _, it := range existing.Items {
		if !added[it.ID] {
			merged = append(merged, it)
			added[it.ID] = true
		}
	}
	// Stable sort: active/interrupted first, then pending, then done/skipped.
	sort.SliceStable(merged, func(i, j int) bool {
		return statusOrder(merged[i].Status) < statusOrder(merged[j].Status)
	})
	if err := validatePlanItems(merged); err != nil {
		return localErrorResult("plan", err), nil
	}
	now := time.Now()
	existing.Items = merged
	existing.UpdatedAt = now
	existing.Accepted = false
	existing.AcceptedAt = time.Time{}
	existing.AcceptanceSummary = ""
	if existing.CreatedAt.IsZero() {
		existing.CreatedAt = now
	}
	if err := writePlan(path, &existing); err != nil {
		return localErrorResult("plan", err), nil
	}
	return ToolResult{Value: map[string]any{"ok": true, "items": merged, "total": len(merged), "hint": "Call plan(action=mark, id=..., status=done|in_progress|interrupted|skipped) as you work through items."}}, nil
}

func normalizePlanQuestion(question string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(question))), " ")
}

func planMark(path, id, status, evidenceJSON, evidenceRefsJSON string) (ToolResult, error) {
	if id == "" {
		return localErrorResult("plan", fmt.Errorf("id is required for action=mark")), nil
	}
	switch status {
	case "pending", "in_progress", "interrupted", "done", "skipped":
	default:
		return localErrorResult("plan", fmt.Errorf("status must be one of pending|in_progress|interrupted|done|skipped, got %q", status)), nil
	}
	planFileMu.Lock()
	defer planFileMu.Unlock()
	doc := readPlanOrEmpty(path)
	found := false
	for i, it := range doc.Items {
		if it.ID == id {
			if status == "done" {
				evidence, err := parsePlanEvidence(evidenceJSON)
				if err != nil {
					return localErrorResult("plan", err), nil
				}
				evidenceRefs, err := parsePlanEvidenceRefs(evidenceRefsJSON)
				if err != nil {
					return localErrorResult("plan", err), nil
				}
				if len(evidence) > 0 {
					doc.Items[i].Evidence = append(doc.Items[i].Evidence, evidence...)
				}
				doc.Items[i].EvidenceRefs = appendUniqueStrings(doc.Items[i].EvidenceRefs, evidenceRefs...)
				if strings.TrimSpace(doc.Items[i].DoneWhen) == "" {
					return localErrorResult("plan", fmt.Errorf("item %q cannot be done: done_when is required", id)), nil
				}
				if len(doc.Items[i].EvidenceRefs) == 0 && len(doc.Items[i].Evidence) == 0 {
					return localErrorResult("plan", fmt.Errorf("item %q cannot be done without evidence; pass evidence_refs/evidence", id)), nil
				}
				if !hasPlanEvidenceSummary(doc.Items[i]) {
					return localErrorResult("plan", fmt.Errorf("item %q cannot be done without a short evidence summary", id)), nil
				}
				doc.Items[i].AuditStatus = ""
				doc.Items[i].AuditConfidence = 0
				doc.Items[i].AuditProbabilities = nil
				doc.Items[i].AuditError = ""
				doc.Items[i].AuditDegraded = false
				doc.Items[i].AuditedAt = 0
			}
			doc.Items[i].Status = status
			if status != "done" {
				doc.Items[i].AuditStatus = ""
				doc.Items[i].AuditError = ""
				doc.Items[i].AuditDegraded = false
				doc.Items[i].AuditedAt = 0
			}
			found = true
			break
		}
	}
	if !found {
		return localErrorResult("plan", fmt.Errorf("no plan item with id=%q", id)), nil
	}
	doc.Accepted = false
	doc.AcceptedAt = time.Time{}
	doc.AcceptanceSummary = ""
	doc.UpdatedAt = time.Now()
	if err := writePlan(path, &doc); err != nil {
		return localErrorResult("plan", err), nil
	}
	return ToolResult{Value: map[string]any{"ok": true, "id": id, "status": status}}, nil
}

func planAccept(path, summary string) (ToolResult, error) {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return localErrorResult("plan", fmt.Errorf("summary is required for action=accept")), nil
	}
	planFileMu.Lock()
	defer planFileMu.Unlock()
	doc := readPlanOrEmpty(path)
	if len(doc.Items) == 0 {
		return localErrorResult("plan", fmt.Errorf("cannot accept an empty plan")), nil
	}
	for _, item := range doc.Items {
		status := strings.ToLower(strings.TrimSpace(item.Status))
		if status == "skipped" {
			continue
		}
		if status != "done" {
			return localErrorResult("plan", fmt.Errorf("cannot accept: item %q is still %s", item.ID, item.Status)), nil
		}
		if len(item.EvidenceRefs) == 0 && len(item.Evidence) == 0 {
			return localErrorResult("plan", fmt.Errorf("cannot accept: done item %q has no evidence", item.ID)), nil
		}
		if !hasPlanEvidenceSummary(item) {
			return localErrorResult("plan", fmt.Errorf("cannot accept: done item %q has no short evidence summary", item.ID)), nil
		}
	}
	doc.Accepted = true
	doc.AcceptedAt = time.Now()
	doc.AcceptanceSummary = summary
	if err := writePlan(path, &doc); err != nil {
		return localErrorResult("plan", err), nil
	}
	return ToolResult{Value: map[string]any{
		"ok": true, "accepted": true, "summary": summary, "items": doc.Items,
	}}, nil
}

func validatePlanItems(items []PlanItem) error {
	byID := make(map[string]PlanItem, len(items))
	children := make(map[string]int)
	for _, item := range items {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			return fmt.Errorf("every plan item requires a non-empty id")
		}
		if _, exists := byID[id]; exists {
			return fmt.Errorf("duplicate plan item id %q", id)
		}
		if !validPlanStatus(item.Status) {
			return fmt.Errorf("item %q has invalid status %q", id, item.Status)
		}
		if strings.TrimSpace(item.Action) == "" {
			return fmt.Errorf("item %q requires action", id)
		}
		if strings.TrimSpace(item.DoneWhen) == "" {
			return fmt.Errorf("item %q requires done_when", id)
		}
		if !executableDoneWhen(item.DoneWhen) {
			return fmt.Errorf("item %q done_when is not executable; use a command, test/build check, file check, or observable runtime check", id)
		}
		if item.ParentID != "" {
			children[item.ParentID]++
		}
		if item.Status == "done" && len(item.EvidenceRefs) == 0 && len(item.Evidence) == 0 {
			return fmt.Errorf("done item %q requires evidence_refs or evidence", id)
		}
		if item.Status == "done" && !hasPlanEvidenceSummary(item) {
			return fmt.Errorf("done item %q requires at least one short evidence summary", id)
		}
		for _, evidence := range item.Evidence {
			if strings.TrimSpace(evidence.Kind) == "" || strings.TrimSpace(evidence.Ref) == "" {
				return fmt.Errorf("item %q has evidence without kind/ref", id)
			}
		}
		byID[id] = item
	}
	for _, item := range items {
		if item.ParentID != "" {
			if item.ParentID == item.ID {
				return fmt.Errorf("item %q cannot depend on itself as parent", item.ID)
			}
			if _, ok := byID[item.ParentID]; !ok {
				return fmt.Errorf("item %q references missing parent_id %q", item.ID, item.ParentID)
			}
		}
		for _, dep := range item.DependsOn {
			if dep == item.ID {
				return fmt.Errorf("item %q cannot depend on itself", item.ID)
			}
			if _, ok := byID[dep]; !ok {
				return fmt.Errorf("item %q references missing dependency %q", item.ID, dep)
			}
		}
		if isUmbrellaPlanItem(item) && children[item.ID] < 2 {
			return fmt.Errorf("item %q is too broad; split it into at least two child items with parent_id=%q", item.ID, item.ID)
		}
	}
	return nil
}

func validPlanStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "pending", "in_progress", "interrupted", "done", "skipped":
		return true
	default:
		return false
	}
}

func executableDoneWhen(value string) bool {
	text := strings.ToLower(strings.TrimSpace(value))
	if text == "" {
		return false
	}
	if strings.Contains(text, "`") || strings.HasPrefix(text, "verify:") || strings.HasPrefix(text, "命令:") {
		return true
	}
	markers := []string{
		"运行", "执行", "命令", "测试", "验证", "检查", "确认", "打开", "刷新", "重启",
		"无报错", "返回", "通过", "退出码", "exit", "test", "build", "lint", "curl",
		"node ", "pnpm ", "npm ", "go test", "cargo ", "python ", "pytest",
		"powershell", "bash", "git diff", "http 200", "status 200", "screenshot",
	}
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func isUmbrellaPlanItem(item PlanItem) bool {
	if strings.TrimSpace(item.ParentID) != "" {
		return false
	}
	action := strings.TrimSpace(item.Action)
	if len([]rune(action)) > 72 {
		return true
	}
	if strings.ContainsAny(action, "、；;") {
		return true
	}
	for _, marker := range []string{"以及", "并且", "同时完成", "全部", "所有", "整套", "完整实现"} {
		if strings.Contains(action, marker) {
			return true
		}
	}
	return strings.Count(action, "/") >= 2 &&
		(strings.Contains(strings.ToUpper(action), "POST") || strings.Contains(strings.ToUpper(action), "DELETE") || strings.Contains(strings.ToUpper(action), "GET"))
}

func parsePlanEvidence(raw string) ([]PlanEvidence, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil, nil
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return nil, fmt.Errorf("evidence must be a JSON array of {kind,ref,tool_call_id,summary}: %w", err)
	}
	evidence := make([]PlanEvidence, 0, len(rows))
	for i, row := range rows {
		item := PlanEvidence{
			Kind:       firstPlanEvidenceString(row, "kind", "type"),
			Ref:        firstPlanEvidenceString(row, "ref", "path", "file_path", "file", "command", "test", "artifact", "url", "query", "tool_call_id", "call_id", "id"),
			ToolCallID: firstPlanEvidenceString(row, "tool_call_id", "call_id"),
			Summary:    firstPlanEvidenceString(row, "summary", "description", "result", "output"),
		}
		if item.Kind == "" {
			item.Kind = inferredPlanEvidenceKind(row)
		}
		if item.Ref == "" && item.Summary != "" {
			item.Kind = firstNonEmpty(item.Kind, "note")
			item.Ref = item.Summary
		}
		if item.Kind == "" || item.Ref == "" {
			return nil, fmt.Errorf("evidence[%d] requires kind and ref", i)
		}
		evidence = append(evidence, item)
	}
	return evidence, nil
}

func firstPlanEvidenceString(row map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := row[key]; ok && value != nil {
			if text := strings.TrimSpace(fmt.Sprint(value)); text != "" {
				return text
			}
		}
	}
	return ""
}

func inferredPlanEvidenceKind(row map[string]any) string {
	switch {
	case firstPlanEvidenceString(row, "path", "file_path", "file") != "":
		return "file"
	case firstPlanEvidenceString(row, "command") != "":
		return "command"
	case firstPlanEvidenceString(row, "test") != "":
		return "test"
	case firstPlanEvidenceString(row, "artifact") != "":
		return "artifact"
	case firstPlanEvidenceString(row, "tool_call_id", "call_id") != "":
		return "tool_result"
	case firstPlanEvidenceString(row, "url") != "":
		return "url"
	default:
		return ""
	}
}

func parsePlanEvidenceRefs(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil, nil
	}
	var refs []string
	if err := json.Unmarshal([]byte(raw), &refs); err != nil {
		return nil, fmt.Errorf("evidence_refs must be a JSON array of tool_call_id strings: %w", err)
	}
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		if value := strings.TrimSpace(ref); value != "" {
			out = append(out, value)
		}
	}
	return out, nil
}

func appendUniqueStrings(values []string, additions ...string) []string {
	seen := make(map[string]bool, len(values)+len(additions))
	out := make([]string, 0, len(values)+len(additions))
	for _, value := range append(append([]string{}, values...), additions...) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func hasPlanEvidenceSummary(item PlanItem) bool {
	for _, evidence := range item.Evidence {
		if strings.TrimSpace(evidence.Summary) != "" {
			return true
		}
	}
	return false
}

func sameStringSlice(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func samePlanEvidence(left, right []PlanEvidence) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// PinnedToolCallIDs returns evidence references that cleanup must not remove
// before the plan is accepted.
func PinnedToolCallIDs(path string) map[string]bool {
	planFileMu.Lock()
	defer planFileMu.Unlock()
	doc := readPlanOrEmpty(path)
	if doc.Accepted {
		return nil
	}
	pinned := map[string]bool{}
	for _, item := range doc.Items {
		status := strings.ToLower(strings.TrimSpace(item.Status))
		auditStatus := strings.ToLower(strings.TrimSpace(item.AuditStatus))
		if status == "skipped" || (status == "done" && auditStatus == "done") {
			continue
		}
		for _, ref := range item.EvidenceRefs {
			if ref = strings.TrimSpace(ref); ref != "" {
				pinned[ref] = true
			}
		}
		for _, evidence := range item.Evidence {
			if evidence.ToolCallID != "" {
				pinned[evidence.ToolCallID] = true
			}
			if evidence.Ref != "" {
				pinned[evidence.Ref] = true
			}
		}
	}
	return pinned
}

func readPlanOrEmpty(path string) planDoc {
	data, err := os.ReadFile(path)
	if err != nil {
		return planDoc{}
	}
	var doc planDoc
	_ = json.Unmarshal(data, &doc)
	return doc
}

func writePlan(path string, doc *planDoc) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func statusOrder(s string) int {
	switch s {
	case "in_progress":
		return 0
	case "interrupted":
		return 1
	case "pending":
		return 2
	case "skipped":
		return 3
	case "done":
		return 4
	default:
		return 5
	}
}
