package selectt

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/repo"
)

func hive(t *testing.T) (*repo.Repo, *git.Repo, string) {
	t.Helper()
	root := t.TempDir()
	ctx := context.Background()
	g := git.New(root)
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		g.Run(ctx, a...)
	}
	repo.Init(root)
	rp, _ := repo.Open(root)
	return rp, g, root
}

func sub(root, name string, files map[string]string) {
	d := filepath.Join(root, "submodules", name)
	os.MkdirAll(d, 0o755)
	for f, b := range files {
		os.WriteFile(filepath.Join(d, f), []byte(b), 0o644)
	}
}

func sel(root string, g *git.Repo) *Selector {
	rp, _ := repo.Open(root)
	return &Selector{Repo: rp, Git: g, Rand: rand.New(rand.NewSource(1)), TTL: time.Hour}
}

func TestSelectWork(t *testing.T) {
	_, g, root := hive(t)
	sub(root, "a", map[string]string{"ROI.md": "x", "PLAN.md": "## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"})
	g.Commit(context.Background(), "seed")
	head, _ := g.LastCommit(context.Background(), "submodules/a/ROI.md")
	os.WriteFile(filepath.Join(root, "submodules/a/PLAN.md"), []byte("<!-- Beehive-ROI: "+head+" -->\n## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	g.Commit(context.Background(), "stamp")
	s, err := sel(root, g).Select(context.Background())
	if err != nil || s == nil {
		t.Fatalf("sel %v %v", s, err)
	}
	if s.Kind != Work || s.Task.ID != "T1" {
		t.Fatalf("got %+v", s)
	}
}

func TestDormantSkipped(t *testing.T) {
	_, g, root := hive(t)
	sub(root, "a", map[string]string{}) // no ROI -> dormant
	g.Commit(context.Background(), "seed")
	s, _ := sel(root, g).Select(context.Background())
	if s != nil {
		t.Fatalf("dormant selected: %+v", s)
	}
}

func TestBootstrap(t *testing.T) {
	_, g, root := hive(t)
	sub(root, "a", map[string]string{"ROI.md": "x"}) // ROI no PLAN
	g.Commit(context.Background(), "seed")
	s, _ := sel(root, g).Select(context.Background())
	if s == nil || s.Kind != Bootstrap {
		t.Fatalf("want bootstrap, got %+v", s)
	}
}

func TestReconcilePriority0(t *testing.T) {
	_, g, root := hive(t)
	// PLAN stamped to an old sha but ROI committed later -> drift.
	sub(root, "a", map[string]string{"ROI.md": "x", "PLAN.md": "<!-- Beehive-ROI: dead -->\n## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"})
	g.Commit(context.Background(), "seed")
	s, _ := sel(root, g).Select(context.Background())
	if s == nil || s.Kind != Reconcile || s.DiffRange == "" {
		t.Fatalf("want reconcile, got %+v", s)
	}
}

// stampAll seeds each submodule's PLAN.md with the ROI commit sha so the selector
// sees no reconcile drift and proceeds to Work selection. Call after committing.
func stampAll(t *testing.T, g *git.Repo, root string, names ...string) {
	t.Helper()
	ctx := context.Background()
	for _, n := range names {
		head, err := g.LastCommit(ctx, "submodules/"+n+"/ROI.md")
		if err != nil || head == "" {
			t.Fatalf("ROI head for %s: %q %v", n, head, err)
		}
		p := filepath.Join(root, "submodules", n, "PLAN.md")
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("<!-- Beehive-ROI: "+head+" -->\n"+string(b)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.Commit(ctx, "stamp"); err != nil {
		t.Fatal(err)
	}
}

const linkAB = "submodules:\n  - a\n  - b\n"

func TestLinkedDepGatesSelection(t *testing.T) {
	_, g, root := hive(t)
	// a:A1 depends on linked b:B1, which is not DONE -> A1 is held. Only b:B1 is
	// selectable, so it must be the pick regardless of submodule order.
	sub(root, "a", map[string]string{
		"ROI.md":               "x",
		"SUBMODULE-LINKS.yaml": linkAB,
		"PLAN.md":              "## A1 [TODO] <!-- attempts=0 deps=b:B1 -->\ngo\n",
	})
	sub(root, "b", map[string]string{
		"ROI.md":               "x",
		"SUBMODULE-LINKS.yaml": linkAB,
		"PLAN.md":              "## B1 [TODO] <!-- attempts=0 deps= -->\ngo\n",
	})
	g.Commit(context.Background(), "seed")
	stampAll(t, g, root, "a", "b")
	s, err := sel(root, g).Select(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s == nil || s.Submodule.Name != "b" || s.Task.ID != "B1" {
		t.Fatalf("cross-submodule dep should gate A1; want B1 in b, got %+v", s)
	}
}

func TestLinkedDepDoneUnblocks(t *testing.T) {
	_, g, root := hive(t)
	// b:B1 is DONE, so a:A1's cross-submodule prerequisite is satisfied. b has no
	// other selectable task, so A1 in a is the only pick.
	sub(root, "a", map[string]string{
		"ROI.md":               "x",
		"SUBMODULE-LINKS.yaml": linkAB,
		"PLAN.md":              "## A1 [TODO] <!-- attempts=0 deps=b:B1 -->\ngo\n",
	})
	sub(root, "b", map[string]string{
		"ROI.md":               "x",
		"SUBMODULE-LINKS.yaml": linkAB,
		"PLAN.md":              "## B1 [DONE] <!-- attempts=0 deps= -->\ngo\n",
	})
	g.Commit(context.Background(), "seed")
	stampAll(t, g, root, "a", "b")
	s, err := sel(root, g).Select(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s == nil || s.Submodule.Name != "a" || s.Task.ID != "A1" {
		t.Fatalf("DONE dep should unblock A1; got %+v", s)
	}
}

func TestCrossDepRequiresLink(t *testing.T) {
	_, g, root := hive(t)
	// a:A1 depends on b:B1 but a declares NO link to b. Even though B1 is DONE the
	// dependency is unauthorized, so A1 is held; b has no other task -> nothing
	// selectable.
	sub(root, "a", map[string]string{
		"ROI.md":  "x",
		"PLAN.md": "## A1 [TODO] <!-- attempts=0 deps=b:B1 -->\ngo\n",
	})
	sub(root, "b", map[string]string{
		"ROI.md":  "x",
		"PLAN.md": "## B1 [DONE] <!-- attempts=0 deps= -->\ngo\n",
	})
	g.Commit(context.Background(), "seed")
	stampAll(t, g, root, "a", "b")
	s, err := sel(root, g).Select(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s != nil {
		t.Fatalf("unlinked cross-submodule dep must gate selection, got %+v", s)
	}
}

func TestCyclicTasksNotSelected(t *testing.T) {
	_, g, root := hive(t)
	// a:A1 -> b:B1 -> a:A1 forms a cross-submodule wait cycle. Both tasks are on
	// the cycle and must be excluded from selection.
	sub(root, "a", map[string]string{
		"ROI.md":               "x",
		"SUBMODULE-LINKS.yaml": linkAB,
		"PLAN.md":              "## A1 [TODO] <!-- attempts=0 deps=b:B1 -->\ngo\n",
	})
	sub(root, "b", map[string]string{
		"ROI.md":               "x",
		"SUBMODULE-LINKS.yaml": linkAB,
		"PLAN.md":              "## B1 [TODO] <!-- attempts=0 deps=a:A1 -->\ngo\n",
	})
	g.Commit(context.Background(), "seed")
	stampAll(t, g, root, "a", "b")

	rp, _ := repo.Open(root)
	graph, err := LoadEdges(rp)
	if err != nil {
		t.Fatal(err)
	}
	if graph.Validate() == nil {
		t.Fatal("Validate: want a cycle in a:A1 <-> b:B1")
	}
	if !graph.InCycle("a:A1") || !graph.InCycle("b:B1") {
		t.Fatalf("both cycle nodes must be flagged: %+v", graph.cyclic)
	}
	s, err := sel(root, g).Select(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s != nil {
		t.Fatalf("cyclic tasks must not be selected, got %+v", s)
	}
}

// stampedPlan writes a submodule with ROI.md + a PLAN.md whose ROI stamp matches
// the committed ROI head, so reconcile (priority 0) does not preempt the task
// tiers under test.
func stampedPlan(t *testing.T, root string, g *git.Repo, name, planBody string) {
	t.Helper()
	ctx := context.Background()
	sub(root, name, map[string]string{"ROI.md": "x", "PLAN.md": "placeholder\n"})
	g.Commit(ctx, "seed "+name)
	head, _ := g.LastCommit(ctx, "submodules/"+name+"/ROI.md")
	os.WriteFile(filepath.Join(root, "submodules", name, "PLAN.md"),
		[]byte("<!-- Beehive-ROI: "+head+" -->\n"+planBody), 0o644)
	g.Commit(ctx, "stamp "+name)
}

// TestSelectReviewKind: a NEEDS-REVIEW task must be selected as Kind=Review (not
// Work) so the runner does NOT claim/clobber it to IN-PROGRESS and the agent
// reviews instead of re-implementing. Review tier outranks a TODO task.
func TestSelectReviewKind(t *testing.T) {
	_, g, root := hive(t)
	stampedPlan(t, root, g,
		"a", "## R1 [NEEDS-REVIEW] <!-- attempts=0 deps= -->\nreview me\n## T1 [TODO] <!-- attempts=0 deps= -->\ntodo\n")
	s, err := sel(root, g).Select(context.Background())
	if err != nil || s == nil {
		t.Fatalf("sel %v %v", s, err)
	}
	if s.Kind != Review || s.Task.ID != "R1" {
		t.Fatalf("want Review/R1, got %+v", s)
	}
}

// TestSelectArbitrateKind: a NEEDS-ARBITRATION task selects as Kind=Arbitrate.
func TestSelectArbitrateKind(t *testing.T) {
	_, g, root := hive(t)
	stampedPlan(t, root, g,
		"a", "## A1 [NEEDS-ARBITRATION] <!-- attempts=1 deps= -->\nsettle me\n## T1 [TODO] <!-- attempts=0 deps= -->\ntodo\n")
	s, err := sel(root, g).Select(context.Background())
	if err != nil || s == nil {
		t.Fatalf("sel %v %v", s, err)
	}
	if s.Kind != Arbitrate || s.Task.ID != "A1" {
		t.Fatalf("want Arbitrate/A1, got %+v", s)
	}
}

// TestSelectSkipsActiveClaim: a task held by a fresh session+heartbeat is NOT
// selected — the deterministic first guard against working someone else's task.
func TestSelectSkipsActiveClaim(t *testing.T) {
	_, g, root := hive(t)
	now := time.Now().UTC().Format(time.RFC3339)
	stampedPlan(t, root, g,
		"a", "## H1 [TODO] <!-- attempts=0 deps= session=bee-other heartbeat="+now+" -->\nheld\n")
	s, err := sel(root, g).Select(context.Background())
	if err != nil {
		t.Fatalf("sel err %v", err)
	}
	if s != nil {
		t.Fatalf("actively-claimed task must not be selected, got %+v", s)
	}
}

// TestSelectReclaimsStaleClaim: a task whose claim heartbeat expired IS selectable
// (the owner died); the selecting bee's own claim will overwrite the dead stamp.
func TestSelectReclaimsStaleClaim(t *testing.T) {
	_, g, root := hive(t)
	stampedPlan(t, root, g,
		"a", "## S1 [TODO] <!-- attempts=0 deps= session=bee-dead heartbeat=2000-01-01T00:00:00Z -->\nstale\n")
	s, err := sel(root, g).Select(context.Background())
	if err != nil || s == nil {
		t.Fatalf("sel %v %v", s, err)
	}
	if s.Kind != Work || s.Task.ID != "S1" {
		t.Fatalf("want Work/S1 (reclaimed), got %+v", s)
	}
}

// TestReconcileRangeEmptyBase proves that with no prior ROI stamp the reconcile
// diff base is git's empty-tree sha (a valid revision) — NOT the bogus "ROOT"
// sentinel — so `git diff <empty-tree>..<head>` yields the full initial ROI as
// additions instead of erroring on an unknown revision.
func TestReconcileRangeEmptyBase(t *testing.T) {
	_, g, root := hive(t)
	// ROI.md present and committed, PLAN.md present but UNSTAMPED -> empty base.
	sub(root, "a", map[string]string{"ROI.md": "x", "PLAN.md": "## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"})
	ctx := context.Background()
	g.Commit(ctx, "seed")

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	var sm repo.Submodule
	for _, s := range subs {
		if s.Name == "a" {
			sm = s
		}
	}
	head, err := g.LastCommit(ctx, "submodules/a/ROI.md")
	if err != nil || head == "" {
		t.Fatalf("ROI head: %q %v", head, err)
	}
	rng, err := sel(root, g).reconcileRange(ctx, sm)
	if err != nil {
		t.Fatalf("reconcileRange: %v", err)
	}
	if want := emptyTree + ".." + head; rng != want {
		t.Fatalf("empty-base range: got %q want %q", rng, want)
	}
	// The range must be a VALID git diff argument ("ROOT" was not): the empty-tree
	// base makes the whole ROI show up as additions.
	if _, err := g.Run(ctx, "diff", "--stat", rng); err != nil {
		t.Fatalf("range %q is not a valid git diff arg: %v", rng, err)
	}
}

// TestReconcilePrefixStampNoDrift proves a SHORT ROI stamp that prefixes the full
// head sha is treated as up-to-date (reconcileRange returns "" — no drift), the
// selection-side mirror of Runner.reconciled's prefix match.
func TestReconcilePrefixStampNoDrift(t *testing.T) {
	_, g, root := hive(t)
	sub(root, "a", map[string]string{"ROI.md": "x", "PLAN.md": "placeholder\n"})
	ctx := context.Background()
	g.Commit(ctx, "seed")
	head, _ := g.LastCommit(ctx, "submodules/a/ROI.md")
	// Stamp PLAN.md with only the first 12 chars of the full ROI head sha.
	os.WriteFile(filepath.Join(root, "submodules/a/PLAN.md"),
		[]byte("<!-- Beehive-ROI: "+head[:12]+" -->\n## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"), 0o644)

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	var sm repo.Submodule
	for _, s := range subs {
		if s.Name == "a" {
			sm = s
		}
	}
	rng, err := sel(root, g).reconcileRange(ctx, sm)
	if err != nil {
		t.Fatalf("reconcileRange: %v", err)
	}
	if rng != "" {
		t.Fatalf("short prefix stamp must read as no-drift, got range %q", rng)
	}
}
