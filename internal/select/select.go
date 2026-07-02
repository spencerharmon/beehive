// Package selectt performs deterministic, no-LLM task selection that always
// yields a workable task: weighted-random over submodules, ROI-reconcile as
// priority 0 (PLAN.md stamp vs ROI.md commit), bootstrap when PLAN absent, then
// GC > arbitration > review > main by priority, dependency-gated, cycle-skipped,
// NEEDS-HUMAN excluded. Dependency gating spans submodules: a TODO task whose dep
// names a linked submodule's task ("<submodule>:<taskid>") is held until that
// task is DONE, and a task on a wait cycle is excluded rather than deadlocked
// (this package owns the combined cross-submodule graph; see graph.go). The
// package name avoids the "select" keyword.
package selectt

import (
	"context"
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
}

// Select walks weighted-random submodules and returns the first workable item.
// nil is returned only when no submodule has any workable task.
func (s *Selector) Select(ctx context.Context) (*Selection, error) {
	// Evaluate selection against the freshest main. main.go only Fetches the
	// remote (never Pulls), so the primary seed selector otherwise reads a stale
	// local main and re-emits a reconcile a peer already folded and stamped into
	// PLAN.md — the audited reconcile_loop. Fast-forward the tracked tip first;
	// best-effort, so no-remote / dirty / diverged / offline all fall back to the
	// current checkout and selection proceeds against it.
	s.pullMainTip(ctx)
	subs, err := s.Repo.Submodules()
	if err != nil {
		return nil, err
	}
	graph, err := LoadEdges(s.Repo)
	if err != nil {
		return nil, err
	}
	order := s.weightedOrder(subs)
	now := time.Now().UTC()
	for _, sm := range order {
		sel, err := s.fromSubmodule(ctx, sm, now, graph)
		if err != nil {
			return nil, err
		}
		if sel != nil {
			return sel, nil
		}
	}
	return nil, nil
}

func (s *Selector) fromSubmodule(ctx context.Context, sm repo.Submodule, now time.Time, graph *Graph) (*Selection, error) {
	if sm.Dormant() {
		return nil, nil
	}
	if sm.NeedsBootstrap() {
		return &Selection{Kind: Bootstrap, Submodule: sm}, nil
	}
	rng, err := s.reconcileRange(ctx, sm)
	if err != nil {
		return nil, err
	}
	if rng != "" {
		return &Selection{Kind: Reconcile, Submodule: sm, DiffRange: rng}, nil
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
	if len(cands) == 0 {
		return nil, nil
	}
	t := s.pickTask(cands)
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
	return &Selection{Kind: kind, Submodule: sm, Task: t}, nil
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

// pullMainTip fast-forwards the repo's checkout to the tracked remote's main so
// selection reads PLAN.md/ROI.md at the tip the swarm has actually converged to
// rather than a stale local main. It is best-effort and never blocks selection:
// a repo with no remote (local-only hives, tests), a non-fast-forward (local
// commits on the branch), a dirty tree, or an offline/failed fetch all leave the
// current checkout untouched and selection proceeds against it. Freshening the
// read at its source is what turns an already-applied reconcile into a no-op
// instead of a redundant, zero-progress pass.
func (s *Selector) pullMainTip(ctx context.Context) {
	if s.Git == nil {
		return
	}
	remote, err := s.Git.Remote(ctx)
	if err != nil || remote == "" {
		return
	}
	_ = s.Git.Pull(ctx, remote, "main")
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

// weightedOrder returns submodules shuffled, each repeated by its weight, so
// higher-weighted submodules are tried first on average. Deterministic per Rand.
func (s *Selector) weightedOrder(subs []repo.Submodule) []repo.Submodule {
	pool := make([]repo.Submodule, 0, len(subs))
	for _, sm := range subs {
		w := s.weight(sm)
		for i := 0; i < w; i++ {
			pool = append(pool, sm)
		}
	}
	s.Rand.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	seen := map[string]bool{}
	out := make([]repo.Submodule, 0, len(subs))
	for _, sm := range pool {
		if !seen[sm.Name] {
			seen[sm.Name] = true
			out = append(out, sm)
		}
	}
	return out
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

// pickTask weighted-randomly chooses one candidate by Task.Weight.
func (s *Selector) pickTask(cands []plan.Task) plan.Task {
	total := 0
	for _, t := range cands {
		w := t.Weight
		if w < 1 {
			w = 1
		}
		total += w
	}
	r := s.Rand.Intn(total)
	for _, t := range cands {
		w := t.Weight
		if w < 1 {
			w = 1
		}
		if r < w {
			return t
		}
		r -= w
	}
	return cands[len(cands)-1]
}
