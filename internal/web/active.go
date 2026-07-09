package web

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
)

// active.go holds the canonical honeybee-active-ness determination
// (active-honeybee-count-unify): the ONE place that decides "is a honeybee
// currently working this submodule", replacing what used to be divergent,
// independently-derived copies — the dashboard's own claim-only tally
// (web.go's subViews), the sessions page's stream-branch heuristic
// (sessionLive/sessionInfos), and the plan view's bare claim check
// (plan.go's projectTask). Every consumer — the dashboard 🐝 counter, the
// sessions page/list, the plan view, and /stats — now routes through
// activeHoneybees (directly, or via the Plan.Bees/PlanItem.Active it
// populates through projectPlan), so they can never again disagree.

// ActiveHoneybee is one currently-working honeybee in the canonical set
// activeHoneybees returns. Key identifies it uniquely within its submodule: a
// claimed PLAN task's id, or — for a pass that claims no PLAN task at all
// (bootstrap/reconcile: singleton-lock-held, not task-claimed; see
// internal/claim's ClaimLock family) — the kind parsed from its session
// file's name (sessionNameRE), so a reconcile/bootstrap pass is still counted
// even though it has no PLAN.md row to derive a claim from. Session/Heartbeat
// carry whichever signal contributed the entry: a claimed task's own
// session+heartbeat, or a taskless pass's live session branch/tip time.
type ActiveHoneybee struct {
	Key       string
	Session   string
	Heartbeat time.Time
}

// claimActiveSet is the claim-only half of the canonical set: every task
// carrying a fresh session+heartbeat claim (plan.Task.Active), keyed by task
// id. It is pure — no I/O beyond the already-parsed tasks — so parsePlan's
// uncached baseline (which has no git/sessions access) can compute a correct,
// if partial (claim-only, i.e. blind to a taskless reconcile/bootstrap pass),
// active set standalone. activeHoneybees below seeds from exactly this and
// unions in the session-derived half a pure function cannot see (resolving a
// live session needs git to check the stream branch).
func claimActiveSet(tasks []*plan.Task, now time.Time, ttl time.Duration) map[string]ActiveHoneybee {
	set := map[string]ActiveHoneybee{}
	for _, t := range tasks {
		if t.Active(now, ttl) {
			set[t.ID] = ActiveHoneybee{Key: t.ID, Session: t.Session, Heartbeat: t.Heartbeat}
		}
	}
	return set
}

// sessionFileLive is the ONE stream-branch-liveness check every consumer
// shares — no second copy may reimplement it. raw is a session file's on-disk
// content and rem the resolved git remote name ("" for a local-only hive). ok
// is false for a durable final transcript (not a stub) or a stub whose named
// branch no longer exists — the session ended without its finalize replacing
// the stub (an orphaned publish). tip is the branch's last-commit time.
// Deliberately NOT gated against any TTL: a running session can go a long
// stretch with no new commit (a quiet turn), so branch EXISTENCE — not commit
// recency — is what tracks the live process (session-liveness-branch-gone's
// fix, preserved here as the one shared implementation).
func (s *Server) sessionFileLive(ctx context.Context, raw, rem string) (branch string, tip time.Time, ok bool) {
	branch, isStub := repo.ParseSessionStub(raw)
	if !isStub {
		return "", time.Time{}, false
	}
	tip, ok = s.branchTipTime(ctx, branch, rem)
	return branch, tip, ok
}

// activeHoneybees is THE canonical function (active-honeybee-count-unify): the
// complete set of honeybees currently working ONE submodule's sessionsDir,
// unioning two signals and deduping by Key so a claimed task's own live
// session never double-counts it:
//
//  1. claimActiveSet(tasks, now, ttl) — every PLAN.md task with a fresh claim.
//  2. Every LIVE session file under sessionsDir (sessionFileLive) whose name
//     (sessionNameRE) keys a pass NOT already active via (1) — this is what
//     counts a reconcile/bootstrap pass, which claims no PLAN task at all, and
//     is exactly what a claim-only tally misses (the ROI's reported divergence:
//     the sessions page showed such a pass live while the dashboard did not).
//
// A task whose claim went stale (past ttl) is dropped from (1), and — because
// a crashed/finished honeybee's session either finalizes (no longer a stub) or
// its branch dies with it — is absent from (2) too, so it never resurfaces via
// the union. Best-effort: an unreadable sessionsDir yields the claim-only half.
func (s *Server) activeHoneybees(ctx context.Context, sessionsDir string, tasks []*plan.Task, now time.Time, ttl time.Duration) map[string]ActiveHoneybee {
	set := claimActiveSet(tasks, now, ttl)
	ents, err := os.ReadDir(sessionsDir)
	if err != nil {
		return set
	}
	rem, _ := s.git.Remote(ctx)
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		stem := strings.TrimSuffix(e.Name(), ".md")
		m := sessionNameRE.FindStringSubmatch(stem)
		if m == nil {
			continue // not the bee-<key>-<epoch>-<pid> convention: nothing to key it by
		}
		key := m[1]
		if _, ok := set[key]; ok {
			continue // already active via its own fresh PLAN claim
		}
		raw, err := os.ReadFile(filepath.Join(sessionsDir, e.Name()))
		if err != nil {
			continue
		}
		if _, tip, live := s.sessionFileLive(ctx, string(raw), rem); live {
			set[key] = ActiveHoneybee{Key: key, Session: stem, Heartbeat: tip}
		}
	}
	return set
}
