// Package selectt performs deterministic, no-LLM task selection that always
// yields a workable task: it builds ONE cluster-wide pool of every selectable
// item across all submodules, then does a weighted-random draw over the whole
// pool (submodule-weight × task-weight). Reconcile/bootstrap form a priority tier
// within that pool — folded promptly and fairly across every drifted submodule
// (no single high-weight one monopolizes) before work is drawn. A submodule that has drifted (PLAN.md
// stamp vs ROI.md commit) contributes ONLY its reconcile — its own tasks are
// ruled out until the ROI is folded — and contributes nothing at all while a
// concurrent reconcile/bootstrap already holds that submodule's lock. Because the
// draw is cluster-wide, a busy high-weight submodule reconciling never starves
// other submodules' work or their own reconciles (session audit 2026-08-03).
// Bootstrap is offered when PLAN is absent; within a submodule GC > arbitration >
// review > main tier ordering is applied by plan.Candidates, dependency-gated,
// cycle-skipped, NEEDS-HUMAN excluded. Dependency gating spans submodules: a TODO task whose dep
// names a linked submodule's task ("<submodule>:<taskid>") is held until that
// task is DONE, and a task on a wait cycle is excluded rather than deadlocked
// (this package owns the combined cross-submodule graph; see graph.go). A TODO
// task carrying a future `not_before=<RFC3339>` wall-clock gate is likewise held
// out of the ready set until now reaches it (a general delay primitive: backoff,
// TTL/convergence wait, spaced re-check), independently of its deps; see
// plan.Task.NotBeforeReached and plan.Plan.Candidates. The package name avoids
// the "select" keyword.
package selectt

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
)

// Kind names the work a selection yields.
type Kind string

const (
	Reconcile Kind = "reconcile" // priority 0: ROI.md drifted from PLAN stamp
	Bootstrap Kind = "bootstrap" // ROI present, PLAN absent
	Work      Kind = "work"      // a concrete TODO PLAN task (or a stale IN-PROGRESS task to GC)
	Review    Kind = "review"    // a NEEDS-REVIEW task: judge an implementer's branch, do not reimplement
	Arbitrate Kind = "arbitrate" // a NEEDS-ARBITRATION task: settle a reviewer/implementer dispute
)

// emptyTree is git's canonical empty-tree object sha. It is the reconcile diff
// base when PLAN.md carries no prior ROI stamp: `git diff <emptyTree>..<head>`
// yields the entire initial ROI as additions. The previous "ROOT" sentinel was
// not a valid git revision, so the resulting range was unusable.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// Selection is the deterministic result handed to the swarm before launch.
type Selection struct {
	Kind      Kind
	Submodule repo.Submodule
	Task      plan.Task // valid only when Kind == Work
	DiffRange string    // <stamp>..<head> for reconcile, "" otherwise
}

// Selector picks a submodule and task. Rand makes results reproducible for tests
// while still random per-process; TTL drives GC detection.
type Selector struct {
	Repo *repo.Repo
	Git  *git.Repo // beehive repo root, for ROI commit lookup
	Rand *rand.Rand
	TTL  time.Duration
	// Debug, when non-nil, receives best-effort diagnostics (currently only a
	// failed pre-selection refresh). Nil in production/tests keeps selection
	// silent; the refresh miss degrades to evaluating the local tree, so nothing
	// is lost by not logging it.
	Debug io.Writer
}

// The liveness window Candidates uses to decide whether a claimed task is still
// actively held by a live peer is the raw claim TTL: swarm.Runner's mid-turn
// heartbeat keepalive re-stamps the claim every ~TTL/3 for the WHOLE duration of
// even a long turn, so a claim with no heartbeat for a full TTL is genuinely dead
// (duplicate-dispatch-selection-guard, session-audit-015).

