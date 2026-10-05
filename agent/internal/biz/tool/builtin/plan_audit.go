package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultJevPlanEndpoint = "https://classifier.dev/v1/systemone"

type planAuditMessage struct {
	Role          string              `json:"role"`
	Content       string              `json:"content"`
	Name          string              `json:"name"`
	ToolCallID    string              `json:"tool_call_id"`
	Step          int                 `json:"step"`
	InteractionID string              `json:"interaction_id"`
	ToolCalls     []planAuditToolCall `json:"tool_calls"`
}

type planAuditToolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type planAuditSession struct {
	Messages []planAuditMessage `json:"messages"`
}

type systemOneQuestion struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria"`
}

type systemOneRequest struct {
	State     string                       `json:"state"`
	Model     string                       `json:"model"`
	Questions map[string]systemOneQuestion `json:"questions"`
}

type systemOneAnswer struct {
	Noul          float64            `json:"noul"`
	Choice        string             `json:"choice"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
}

type systemOneResponse struct {
	Answers map[string]systemOneAnswer `json:"answers"`
	Usage   map[string]int             `json:"usage"`
}

func planAudit(ctx context.Context, planFile, sessionFile, id string) (ToolResult, error) {
	doc := readPlanOrEmpty(planFile)
	if len(doc.Items) == 0 {
		return localErrorResult("plan", fmt.Errorf("no plan items to audit")), nil
	}

	items := selectPlanAuditItems(doc.Items, id)
	if len(items) == 0 {
		if strings.TrimSpace(id) != "" {
			return localErrorResult("plan", fmt.Errorf("no plan item with id=%q", id)), nil
		}
		return localErrorResult("plan", fmt.Errorf("no open plan items to audit")), nil
	}

	endpoint := firstNonEmptyEnv("FAIRY_JEV_ENDPOINT", defaultJevPlanEndpoint)
	model := firstNonEmptyEnv("FAIRY_JEV_MODEL", "jev-latest")
	promptMode := firstNonEmptyEnv("FAIRY_JEV_PROMPT_MODE", "strict")
	timeoutSec := envInt("FAIRY_JEV_TIMEOUT_SEC", 30)
	failOpen := envBool("FAIRY_JEV_FAIL_OPEN", true)
	maxEvidenceChars := envInt("FAIRY_JEV_MAX_EVIDENCE_CHARS", 12000)
	if maxEvidenceChars < 2000 {
		maxEvidenceChars = 2000
	}
	if maxEvidenceChars > 60000 {
		maxEvidenceChars = 60000
	}

	results := make([]map[string]any, 0, len(items))
	for _, item := range items {
		evidence := buildPlanAuditEvidence(sessionFile, item, maxEvidenceChars)
		state := buildPlanAuditState(doc.Question, doc.Items, item, evidence, promptMode)
		answer, err := callSystemOne(ctx, endpoint, model, state, timeoutSec, promptMode)
		if err != nil {
			result := map[string]any{
				"item_id":        item.ID,
				"current_status": item.Status,
				"error":          err.Error(),
			}
			if failOpen {
				result["proposed_status"] = normalizePlanAuditStatus(item.Status)
				result["audit_status"] = "unverified"
				result["audit_degraded"] = true
				result["would_change"] = false
			}
			results = append(results, result)
			continue
		}
		probabilities := planAuditProbabilities(answer)
		proposed := derivePlanAuditStatus(probabilities)
		results = append(results, map[string]any{
			"item_id":         item.ID,
			"action":          item.Action,
			"current_status":  item.Status,
			"proposed_status": proposed,
			"would_change":    proposed != normalizePlanAuditStatus(item.Status),
			"probabilities":   probabilities,
			"usage":           answer.Usage,
			"evidence_chars":  len([]rune(evidence)),
		})
	}
	planFileMu.Lock()
	current := readPlanOrEmpty(planFile)
	for _, result := range results {
		itemID, _ := result["item_id"].(string)
		proposed, _ := result["proposed_status"].(string)
		if itemID == "" || proposed == "" {
			continue
		}
		for i := range current.Items {
			if current.Items[i].ID != itemID {
				continue
			}
			current.Items[i].AuditStatus = proposed
			current.Items[i].AuditError = ""
			current.Items[i].AuditDegraded = false
			if degraded, _ := result["audit_degraded"].(bool); degraded {
				current.Items[i].AuditStatus = "unverified"
				current.Items[i].AuditError, _ = result["error"].(string)
				current.Items[i].AuditDegraded = true
				current.Items[i].AuditConfidence = 0
				current.Items[i].AuditProbabilities = nil
				current.Items[i].AuditedAt = time.Now().UnixMilli()
				break
			}
			current.Items[i].AuditProbabilities = nil
			current.Items[i].AuditConfidence = 0
			if probabilities, ok := result["probabilities"].(map[string]float64); ok {
				current.Items[i].AuditProbabilities = probabilities
				current.Items[i].AuditConfidence = probabilities["satisfied"]
			}
			current.Items[i].AuditedAt = time.Now().UnixMilli()
			break
		}
	}
	current.UpdatedAt = time.Now()
	_ = writePlan(planFile, &current)
	planFileMu.Unlock()

	return ToolResult{Value: map[string]any{
		"ok":      true,
		"probe":   true,
		"model":   model,
		"results": results,
		"hint":    "read-only audit: proposed_status is a recommendation. Call plan(action=mark, id=..., status=...) only after accepting it.",
	}}, nil
}

func selectPlanAuditItems(items []PlanItem, id string) []PlanItem {
	id = strings.TrimSpace(id)
	if id != "" {
		for _, item := range items {
			if item.ID == id {
				return []PlanItem{item}
			}
		}
		return nil
	}
	selected := make([]PlanItem, 0, len(items))
	for _, item := range items {
		status := normalizePlanAuditStatus(item.Status)
		if status == "skipped" {
			continue
		}
		if status == "done" && normalizePlanAuditStatus(item.AuditStatus) == "done" {
			continue
		}
		selected = append(selected, item)
	}
	return selected
}

func buildPlanAuditState(question string, allItems []PlanItem, item PlanItem, evidence, promptMode string) string {
	lines := make([]string, 0, 12)
	if strings.TrimSpace(question) != "" {
		lines = append(lines, "任务目标: "+strings.TrimSpace(question))
	}
	lines = append(lines, "任务ID: "+item.ID)
	lines = append(lines, "任务项: "+strings.TrimSpace(item.Action))
	if strings.TrimSpace(item.Details) != "" {
		lines = append(lines, "任务细节: "+oneLineRunes(item.Details, 1800))
	}
	if strings.TrimSpace(item.DoneWhen) != "" {
		lines = append(lines, "完成标准: "+strings.TrimSpace(item.DoneWhen))
	}
	if len(item.DependsOn) > 0 {
		deps := make([]string, 0, len(item.DependsOn))
		byID := map[string]PlanItem{}
		for _, candidate := range allItems {
			byID[candidate.ID] = candidate
		}
		for _, depID := range item.DependsOn {
			if dep, ok := byID[depID]; ok {
				deps = append(deps, fmt.Sprintf("%s[%s] %s", dep.ID, dep.Status, oneLineRunes(dep.Action, 120)))
			} else {
				deps = append(deps, depID)
			}
		}
		lines = append(lines, "依赖任务: "+strings.Join(deps, "；"))
	}
	if strings.TrimSpace(item.Status) != "" {
		lines = append(lines, "进入本次判断前的状态: "+item.Status)
	}
	if promptMode == "recency" {
		lines = append(lines, "", "当前可见执行证据（按时间从早到晚，末尾状态优先）:")
	} else {
		lines = append(lines, "", "当前可见执行证据:")
	}
	if strings.TrimSpace(evidence) == "" {
		lines = append(lines, "(无)")
	} else {
		lines = append(lines, evidence)
	}
	return strings.Join(lines, "\n")
}

func buildPlanAuditEvidence(sessionFile string, item PlanItem, maxChars int) string {
	sessionFile = strings.TrimSpace(sessionFile)
	if sessionFile == "" {
		return ""
	}
	raw, err := os.ReadFile(sessionFile)
	if err != nil {
		return ""
	}
	var session planAuditSession
	if err := json.Unmarshal(raw, &session); err != nil {
		return ""
	}
	if len(session.Messages) == 0 {
		return ""
	}

	start := 0
	for i, msg := range session.Messages {
		if planAuditCallTouchesItem(msg, item.ID) {
			start = i
			break
		}
	}
	if start == 0 && !planAuditCallTouchesItem(session.Messages[0], item.ID) {
		start = len(session.Messages) - 80
		if start < 0 {
			start = 0
		}
	}

	callNames := map[string]string{}
	callArgs := map[string]string{}
	for _, msg := range session.Messages {
		for _, call := range msg.ToolCalls {
			callNames[call.ID] = call.Function.Name
			callArgs[call.ID] = call.Function.Arguments
		}
	}

	parts := make([]string, 0, 40)
	for i := start; i < len(session.Messages); i++ {
		msg := session.Messages[i]
		if msg.Role == "tool" {
			name := strings.TrimSpace(msg.Name)
			if name == "" {
				name = callNames[msg.ToolCallID]
			}
			if name == "" {
				name = "tool"
			}
			if name == "plan" {
				continue
			}
			args := summarizePlanAuditArgs(callArgs[msg.ToolCallID])
			parts = append(parts, fmt.Sprintf("[%s] %s -> %s", name, args, oneLineRunes(msg.Content, 420)))
			continue
		}
		if msg.Role == "assistant" && strings.TrimSpace(msg.Content) != "" {
			if planAuditHasPlanOnlyToolCall(msg) {
				continue
			}
			parts = append(parts, "[note] "+oneLineRunes(msg.Content, 700))
		}
	}
	return trimPlanAuditEvidence(parts, maxChars)
}

func planAuditCallTouchesItem(msg planAuditMessage, id string) bool {
	for _, call := range msg.ToolCalls {
		if call.Function.Name != "plan" {
			continue
		}
		var args struct {
			ID    string     `json:"id"`
			Items []PlanItem `json:"items"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			continue
		}
		if args.ID == id {
			return true
		}
		for _, item := range args.Items {
			if item.ID == id {
				return true
			}
		}
	}
	return false
}

