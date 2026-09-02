package web

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
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
//     the runner deletes once the pass truly ends. Because that delete is
//     unreliable (a crashed/killed pass leaves the branch behind), mere
//     existence is NOT enough — the branch must ALSO have advanced its tip
//     within the TTL (sessionBranchSet's recency gate). That recency window
//     is a claimless Bootstrap/Reconcile pass's ONLY liveness signal, since
//     it claims no PLAN task at all, and mirrors the claim model's own
//     heartbeat-within-TTL rule for a claimed pass.
//
// A task whose claim has gone STALE (past the TTL) is excluded by the first
// rule, and — having no session id counted yet — is only reachable via the
// second: it counts only if its OWN session's stream branch is still advancing
// within the TTL. In the common case a dead/stale claim's session either never
// wrote a stub, its branch is long gone (the runner's own deferred cleanup, or
// a later pass's cleanup skill, reclaims it), or its branch tip is older than
// the TTL — so a stale claim reliably drops out of the set.
//
// This is the ONE place a stream-branch liveness check happens; every
// consumer needing "is honeybee X active" reads THIS set (or, for a single
// membership test that must not re-scan sessions/ on a tight poll,
// claimedSessions below for the claim half plus its own targeted stub read —
// see sessionLive/sessionInfos), never a re-derived rule of its own.
func (s *Server) activeHoneybees(ctx context.Context, sm repo.Submodule, p Plan) []activeHoneybee {
	return s.activeHoneybeesLive(ctx, sm, p, nil, time.Now(), s.ttl())
}

// activeHoneybeesLive is activeHoneybees with the live-stream-branch set passed
// IN rather than resolved per call. sessionBranchSet is a single whole-hive
// `git for-each-ref` — identical for every submodule — so a page that renders
// every submodule (the dashboard's subViews, /stats' computeStats) resolves it
// ONCE and shares it here, paying one git subprocess for the whole page instead
// of one per submodule (which, over 7 submodules, was the dominant page-load
// cost). live==nil means "resolve it myself" — the path a single-submodule
// caller (or a test) takes; a non-nil (possibly empty) set is used verbatim.
// now/ttl bound the recency gate the self-resolved set applies (see
// sessionBranchSet) and are ignored when a set is passed in.
func (s *Server) activeHoneybeesLive(ctx context.Context, sm repo.Submodule, p Plan, live map[string]bool, now time.Time, ttl time.Duration) []activeHoneybee {
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
		live = s.sessionBranchSet(ctx, rem, now, ttl)
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
// the identical git subprocess per submodule. now/ttl bound the recency gate
// (see sessionBranchSet): only branches whose tip advanced within ttl of now
// are members. Best-effort: a git error yields an empty set (no claimless stub
// counts live), never a failed page.
func (s *Server) liveBranchSet(ctx context.Context, now time.Time, ttl time.Duration) map[string]bool {
	// The whole-hive `git for-each-ref` costs tens of ms and is identical for
	// every submodule on a page; it also is not HEAD-keyable (a stub branch
	// appears/vanishes with no commit). A short TTL memo (cachedTTL) makes a
	// polled dashboard/stats pay it at most once per window. The memo key carries
	// ttl (the recency window) so a differently-configured server never reads a
	// peer key's set; now is NOT in the key — the 2s memo window is invisibly
	// stale next to the minutes-long liveness ttl, so reusing the window's first
	// `now` is exact enough.
	key := fmt.Sprintf("live-branch-set:%d", int64(ttl))
	return cachedTTL(s.cache, key, 2*time.Second, func(bg context.Context) map[string]bool {
		rem, _ := s.git.Remote(bg)
		return s.sessionBranchSet(bg, rem, now, ttl)
	})
}

// sessionBranchSet returns the set of session STREAM branches that are LIVE, as
// a single in-memory snapshot resolved with ONE `git for-each-ref` rather than a
// git subprocess per branch. Liveness is branch existence GATED ON TIP RECENCY:
// a branch is a member only when its tip advanced within ttl of now.
//
// Mere existence is NOT sufficient. The runner is supposed to delete a session's
// stream branch once its pass truly ends, but a crashed/killed/host-died pass
// (or a deferred cleanup that never runs) leaves the branch behind — a mature
// hive accumulates thousands of such orphans. Keying liveness on bare existence
// therefore reported every one of those long-dead sessions as "running" (the
// recurring false-positive bug). The tip-recency gate mirrors the claim model
// exactly — a claim is live only while its heartbeat is within ttl — so a
// claimless Bootstrap/Reconcile stub (which has no PLAN heartbeat) is live only
// while its transcript stream advanced within the same ttl window. ttl is the
// server's claim TTL (minutes), far wider than 44d9309's failed 15s window, so a
// genuinely running pass mid-quiet-turn stays live while a month-dead orphan
// correctly drops out. Both the local ref and, when distributed, the remote-
// tracking ref are considered, taking the LATER tip (mirroring branchTipTime's
// preference order). Best-effort: a git error yields an empty set (no claimless
// stub counts live), never a failed page.
func (s *Server) sessionBranchSet(ctx context.Context, rem string, now time.Time, ttl time.Duration) map[string]bool {
	set := map[string]bool{}
	// %(committerdate:unix) carries each ref's tip time in the SAME for-each-ref,
	// so the recency gate costs no extra git subprocess.
	out, err := s.git.Run(ctx, "for-each-ref", "--format=%(refname:short) %(committerdate:unix)", "refs/heads/", "refs/remotes/")
	if err != nil {
		return set
	}
	prefix := ""
	if rem != "" {
		prefix = rem + "/"
	}
	cutoff := now.Add(-ttl)
	// Collect the LATEST tip per branch NAME first (a branch may have both a local
	// ref and a fresher remote-tracking ref, or vice versa), then apply the
	// recency gate once so local/remote can never disagree on a name's liveness.
	tips := map[string]time.Time{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		ref, tsStr, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		sec, perr := strconv.ParseInt(strings.TrimSpace(tsStr), 10, 64)
		if perr != nil {
			continue
		}
		tip := time.Unix(sec, 0)
		// Resolve the bare branch name: a remote-tracking ref (<rem>/<branch>)
		// contributes under its branch component; a local ref under its own name.
		name := ref
		if prefix != "" && strings.HasPrefix(ref, prefix) {
			name = strings.TrimPrefix(ref, prefix)
		}
		if cur, seen := tips[name]; !seen || tip.After(cur) {
			tips[name] = tip
		}
	}
	for name, tip := range tips {
		if tip.After(cutoff) {
			set[name] = true
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