// Select builds the cluster-wide candidate pool and returns one weighted-random
// pick. nil is returned only when no submodule has any workable item.
func (s *Selector) Select(ctx context.Context) (*Selection, error) {
	subs, err := s.Repo.Submodules()
	if err != nil {
		return nil, err
	}
	// Refresh the beehive checkout to the freshest published main BEFORE evaluating
	// any tier, so reconcile (priority 0) is judged against origin's PLAN.md/ROI.md
	// rather than a not-yet-pulled local tree. This is the reconcile-dedup guard: the
	// session audit found whole zero-progress passes spawned because a stale local
	// stamp still read as ROI drift even though an earlier pass had already folded and
	// stamped the delta (and pushed it to origin). Pulling first makes "reconcile
	// already applied" a deterministic pre-dispatch no-op. Best-effort: a no-remote
	// hive or a non-fast-forward on this worktree's branch degrades to evaluating the
	// local tree — exactly the prior behavior — never a blocked selection.
	if err := s.refreshTrackedMain(ctx); err != nil && s.Debug != nil {
		fmt.Fprintf(s.Debug, "[select] refresh tracked main failed; evaluating local tree: %v\n", err)
	}
	graph, err := LoadEdges(s.Repo)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	// Build ONE cluster-wide candidate pool, then weighted-random pick across all of
	// it. This is the reconcile-starvation fix (session audit 2026-08-03): the prior
	// design walked submodules in weighted order and returned the FIRST submodule that
	// yielded anything, so a high-weight submodule (e.g. flux, weight 5) that was
	// perpetually ROI-drifted monopolized the pass and starved every other submodule's
	// reconcile AND work for hours. Now each submodule contributes its candidates to a
	// shared pool and the pick is weighted-random over the WHOLE cluster, so a busy
	// submodule reconciling never blocks the rest. A submodule's own tasks are ruled
	// out while it is drifted (its PLAN is stale — it contributes only its reconcile)
	// and entirely while a concurrent reconcile/bootstrap holds its lock (so a pass
	// never wastes itself losing a lock race it can already see is held).
	var pool []candidate
	for _, sm := range subs {
		cs, err := s.collect(ctx, sm, now, graph)
		if err != nil {
			return nil, err
		}
		pool = append(pool, cs...)
	}
	if len(pool) == 0 {
		return nil, nil
	}
	return s.pick(pool), nil
}

// candidate is one selectable item plus its selection weight in the cluster-wide
// pool. For a task the weight is submodule-weight × task-weight; for a reconcile /
// bootstrap it is the submodule weight (task-weight 1 equivalent).
type candidate struct {
	sel    Selection
	weight int
}

