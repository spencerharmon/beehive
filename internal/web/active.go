package web

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	return s.activeHoneybeesLive(ctx, sm, p, nil)
}

// activeHoneybeesLive is activeHoneybees with the live-stream-branch set passed
// IN rather than resolved per call. sessionBranchSet is a single whole-hive
// `git for-each-ref` — identical for every submodule — so a page that renders
// every submodule (the dashboard's subViews, /stats' computeStats) resolves it
// ONCE and shares it here, paying one git subprocess for the whole page instead
// of one per submodule (which, over 7 submodules, was the dominant page-load
// cost). live==nil means "resolve it myself" — the path a single-submodule
// caller (or a test) takes; a non-nil (possibly empty) set is used verbatim.
func (s *Server) activeHoneybeesLive(ctx context.Context, sm repo.Submodule, p Plan, live map[string]bool) []activeHoneybee {
	seen := map[string]bool{}
	var out []activeHoneybee
	for _, it := range p.Items {
		if it.Active && !seen[it.Session] {
			seen[it.Session] = true
			out = append(out, activeHoneybee{Session: it.Session, TaskID: it.ID})
		}
	}
	ents := scanSessionDir(sm.SessionsDir())
	if len(ents) == 0 {
		return out // no sessions/ dir yet (a fresh/bootstrap-pending submodule): claims only
	}
	// Collect the stub candidates first, then read+classify their bounded
	// prefixes in PARALLEL. On a mature hive the sessions/ dir accumulates many
	// orphaned stubs (finished passes whose branch is gone but whose stub file
	// lingers), and reading those hundreds of small files SERIALLY was a
	// per-request page-load cost (this runs fresh every dashboard/stats render —
	// it is time-dependent, so it is never memoized).
	type cand struct{ id, branch string }
	var probe []string
	for _, e := range ents {
		id := e.ID
		if seen[id] {
			continue // already counted via its fresh PLAN claim, above
		}
		// A stub is a tiny placeholder (repo.SessionStub — well under
		// stubProbeBytes); a finished transcript is a durable multi-KB body. The
		// parallel scan already carries each file's size, so skip one FAR too big
		// to be a stub without opening it at all — in a mature hive almost every
		// session is a large finished transcript, so this reduces the per-file
		// work to the (already parallelized) stat and keeps this whole-sessions-
		// dir scan (hot on the dashboard/stats/sessions pages) off page-load.
		if e.Size > stubProbeBytes {
			continue // too large to be a stub: a finished transcript
		}
		probe = append(probe, id)
	}
	if len(probe) == 0 {
		return out
	}
	// Read only a bounded prefix of each candidate (its stub marker is the FIRST
	// line) and let ParseSessionStub decide — never the whole file — fanned
	// across the worker pool. "" branch marks a non-stub (durable transcript).
	branches := parallelMap(probe, func(id string) string {
		raw, err := readFilePrefix(filepath.Join(sm.SessionsDir(), id+".md"), stubProbeBytes)
		if err != nil {
			return ""
		}
		branch, isStub := repo.ParseSessionStub(string(raw))
		if !isStub {
			return ""
		}
		return branch
	})
	var cands []cand
	for i, id := range probe {
		if branches[i] != "" {
			cands = append(cands, cand{id: id, branch: branches[i]})
		}
	}
	if len(cands) == 0 {
		return out
	}
	// Resolve the live-branch snapshot only if the caller didn't share one
	// (a whole-page render passes it in, paying the single git for-each-ref
	// once for every submodule instead of once per submodule).
	if live == nil {
		rem, _ := s.git.Remote(ctx)
		live = s.sessionBranchSet(ctx, rem)
	}
	for _, c := range cands {
		if live[c.branch] {
			out = append(out, activeHoneybee{Session: c.id})
		}
	}
	return out
}

// liveBranchSet resolves the whole-hive live-stream-branch snapshot ONCE (a
// single `git for-each-ref`) for a page that renders every submodule, so
// activeHoneybeesLive can share it across all of them instead of re-spawning
// the identical git subprocess per submodule. Best-effort: a git error yields
// an empty set (no claimless stub counts live), never a failed page.
func (s *Server) liveBranchSet(ctx context.Context) map[string]bool {
	// The whole-hive `git for-each-ref` costs tens of ms and is identical for
	// every submodule on a page; it also is not HEAD-keyable (a stub branch
	// appears/vanishes with no commit). A short TTL memo (cachedTTL) makes a
	// polled dashboard/stats pay it at most once per window, and its coarse
	// liveness is invisibly stale next to the minutes-long claim TTL.
	return cachedTTL(s.cache, "live-branch-set", 2*time.Second, func(bg context.Context) map[string]bool {
		rem, _ := s.git.Remote(bg)
		return s.sessionBranchSet(bg, rem)
	})
}

// sessionBranchSet returns the set of session STREAM branches that currently
// exist, as a single in-memory snapshot resolved with ONE `git for-each-ref`
// rather than a git subprocess per branch. A stub's stream branch is deleted by
// the runner only once its pass truly ends, so membership in this set is the
// exact liveness signal activeHoneybees needs for a claimless (Bootstrap/
// Reconcile) pass — the same signal branchTipTime derives per-branch, but
// batched. Both the local ref and, when distributed, the remote-tracking ref
// resolve a branch as live, mirroring branchTipTime's own preference order.
// Best-effort: a git error yields an empty set (no claimless stub counts live),
// never a failed page.
func (s *Server) sessionBranchSet(ctx context.Context, rem string) map[string]bool {
	set := map[string]bool{}
	out, err := s.git.Run(ctx, "for-each-ref", "--format=%(refname:short)", "refs/heads/", "refs/remotes/")
	if err != nil {
		return set
	}
	prefix := ""
	if rem != "" {
		prefix = rem + "/"
	}
	for _, line := range strings.Split(out, "\n") {
		ref := strings.TrimSpace(line)
		if ref == "" {
			continue
		}
		// A local branch is live by its bare name; a remote-tracking ref
		// (<rem>/<branch>) is live by its branch component.
		set[ref] = true
		if prefix != "" && strings.HasPrefix(ref, prefix) {
			set[strings.TrimPrefix(ref, prefix)] = true
		}
	}
	return set
}


// stubProbeBytes bounds how much of a session file readFilePrefix pulls to
// decide stub-ness. A stub is repo.SessionStub — a single HTML-comment marker
// line plus a short human note, well under 512 bytes — and its marker is the
// FIRST line, so this prefix always captures the whole marker (and its stream
// branch) while never reading a finished transcript's multi-KB body.
const stubProbeBytes = 512

// readFilePrefix reads up to n bytes from the head of the file at path. It is
// the bounded-read primitive the hot session scans use to classify a file
// (stub vs finished transcript) without an unbounded os.ReadFile: io.ReadFull
// on a short file returns io.EOF / io.ErrUnexpectedEOF once the file ends, both
// of which mean "read what there was", not a failure.
func readFilePrefix(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	m, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	return buf[:m], nil
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