func planAuditHasPlanOnlyToolCall(msg planAuditMessage) bool {
	if len(msg.ToolCalls) == 0 {
		return false
	}
	for _, call := range msg.ToolCalls {
		if call.Function.Name != "plan" {
			return false
		}
	}
	return true
}

func summarizePlanAuditArgs(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return oneLineRunes(raw, 180)
	}
	keys := []string{"command", "file_path", "path", "query", "url", "goal", "title"}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		if value, ok := args[key]; ok && value != nil {
			parts = append(parts, key+"="+oneLineRunes(fmt.Sprint(value), 160))
		}
	}
	return strings.Join(parts, " ")
}

func trimPlanAuditEvidence(parts []string, maxChars int) string {
	text := strings.Join(parts, "\n")
	runes := []rune(text)
	if len(runes) <= maxChars {
		return text
	}
	head := maxChars / 3
	tail := maxChars - head
	return string(runes[:head]) + "\n...[middle omitted]...\n" + string(runes[len(runes)-tail:])
}

func callSystemOne(ctx context.Context, endpoint, model, state string, timeoutSec int, promptMode string) (systemOneResponse, error) {
	body, err := json.Marshal(systemOneRequest{
		State:     state,
		Model:     model,
		Questions: planAuditQuestions(promptMode),
	})
	if err != nil {
		return systemOneResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return systemOneResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: time.Duration(timeoutSec) * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return systemOneResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return systemOneResponse{}, fmt.Errorf("classifier HTTP %d", resp.StatusCode)
	}
	var out systemOneResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return systemOneResponse{}, err
	}
	return out, nil
}

