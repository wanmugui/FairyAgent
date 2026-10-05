package main

import (
	"context"
	"errors"
)

// toolScoreCleanupJob scores one immutable tool-result snapshot in the
// background. The result is applied only at safe loop boundaries, so the
// classifier never races with an in-flight LLM request or a growing history.
type toolScoreCleanupJob struct {
	done         chan struct{}
	cancel       context.CancelFunc
	candidates   []ToolScoreCandidate
	replacements []CleanedToolResult
	savedRunes   int
	err          error
	trigger      string
	step         int
}

func startToolScoreCleanupJob(ctx context.Context, cands []ToolScoreCandidate, cfg *Config, trigger string, step int) *toolScoreCleanupJob {
	if ctx == nil {
		ctx = context.Background()
	}
	jobCtx, cancel := context.WithCancel(ctx)
	job := &toolScoreCleanupJob{
		done:       make(chan struct{}),
		cancel:     cancel,
		candidates: append([]ToolScoreCandidate(nil), cands...),
		trigger:    trigger,
		step:       step,
	}
	go func() {
		defer close(job.done)
		defer cancel()
		scores, err := ScoreToolCandidatesContext(jobCtx, job.candidates, cfg)
		if err != nil {
			job.err = err
			return
		}
		job.replacements, job.savedRunes = BuildToolScoreCleanupReplacements(job.candidates, scores, cfg)
		if len(job.replacements) == 0 {
			return
		}
		// Emit immediately from the worker so the frontend can replace already
		// rendered tool results while the agent continues its current LLM call.
		cleared := make([]CleanedToolResult, len(job.replacements))
		copy(cleared, job.replacements)
		emitEvent("tool_score_cleanup", map[string]interface{}{
			"status": "completed", "async": true, "step": step, "trigger": trigger,
			"scored": len(job.candidates), "cleared": len(cleared),
			"saved_runes": job.savedRunes, "threshold": cfg.ToolScoreCleanup.Threshold,
			"cleaned": cleared,
		})
	}()
	return job
}

// drainToolScoreCleanupJob applies a completed background job without waiting.
// It returns done=false when scoring is still in flight.
func drainToolScoreCleanupJob(job *toolScoreCleanupJob, workingMsgs, transcript []Message, pinned map[string]bool) (done bool, cleared, savedRunes int, cleaned []CleanedToolResult, err error) {
	if job == nil {
		return false, 0, 0, nil, nil
	}
	select {
	case <-job.done:
	default:
		return false, 0, 0, nil, nil
	}
	if job.err != nil && !errors.Is(job.err, context.Canceled) {
		return true, 0, 0, nil, job.err
	}
	if len(job.replacements) == 0 {
		return true, 0, 0, nil, nil
	}
	replacements := job.replacements
	if len(pinned) > 0 {
		replacements = make([]CleanedToolResult, 0, len(job.replacements))
		for _, replacement := range job.replacements {
			if pinned[replacement.CallID] {
				continue
			}
			replacements = append(replacements, replacement)
		}
	}
	if len(replacements) == 0 {
		return true, 0, 0, nil, nil
	}
	cleared, savedRunes, cleaned = ApplyToolScoreCleanupByCallID(workingMsgs, replacements)
	if len(transcript) > 0 {
		ApplyToolScoreCleanupByCallID(transcript, replacements)
	}
	return true, cleared, savedRunes, cleaned, nil
}
