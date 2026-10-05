package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"agentloop/agent/internal/biz/tool/builtin"
)

// planGateSnapshot is the compact plan state used after a no-tool-call
// response. Only done and skipped are terminal; every other status is still
// actionable and lets the outer loop continue the plan.
type planGateSnapshot struct {
	Question             string
	UpdatedAt            time.Time
	Items                []builtin.PlanItem
	Signature            string
	Prompt               string
	NeedsFinalAcceptance bool
}

type planGateDecision struct {
	Snapshot  planGateSnapshot
	HasActive bool
	ShouldRun bool
	Stalled   bool
	// Stale marks a plan that was last written before this turn started. Its
	// items are still unfinished, but they are not this turn's work: the user
	// has since asked for something else, and pushing the old plan forward is
	// what "I asked a question and got my own plan read back at me" looks like.
	Stale bool
}

// planGateTurn is what the gate needs to know about the turn it is checking.
//
// A plan is authoritative *within* the turn that is executing it. Across turns
// it is only a record: the user's new message decides what happens next, and an
// unfinished item from yesterday's interrupted turn must not be resumed behind
// their back.
type planGateTurn struct {
	// StartedAt is when this turn's user message arrived.
	StartedAt time.Time
	// RuntimeContinuation marks a turn the runtime injected to resume a plan
	// (e.g. after a detached subtask finished). Those are the one case where an
	// older plan is precisely what the turn exists for.
	RuntimeContinuation bool
	// ContinuationRequested marks a user message that asks for the plan to be
	// picked up ("继续/接着/按计划/continue"). This is the same test that decides
	// whether the plan is injected into the prompt, so the gate and the prompt
	// agree about which turns are allowed to touch an older plan.
	ContinuationRequested bool
}

type planGatePayload struct {
	Question string           `json:"question,omitempty"`
	Total    int              `json:"total"`
	Items    []planItemForLLM `json:"items"`
}

// decidePlanContinuation performs the no-tool-call plan check. A changed plan
// or any tool execution since the previous check counts as progress, so long
// plans can continue across many steps without a fixed continuation cap.
func decidePlanContinuation(cfg *Config, sessionFile string, toolsUsedThisTurn int, lastSignature string, lastToolsUsed int, turn planGateTurn) planGateDecision {
	snapshot, ok := loadPlanGateSnapshot(cfg, sessionFile)
	if !ok {
		return planGateDecision{}
	}
	decision := planGateDecision{Snapshot: snapshot, HasActive: true}
	// A plan written in an earlier turn belongs to that turn. The timestamps are
	// the only thing that distinguishes "I am three steps into this plan" from
	// "this plan was left over when the user interrupted me an hour ago", and
	// the second one must not be resumed without being asked. A missing
	// timestamp is unknown vintage, not proof of staleness, so it is left alone.
	resuming := turn.RuntimeContinuation || turn.ContinuationRequested
	if !resuming && !snapshot.UpdatedAt.IsZero() && snapshot.UpdatedAt.Before(turn.StartedAt) {
		decision.Stale = true
		return decision
	}
	progressed := lastSignature == "" || snapshot.Signature != lastSignature || toolsUsedThisTurn > lastToolsUsed
	if progressed {
		decision.ShouldRun = true
		return decision
	}
	decision.Stalled = true
	return decision
}

// planContinuationAllowed reports whether the loop may push the plan forward at
// a step where the model stopped calling tools.
//
// A message from the user outranks the plan. The plan is how the agent keeps its
// own promise, but the user is who the promise was made to: if they speak while
// it is being kept, the next thing that happens has to be an answer to them, and
// the unfinished work waits as unfinished work. Continuing the plan instead is
// what "I asked something mid-run and never got an answer" looks like from the
// outside, and no amount of model-side instruction survived the plan gate
// telling it to keep going.
func planContinuationAllowed(userInterrupted bool, decision planGateDecision) bool {
	if userInterrupted {
		return false
	}
	return decision.ShouldRun
}

