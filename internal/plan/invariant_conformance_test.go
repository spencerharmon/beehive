package plan

import (
	"testing"
	"time"
)

// This file holds conformance tests that pin the task-lifecycle invariants proven
// in the TLA+ Layer-2 model specs/TaskStatus.tla (see docs/formal-spec-mapping.md).
// Each test asserts the code obeys a machine-checked design invariant; the
// expected legal-edge set is written out LITERALLY here, independent of the
// production `transitions` map, so a drift in either one fails loudly rather than
// the test silently mirroring a regressed table.

// TestInvariant_LegalTransitionsOnly backs TaskStatus.tla's LegalTransitionsOnly:
// the agent Transition method admits EXACTLY the sanctioned status edges and no
// others, and every legal edge releases the active claim (a status change is a
// work-phase change). The recovery/escalation edges (REVIEW->TODO/HUMAN, etc.)
// are driven by dedicated methods, not Transition, and are covered separately.
func TestInvariant_LegalTransitionsOnly(t *testing.T) {
	allStatuses := []Status{StatusTODO, StatusReview, StatusArb, StatusDone, StatusHuman}

	// The authoritative agent-edge set, transcribed from the TLA+ LegalEdges
	// (the Transition subset). Kept independent of the production `transitions`
	// map on purpose.
	legal := map[Status]map[Status]bool{
		StatusTODO:   {StatusReview: true},
		StatusReview: {StatusDone: true, StatusArb: true},
		StatusArb:    {StatusTODO: true, StatusDone: true},
		// StatusDone and StatusHuman are terminal: no legal agent edge out.
	}
	isLegal := func(from, to Status) bool { return legal[from][to] }

	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	for _, from := range allStatuses {
		for _, to := range allStatuses {
			if from == to {
				continue // self-edges are not transitions
			}
			// CanTransition must agree with the authoritative set.
			if got := CanTransition(from, to); got != isLegal(from, to) {
				t.Errorf("CanTransition(%s,%s)=%v, want %v", from, to, got, isLegal(from, to))
			}

			// A claimed task attempting the edge.
			tk := &Task{ID: "x", Status: from}
			tk.Claim("bee-1", now)
			err := tk.Transition(to, now)

			if isLegal(from, to) {
				if err != nil {
					t.Errorf("legal edge %s->%s rejected: %v", from, to, err)
					continue
				}
				if tk.Status != to {
					t.Errorf("edge %s->%s left status %s", from, to, tk.Status)
				}
				// LegalTransitionsOnly's companion fact: a status change releases
				// the claim (session + heartbeat cleared).
				if tk.Session != "" || !tk.Heartbeat.IsZero() {
					t.Errorf("edge %s->%s did not release the claim (session=%q hb=%v)",
						from, to, tk.Session, tk.Heartbeat)
				}
			} else {
				if err == nil {
					t.Errorf("illegal edge %s->%s was allowed", from, to)
				}
				// An illegal edge must be a no-op: status and claim preserved.
				if tk.Status != from {
					t.Errorf("illegal edge %s->%s mutated status to %s", from, to, tk.Status)
				}
				if tk.Session != "bee-1" || tk.Heartbeat != now {
					t.Errorf("illegal edge %s->%s disturbed the claim", from, to)
				}
			}
		}
	}
}

// TestInvariant_EscalationTerminates backs TaskStatus.tla's AttemptsBounded +
// Terminates: every runner recovery edge that recycles a reviewable task
// (Reject, Strand, RecoverLostWork) bumps Attempts and, once Attempts exceeds the
// limit, routes deterministically to the terminal NEEDS-HUMAN instead of looping
// rework forever. Each also releases the active claim.
func TestInvariant_EscalationTerminates(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	const limit = 1

	// Each recovery method under test, applied to a fresh reviewable task.
	recoveries := []struct {
		name  string
		apply func(tk *Task) error
	}{
		{"Reject", func(tk *Task) error { return tk.Reject(limit, now) }},
		{"Strand", func(tk *Task) error { return tk.Strand("push failed", limit, now) }},
		{"RecoverLostWork", func(tk *Task) error { return tk.RecoverLostWork("branch GC'd", limit, now) }},
	}

	for _, r := range recoveries {
		// Start NEEDS-REVIEW with a live claim, Attempts under the limit.
		tk := &Task{ID: "x", Status: StatusReview}
		tk.Claim("bee-1", now)

		// First application: under the limit -> recycles to TODO, Attempts++.
		if err := r.apply(tk); err != nil {
			t.Fatalf("%s: first application errored: %v", r.name, err)
		}
		if tk.Status != StatusTODO {
			t.Errorf("%s: under-limit expected TODO, got %s", r.name, tk.Status)
		}
		if tk.Attempts != 1 {
			t.Errorf("%s: expected Attempts=1, got %d", r.name, tk.Attempts)
		}
		if tk.Session != "" || !tk.Heartbeat.IsZero() {
			t.Errorf("%s: recovery did not release the claim", r.name)
		}

		// Move it back to NEEDS-REVIEW (a fresh honeybee redid the work) and apply
		// again: now Attempts exceeds the limit -> terminal NEEDS-HUMAN.
		if err := tk.Transition(StatusReview, now); err != nil {
			t.Fatalf("%s: re-review transition errored: %v", r.name, err)
		}
		if err := r.apply(tk); err != nil {
			t.Fatalf("%s: second application errored: %v", r.name, err)
		}
		if tk.Status != StatusHuman {
			t.Errorf("%s: over-limit expected NEEDS-HUMAN, got %s", r.name, tk.Status)
		}
		if tk.Attempts != 2 {
			t.Errorf("%s: expected Attempts=2, got %d", r.name, tk.Attempts)
		}
		// The escalation must carry a concrete human reason (never empty).
		if tk.HumanReason() == "" {
			t.Errorf("%s: escalation to NEEDS-HUMAN left an empty reason", r.name)
		}
	}
}

// TestInvariant_HumanIsTerminalForAgent backs the modeling choice in
// TaskStatus.tla that NEEDS-HUMAN is terminal for the autonomous machine: no
// agent Transition leaves it. Only the operator-driven Resolve reopens it (a
// separate method, exercised by TestResolveReopensHumanTask).
func TestInvariant_HumanIsTerminalForAgent(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	for _, to := range []Status{StatusTODO, StatusReview, StatusArb, StatusDone} {
		tk := &Task{ID: "x", Status: StatusHuman}
		if err := tk.Transition(to, now); err == nil {
			t.Errorf("agent Transition NEEDS-HUMAN->%s must be illegal", to)
		}
		if tk.Status != StatusHuman {
			t.Errorf("failed Transition mutated NEEDS-HUMAN to %s", tk.Status)
		}
	}
}