func planAuditQuestions(promptMode string) map[string]systemOneQuestion {
	return map[string]systemOneQuestion{
		"started": {
			Type:         "noul",
			Instructions: "证据是否表明这个任务项已经开始实际执行？只读文件、搜索、列目录或写计划不算执行；修改文件、运行命令验证、生成产物才算。",
			Criteria: map[string]string{
				"true":  "已经开始实际执行，但未要求已经完成",
				"false": "尚未实际执行，或只有阅读/搜索/规划",
			},
		},
		"satisfied": {
			Type: "noul",
			Instructions: ternaryPlanAudit(promptMode == "recency",
				"综合按时间排列的证据，任务项是否已经达到完成标准？允许执行过程中出现失败，只要后续证据显示已经修复、验证通过或最终完成，就判 true；不要因为写了代码或跑了无关命令就判为满足。",
				"证据是否已经满足任务项的完成标准？要求证据直接支撑完成标准，不要因为写了代码或跑了无关命令就判为满足。"),
			Criteria: map[string]string{
				"true":  "证据已经满足完成标准",
				"false": "证据未满足完成标准",
			},
		},
		"blocked": {
			Type: "noul",
			Instructions: ternaryPlanAudit(promptMode == "recency",
				"按证据的最终状态判断：任务此刻是否仍被错误、超时、上下文截断或外部依赖阻断，并且尚未完成？如果历史上有报错但后续已恢复、完成或转入正常下一步，判 false。",
				"证据是否表明任务执行被错误、超时、上下文截断或外部依赖明确阻断，并且尚未完成？"),
			Criteria: map[string]string{
				"true":  "明确被阻断，当前无法继续且未达到完成标准",
				"false": "没有明确的阻断证据",
			},
		},
		"abandoned": {
			Type:         "noul",
			Instructions: "证据是否明确表明这个任务项已经被放弃，或决定不需要执行？",
			Criteria: map[string]string{
				"true":  "明确放弃或判定不需要做",
				"false": "没有明确放弃",
			},
		},
	}
}