func loadPlanGateSnapshot(cfg *Config, sessionFile string) (planGateSnapshot, bool) {
	if cfg == nil {
		return planGateSnapshot{}, false
	}
	planFile := builtin.PlanFilePathFor(cfg.ResolvePath(cfg.WorkspaceDir), sessionFile)
	if planFile == "" {
		return planGateSnapshot{}, false
	}
	raw, err := os.ReadFile(planFile)
	if err != nil {
		return planGateSnapshot{}, false
	}
	var doc struct {
		Question  string             `json:"question"`
		UpdatedAt time.Time          `json:"updated_at"`
		Items     []builtin.PlanItem `json:"items"`
		Accepted  bool               `json:"accepted"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return planGateSnapshot{}, false
	}
	if len(doc.Items) == 0 {
		return planGateSnapshot{}, false
	}

	active := make([]builtin.PlanItem, 0, len(doc.Items))
	for _, item := range doc.Items {
		switch strings.ToLower(strings.TrimSpace(item.Status)) {
		case "done", "skipped":
			continue
		default:
			active = append(active, item)
		}
	}
	if len(active) == 0 {
		if doc.Accepted {
			return planGateSnapshot{}, false
		}
		compact := make([]planItemForLLM, 0, len(doc.Items))
		signatureParts := []string{fmt.Sprintf("accepted=%t updated=%d", doc.Accepted, doc.UpdatedAt.UnixNano())}
		for _, item := range doc.Items {
			summaries := make([]string, 0, len(item.Evidence))
			for _, evidence := range item.Evidence {
				if strings.TrimSpace(evidence.Summary) != "" {
					summaries = append(summaries, evidence.Summary)
				}
			}
			compact = append(compact, planItemForLLM{
				ID:              item.ID,
				Status:          item.Status,
				Action:          item.Action,
				DoneWhen:        item.DoneWhen,
				AuditStatus:     item.AuditStatus,
				AuditDegraded:   item.AuditDegraded,
				EvidenceRefs:    item.EvidenceRefs,
				EvidenceSummary: summaries,
			})
			signatureParts = append(signatureParts, strings.Join([]string{
				item.ID, item.Status, item.DoneWhen, item.AuditStatus, fmt.Sprintf("%t", item.AuditDegraded),
				strings.Join(item.EvidenceRefs, ","),
			}, "\x00"))
		}
		payload, err := json.Marshal(planGatePayload{
			Question: strings.TrimSpace(doc.Question),
			Total:    len(compact),
			Items:    compact,
		})
		if err != nil {
			return planGateSnapshot{}, false
		}
		promptParts := []string{
			"[Plan final acceptance check]",
			"All plan items are terminal, but this plan has not passed final acceptance.",
			"Do not answer as final yet.",
			"The model owns final acceptance: review the evidence summaries, then call plan(action=accept, summary=...). External JEV audit is optional; do not wait for or retry an unavailable classifier.",
		}
		promptParts = append(promptParts,
			"If evidence is insufficient, mark that item in_progress/interrupted and continue the real work.",
		)
		prompt := strings.Join(append(promptParts, "<plan>"+string(payload)+"</plan>\n"), "\n")
		return planGateSnapshot{
			Question:             strings.TrimSpace(doc.Question),
			UpdatedAt:            doc.UpdatedAt,
			Items:                doc.Items,
			Signature:            strings.Join(signatureParts, "\x1f"),
			Prompt:               prompt,
			NeedsFinalAcceptance: true,
		}, true
	}

	compact := make([]planItemForLLM, 0, len(active))
	signatureParts := make([]string, 0, len(active)+1)
	signatureParts = append(signatureParts, fmt.Sprintf("updated=%d", doc.UpdatedAt.UnixNano()))
	for _, item := range active {
		compact = append(compact, planItemForLLM{
			ID:       item.ID,
			Status:   item.Status,
			Action:   item.Action,
			DoneWhen: item.DoneWhen,
		})
		signatureParts = append(signatureParts, strings.Join([]string{item.ID, item.Status, item.Action, item.DoneWhen}, "\x00"))
	}

	payload, err := json.Marshal(planGatePayload{
		Question: strings.TrimSpace(doc.Question),
		Total:    len(active),
		Items:    compact,
	})
	if err != nil {
		return planGateSnapshot{}, false
	}
	prompt := "[Plan continuation check]\n" +
		"The active plan still has unfinished items. Every status except done and skipped is unfinished.\n" +
		"This is not a new user request and not a request for a summary. Continue the plan now:\n" +
		"1. Call tools for the next item, then call plan(action=mark/update) after the real status changes.\n" +
		"2. If a user decision is required, call ask_user and wait.\n" +
		"3. Do not only reply with 'continue' or restate the plan, and do not repeat completed work.\n" +
		"<plan>" + string(payload) + "</plan>\n"

	return planGateSnapshot{
		Question:  strings.TrimSpace(doc.Question),
		UpdatedAt: doc.UpdatedAt,
		Items:     active,
		Signature: strings.Join(signatureParts, "\x1f"),
		Prompt:    prompt,
	}, true
}

func pinnedPlanToolCallIDs(cfg *Config, sessionFile string) map[string]bool {
	if cfg == nil {
		return nil
	}
	planFile := builtin.PlanFilePathFor(cfg.ResolvePath(cfg.WorkspaceDir), sessionFile)
	if planFile == "" {
		return nil
	}
	return builtin.PinnedToolCallIDs(planFile)
}
