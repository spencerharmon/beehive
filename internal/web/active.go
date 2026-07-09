package web

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/spencerharmon/beehive/internal/repo"
)

// activeHoneybee is one honeybee session currently working a submodule right
// now — the UNIFIED signal every consumer needing a "how many/which honeybees
// are active" figure reads through (active-honeybee-count-unify): the
// dashboard's per-submodule 🐝 counter (subViews), the sessions page/list
// (sessionInfos/sessionLive), and /stats' per-submodule "active now" figure
// (computeStats). Before this fix each of those independently decided
// "honeybee active" its OWN way — the dashboard counted only a fresh
// PLAN-task claim (silently omitting every Bootstrap/Reconcile pass, which
// claims no task at all: those kinds take a singleton .bee-lock-<kind>
// instead, see internal/claim.Claimer.ClaimLock) while the sessions page
// derived liveness purely from whether a session's stream branch still
// existed (a DIFFERENT signal, blind to claim freshness) — so the two could,
// and did, disagree. Session is the one token that names BOTH a PLAN task's
// claim (plan.Task.Session) AND a session's file stem / stream branch
// (internal/swarm.SessionID builds all three from the identical value),
// which is what lets a claim and its own session file be deduped into ONE
// entry instead of double-counted.
type activeHoneybee struct {
	Session string // the claim/session token; also the sessions/<Session>.md file stem
	TaskID  string // the PLAN task this session claims; "" for a claimless Bootstrap/Reconcile pass
}

// activeHoneybees returns the deduped set of honeybees actively working sm
// right now, unioning two signals and NEVER inferring from a task's status:
//
//   - every task in p (sm's OWN projected plan view, e.g. via s.planView)
//     carrying a fresh claim — PlanItem.Active, i.e.
//     internal/plan.Task.Active(now, ttl): a session id and a heartbeat
//     stamped within the TTL. This is the ONLY rule a Work/Review/
//     Arbitration pass needs (it claims the task it works) and is the exact
//     value plan_items.html already renders per task, so the plan view and
//     this function read the identical primitive — never a second,
//     divergent one.
//   - every OTHER (not already counted above) live session stub under
//     sessions/: a stub names a stream branch (repo.ParseSessionStub) that
//     the runner deletes only once the pass truly ends, so the branch's mere
//     existence (branchTipTime's ok, regardless of its tip's age — a running
//     pass can go quiet for a whole turn without writing) is a Bootstrap/
//     Reconcile pass's ONLY liveness signal, since it claims no PLAN task at
//     all.
//
// A task whose claim has gone STALE (past the TTL) is excluded by the first
// rule, and — having no session id counted yet — is only reachable via the
// second: it counts only if its OWN session happens to still have a live
// stub. In the common case a dead/stale claim's session either never wrote a
// stub or its branch is long gone (the runner's own deferred cleanup, or a
// later pass's cleanup skill, reclaims it), so a stale claim reliably drops
// out of the set.
//
// This is the ONE place a stream-branch liveness check happens; every
// consumer needing "is honeybee X active" reads THIS set (or, for a single
// membership test that must not re-scan sessions/ on a tight poll,
// claimedSessions below for the claim half plus its own targeted stub read —
// see sessionLive/sessionInfos), never a re-derived rule of its own.
func (s *Server) activeHoneybees(ctx context.Context, sm repo.Submodule, p Plan) []activeHoneybee {
	seen := map[string]bool{}
	var out []activeHoneybee
	for _, it := range p.Items {
		if it.Active && !seen[it.Session] {
			seen[it.Session] = true
			out = append(out, activeHoneybee{Session: it.Session, TaskID: it.ID})
		}
	}
	ents, err := os.ReadDir(sm.SessionsDir())
	if err != nil {
		return out // no sessions/ dir yet (a fresh/bootstrap-pending submodule): claims only
	}
	rem, _ := s.git.Remote(ctx)
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".md")
		if seen[id] {
			continue // already counted via its fresh PLAN claim, above
		}
		raw, err := os.ReadFile(filepath.Join(sm.SessionsDir(), e.Name()))
		if err != nil {
			continue
		}
		branch, isStub := repo.ParseSessionStub(string(raw))
		if !isStub {
			continue // a durable final transcript: that session has ended
		}
		if _, ok := s.branchTipTime(ctx, branch, rem); ok {
			out = append(out, activeHoneybee{Session: id})
		}
	}
	return out
}

// claimedSessions returns, for p (a submodule's projected plan view), the set
// of session ids currently claiming a task with a fresh heartbeat within the
// TTL — PlanItem.Active, i.e. internal/plan.Task.Active(now, ttl). This is
// EXACTLY activeHoneybees' first pass, factored out so a caller that only
// needs "is session X claimed" (sessionLive's single-session check,
// sessionInfos' own already-in-progress sessions/ walk) reads the identical
// predicate instead of re-deriving it — without paying for a second
// sessions/ directory scan the way calling activeHoneybees itself would.
func claimedSessions(p Plan) map[string]bool {
	out := make(map[string]bool, len(p.Items))
	for _, it := range p.Items {
		if it.Active {
			out[it.Session] = true
		}
	}
	return out
}