func planAuditProbabilities(resp systemOneResponse) map[string]float64 {
	return map[string]float64{
		"started":   resp.Answers["started"].Noul,
		"satisfied": resp.Answers["satisfied"].Noul,
		"blocked":   resp.Answers["blocked"].Noul,
		"abandoned": resp.Answers["abandoned"].Noul,
	}
}

func derivePlanAuditStatus(probabilities map[string]float64) string {
	// Choose the strongest signal that crossed the threshold instead of using a
	// fixed priority order. The old abandoned-first rule could turn a 0.7
	// satisfied item into skipped whenever abandoned was exactly 0.5.
	statuses := []struct {
		key    string
		status string
	}{
		{"satisfied", "done"},
		{"blocked", "interrupted"},
		{"abandoned", "skipped"},
	}
	bestStatus := "pending"
	bestScore := 0.0
	for _, candidate := range statuses {
		score := probabilities[candidate.key]
		if score >= 0.5 && score > bestScore {
			bestScore = score
			bestStatus = candidate.status
		}
	}
	if bestStatus != "pending" {
		return bestStatus
	}
	if probabilities["started"] >= 0.5 {
		return "in_progress"
	}
	return "pending"
}

func normalizePlanAuditStatus(status string) string {
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "" {
		return "pending"
	}
	return status
}

func firstNonEmptyEnv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func oneLineRunes(value string, limit int) string {
	text := strings.Join(strings.Fields(value), " ")
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit]) + "..."
	}
	return text
}

func ternaryPlanAudit(condition bool, yes, no string) string {
	if condition {
		return yes
	}
	return no
}