// collect returns every selectable candidate a single submodule contributes to the
// cluster pool. A drifted submodule yields ONLY its reconcile (all its tasks are
// ruled out until the ROI is folded); a submodule whose reconcile/bootstrap lock is
// held by a live pass yields nothing (the operation is already in flight).
func (s *Selector) collect(ctx context.Context, sm repo.Submodule, now time.Time, graph *Graph) ([]candidate, error) {
	if sm.Dormant() {
		return nil, nil
	}
	w := s.weight(sm)
	if sm.NeedsBootstrap() {
		if s.lockHeld(sm, string(Bootstrap), now) {
			return nil, nil
		}
		return []candidate{{Selection{Kind: Bootstrap, Submodule: sm}, w}}, nil
	}
	rng, err := s.reconcileRange(ctx, sm)
	if err != nil {
		return nil, err
	}
	if rng != "" {
		// Drifted: the PLAN predates the current ROI, so working any of this
		// submodule's tasks would act on a stale plan. Rule them all out and offer
		// only the reconcile — and nothing at all while a concurrent reconcile
		// already holds the lock (its tasks stay ruled out for the duration).
		if s.lockHeld(sm, string(Reconcile), now) {
			return nil, nil
		}
		return []candidate{{Selection{Kind: Reconcile, Submodule: sm, DiffRange: rng}, w}}, nil
	}
	b, err := os.ReadFile(sm.PlanPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	pl, err := plan.Parse(string(b))
	if err != nil {
		return nil, err
	}
	cands := graphGate(sm, pl.Candidates(now, s.TTL), graph)
	out := make([]candidate, 0, len(cands))
	for _, t := range cands {
		// Tier the selection by the task's own status so the runner claims the right
		// kind of session. A NEEDS-REVIEW / NEEDS-ARBITRATION task becomes Review /
		// Arbitrate (judge existing work); everything else is Work. Candidates already
		// excluded actively-claimed tasks, so a selected task is either unclaimed or
		// holds a stale claim the runner's own claim will overwrite.
		kind := Work
		switch t.Status {
		case plan.StatusReview:
			kind = Review
		case plan.StatusArb:
			kind = Arbitrate
		}
		tw := t.Weight
		if tw < 1 {
			tw = 1
		}
		out = append(out, candidate{Selection{Kind: kind, Submodule: sm, Task: t}, w * tw})
	}
	return out, nil
}

// graphGate filters main-tier (TODO) candidates through the combined
// cross-submodule graph: a task on a wait cycle is excluded, and a task whose
// cross-submodule prerequisite is unauthorized or not DONE is held. Recovery
// tiers (GC stale / arbitration / review) pass through untouched — they exist to
// unstick work, not to start it, so they are never dependency- or cycle-gated.
func graphGate(sm repo.Submodule, cands []plan.Task, graph *Graph) []plan.Task {
	out := make([]plan.Task, 0, len(cands))
	for _, t := range cands {
		if t.Status == plan.StatusTODO {
			node := sm.Name + ":" + t.ID
			if graph.InCycle(node) {
				continue
			}
			blocked := false
			for _, d := range t.Deps {
				if !graph.crossDepSatisfied(sm.Name, d) {
					blocked = true
					break
				}
			}
			if blocked {
				continue
			}
		}
		out = append(out, t)
	}
	return out
}

// reconcileRange returns "<stamp>..<roiHead>" when ROI.md drifted, else "".
func (s *Selector) reconcileRange(ctx context.Context, sm repo.Submodule) (string, error) {
	if _, err := os.Stat(sm.ROIPath()); err != nil {
		return "", nil
	}
	roiPath := "submodules/" + sm.Name + "/" + repo.ROIFile
	head, err := s.Git.LastCommit(ctx, roiPath)
	if err != nil || head == "" {
		return "", err
	}
	stamp, err := sm.ROIStamp()
	if err != nil {
		return "", err
	}
	if stamp == head || strings.HasPrefix(head, stamp) && stamp != "" {
		return "", nil
	}
	from := stamp
	if from == "" {
		from = emptyTree
	}
	return from + ".." + head, nil
}

// refreshTrackedMain fast-forwards the beehive checkout to origin/main via the
// git-remote-ops Pull (--ff-only) so selection — reconcile (priority 0) above all
// — evaluates the freshest published PLAN.md/ROI.md instead of a stale local tree.
// A repo with no remote (single-host install, tests) is a no-op: the local tree is
// already authoritative. --ff-only never creates a merge commit, so a divergent
// branch (e.g. this worktree carries a lost-race commit not on origin) errors
// rather than merging; the caller treats that as a soft miss and evaluates local,
// which is byte-identical to the pre-guard behavior — the refresh only ever makes
// the evaluation FRESHER, never blocks it.
func (s *Selector) refreshTrackedMain(ctx context.Context) error {
	rem, err := s.Git.Remote(ctx)
	if err != nil {
		return err
	}
	if rem == "" {
		return nil
	}
	return s.Git.Pull(ctx, rem, "main")
}

// weight reads submodules/<name>/weight (positive int), default 1.
func (s *Selector) weight(sm repo.Submodule) int {
	b, err := os.ReadFile(filepath.Join(sm.Path, "weight"))
	if err != nil {
		return 1
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// lockHeld reports whether submodule sm's named singleton lock
// (submodules/<sm>/.bee-lock-<name>) is currently held by a live pass — i.e. its
// heartbeat ts is within TTL. Mirrors claim.readLock + claim.lockActive without
// importing the claim package (avoids an import cycle). A missing/malformed lock
// file reads as unlocked.
func (s *Selector) lockHeld(sm repo.Submodule, name string, now time.Time) bool {
	b, err := os.ReadFile(filepath.Join(sm.Path, ".bee-lock-"+name))
	if err != nil {
		return false
	}
	lines := strings.SplitN(strings.TrimSpace(string(b)), "\n", 2)
	if len(lines) < 2 {
		return false
	}
	ts, err := strconv.ParseInt(strings.TrimSpace(lines[1]), 10, 64)
	if err != nil {
		return false
	}
	return ts != 0 && now.Sub(time.Unix(ts, 0)) < s.TTL
}

// pick chooses one candidate from the cluster pool weighted-randomly by its weight
// (submodule-weight × task-weight), the same weighted draw the old per-submodule
// pickTask used — now applied across the whole cluster.
func (s *Selector) pick(pool []candidate) *Selection {
	// Reconcile/bootstrap are a priority tier WITHIN the cluster pool: a drifted
	// submodule contributes nothing but its reconcile (its own tasks are ruled out
	// until the ROI is folded), so a weight-1 reconcile must not languish behind the
	// whole cluster's work pool — fold it promptly. When any priority candidate is
	// present, draw (weighted) among ONLY those, fairly across EVERY drifted submodule
	// so no single high-weight one monopolizes and none starves. When none is present
	// (nothing drifted, or every pending reconcile's lock is already held and was
	// excluded upstream in collect) the pass still does useful work by drawing from
	// the work pool — never a wasted pass.
	if pri := priorityOnly(pool); len(pri) > 0 {
		pool = pri
	}
	total := 0
	for i := range pool {
		if pool[i].weight < 1 {
			pool[i].weight = 1
		}
		total += pool[i].weight
	}
	r := s.Rand.Intn(total)
	for i := range pool {
		if r < pool[i].weight {
			sel := pool[i].sel
			return &sel
		}
		r -= pool[i].weight
	}
	sel := pool[len(pool)-1].sel
	return &sel
}

// priorityOnly returns just the reconcile/bootstrap candidates from the pool (the
// PLAN-freshness tier that outranks work when present).
func priorityOnly(pool []candidate) []candidate {
	var pri []candidate
	for _, c := range pool {
		if c.sel.Kind == Reconcile || c.sel.Kind == Bootstrap {
			pri = append(pri, c)
		}
	}
	return pri
}
