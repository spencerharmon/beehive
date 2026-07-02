package selectt

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
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

// beehiveOrigin creates an origin beehive repo with submodule `sm`: ROI.md (v1)
// committed and PLAN.md stamped at that ROI head (in sync, no drift). It returns
// the origin git handle, its dir, and the v1 ROI head sha. A test drives origin
// forward from here (bumpROI/stampPlan) and clones it (cloneHive) to exercise a
// selector that fast-forwards to the published tip before judging drift.
func beehiveOrigin(t *testing.T) (*git.Repo, string, string) {
	t.Helper()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "origin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	og := git.New(dir)
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		og.Run(ctx, a...)
	}
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# agents\n"), 0o644)
	smd := filepath.Join(dir, "submodules", "sm")
	if err := os.MkdirAll(smd, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(smd, "ROI.md"), []byte("intent v1\n"), 0o644)
	if err := og.Commit(ctx, "roi v1"); err != nil {
		t.Fatal(err)
	}
	roiV1, err := og.LastCommit(ctx, "submodules/sm/ROI.md")
	if err != nil || roiV1 == "" {
		t.Fatalf("roiV1: %q %v", roiV1, err)
	}
	os.WriteFile(filepath.Join(smd, "PLAN.md"),
		[]byte("<!-- Beehive-ROI: "+roiV1+" -->\n## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	if err := og.Commit(ctx, "plan stamped v1"); err != nil {
		t.Fatal(err)
	}
	return og, dir, roiV1
}

