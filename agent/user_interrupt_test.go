package main

import "testing"

// The rule the user asked for: a message that arrives while work is in flight
// outranks the plan. Before this, an unfinished plan was enough to make the loop
// inject "继续执行计划" and carry on, so the message sat in the transcript
// unanswered - which from the outside looks exactly like being ignored.
func TestAUserMessageStopsThePlanFromContinuing(t *testing.T) {
	active := planGateDecision{HasActive: true, ShouldRun: true}

	if !planContinuationAllowed(false, active) {
		t.Fatal("with nobody interrupting, an unfinished plan still continues as before")
	}
	if planContinuationAllowed(true, active) {
		t.Fatal("after the user speaks, the plan must not be pushed forward")
	}
	if planContinuationAllowed(true, planGateDecision{}) {
		t.Fatal("an interrupt must not turn into permission to continue either")
	}
}

// The note explains the rule to the model. It is machine output, so it must not
// land in the transcript the user reads back.
func TestTheInterruptNoteStaysOutOfTheTranscript(t *testing.T) {
	note := newInternalControlMessage("system", userInterruptRuleText, internalTypeUserInterrupt)
	if !isInternalControlMessage(note) {
		t.Fatal("the interrupt note is internal control, not user input")
	}
	if userInterruptRuleText == "" {
		t.Fatal("the note has to say something")
	}
}