// cloneHive clones origin into a fresh local checkout (its `origin` remote points
// back at originDir) and returns the local dir + git handle. The clone captures
// origin's CURRENT tip, so where a test clones in the origin timeline decides
// whether the local checkout starts in sync or already drifted.
func cloneHive(t *testing.T, originDir string) (string, *git.Repo) {
	t.Helper()
	ctx := context.Background()
	localDir := filepath.Join(t.TempDir(), "local")
	if _, err := git.New(originDir).Run(ctx, "clone", originDir, localDir); err != nil {
		t.Fatalf("clone: %v", err)
	}
	lg := git.New(localDir)
	for _, a := range [][]string{{"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		lg.Run(ctx, a...)
	}
	return localDir, lg
}

// bumpROI advances submodule sm's ROI.md in origin (leaving PLAN.md stamped at the
// prior head, i.e. introducing drift) and returns the new ROI head sha.
func bumpROI(t *testing.T, og *git.Repo, originDir, body string) string {
	t.Helper()
	ctx := context.Background()
	os.WriteFile(filepath.Join(originDir, "submodules", "sm", "ROI.md"), []byte(body), 0o644)
	if err := og.Commit(ctx, "roi bump"); err != nil {
		t.Fatal(err)
	}
	h, err := og.LastCommit(ctx, "submodules/sm/ROI.md")
	if err != nil || h == "" {
		t.Fatalf("roi head: %q %v", h, err)
	}
	return h
}

// stampPlan restamps submodule sm's PLAN.md in origin at sha (a peer folding the
// ROI delta) and commits it.
func stampPlan(t *testing.T, og *git.Repo, originDir, sha string) {
	t.Helper()
	ctx := context.Background()
	os.WriteFile(filepath.Join(originDir, "submodules", "sm", "PLAN.md"),
		[]byte("<!-- Beehive-ROI: "+sha+" -->\n## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	if err := og.Commit(ctx, "plan restamp"); err != nil {
		t.Fatal(err)
	}
}

func mustOpen(t *testing.T, dir string) *repo.Repo {
	t.Helper()
	rp, err := repo.Open(dir)
	if err != nil {
		t.Fatalf("open %s: %v", dir, err)
	}
	return rp
}

func smOf(t *testing.T, rp *repo.Repo) repo.Submodule {
	t.Helper()
	subs, err := rp.Submodules()
	if err != nil || len(subs) == 0 {
		t.Fatalf("submodules: %v (%d)", err, len(subs))
	}
	return subs[0]
}

// TestSelectReconcilePullsSuppressesAppliedDelta reproduces + fixes the audited
// re-reconcile: the local checkout is stale (PLAN stamped at v1 while ROI has
// advanced to v2), so a naive Select re-emits a reconcile whose delta a peer has
// ALREADY folded + stamped on the published main. With Remote set, Select must
// fast-forward to that tip first, see PLAN now stamped at the ROI head, and yield
// the real Work task instead of a redundant Reconcile.
func TestSelectReconcilePullsSuppressesAppliedDelta(t *testing.T) {
	ctx := context.Background()
	og, originDir, _ := beehiveOrigin(t)              // origin in sync at v1
	roiV2 := bumpROI(t, og, originDir, "intent v2\n") // origin drifts (PLAN v1, ROI v2)
	localDir, lg := cloneHive(t, originDir)           // clone at the drift point
	rp := mustOpen(t, localDir)
	sm := smOf(t, rp)

	// Precondition: the STALE local view (no pull) still shows drift — a naive
	// selector would re-reconcile the already-applied delta.
	pre, err := (&Selector{Repo: rp, Git: lg, TTL: time.Hour}).reconcileRange(ctx, sm)
	if err != nil {
		t.Fatalf("pre reconcileRange: %v", err)
	}
	if pre == "" {
		t.Fatal("precondition: stale local must show drift before the pull")
	}

	// A peer folds the delta and restamps PLAN at the ROI head on origin.
	stampPlan(t, og, originDir, roiV2)

	s := &Selector{Repo: rp, Git: lg, Rand: rand.New(rand.NewSource(1)), TTL: time.Hour, Remote: "origin"}
	got, err := s.Select(ctx)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if got == nil || got.Kind != Work || got.Task.ID != "T1" {
		t.Fatalf("an applied delta must suppress the reconcile and yield Work/T1, got %+v", got)
	}
	// Prove the pull happened: the local PLAN.md now carries the ROI head stamp.
	b, _ := os.ReadFile(filepath.Join(localDir, "submodules/sm/PLAN.md"))
	if !strings.Contains(string(b), roiV2) {
		t.Fatalf("Select did not fast-forward to the peer fold; PLAN still stale:\n%s", string(b))
	}
}

// TestSelectReconcilePullsSurfacesGenuineDrift is the negative control: a genuine
// unreconciled ROI advance published to main must still be selected as Reconcile
// after the pull. The local checkout starts IN SYNC (so a stale selector would
// see nothing to do); the pull surfaces the drift and Select emits a Reconcile
// spanning exactly the v1..v2 ROI range.
func TestSelectReconcilePullsSurfacesGenuineDrift(t *testing.T) {
	ctx := context.Background()
	og, originDir, roiV1 := beehiveOrigin(t) // origin in sync at v1
	localDir, lg := cloneHive(t, originDir)  // clone while still in sync
	rp := mustOpen(t, localDir)
	sm := smOf(t, rp)

	// Precondition: the in-sync local (no pull) sees NO drift — it would miss the
	// reconcile a peer just published.
	pre, err := (&Selector{Repo: rp, Git: lg, TTL: time.Hour}).reconcileRange(ctx, sm)
	if err != nil {
		t.Fatalf("pre reconcileRange: %v", err)
	}
	if pre != "" {
		t.Fatalf("precondition: in-sync local must show no drift, got %q", pre)
	}

	// Origin's ROI advances and is NOT folded — genuine, unreconciled drift on main.
	roiV2 := bumpROI(t, og, originDir, "intent v2\n")

	s := &Selector{Repo: rp, Git: lg, Rand: rand.New(rand.NewSource(1)), TTL: time.Hour, Remote: "origin"}
	got, err := s.Select(ctx)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if got == nil || got.Kind != Reconcile {
		t.Fatalf("pulled genuine drift must select Reconcile, got %+v", got)
	}
	if want := roiV1 + ".." + roiV2; got.DiffRange != want {
		t.Fatalf("reconcile range = %q, want %q", got.DiffRange, want)
	}
}

// TestSelectReconcileNoReEmitAtStampedHead proves idempotency: once PLAN.md is
// stamped at the ROI head, repeated selection cycles never re-emit a reconcile
// and the ROI head does not move — the reconcile fires once and stays quiet.
func TestSelectReconcileNoReEmitAtStampedHead(t *testing.T) {
	_, g, root := hive(t)
	ctx := context.Background()
	sub(root, "a", map[string]string{"ROI.md": "x", "PLAN.md": "placeholder\n"})
	g.Commit(ctx, "seed")
	head, _ := g.LastCommit(ctx, "submodules/a/ROI.md")
	os.WriteFile(filepath.Join(root, "submodules/a/PLAN.md"),
		[]byte("<!-- Beehive-ROI: "+head+" -->\n## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	g.Commit(ctx, "stamp")

	headBefore, _ := g.LastCommit(ctx, "submodules/a/ROI.md")
	for i := 0; i < 2; i++ {
		s, err := sel(root, g).Select(ctx)
		if err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
		if s == nil || s.Kind == Reconcile {
			t.Fatalf("cycle %d: a stamped head must not re-emit a reconcile, got %+v", i, s)
		}
		if s.Kind != Work || s.Task.ID != "T1" {
			t.Fatalf("cycle %d: want Work/T1, got %+v", i, s)
		}
	}
	headAfter, _ := g.LastCommit(ctx, "submodules/a/ROI.md")
	if headBefore != headAfter {
		t.Fatalf("ROI head moved across cycles (%s -> %s); a no-drift reconcile must be a no-op", headBefore, headAfter)
	}
}
