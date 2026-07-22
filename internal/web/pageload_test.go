package web

// Page-load performance regression gate.
//
// These tests measure the wall-clock render/load time of beehived's heaviest
// pages (dashboard, /stats, the sessions list, a session page, and the plan
// view) and FAIL when a page regresses past its per-page budget. As a hive
// accumulates many sessions those pages slow down, so the swarm needs an
// automated gate that catches a regression before it ships. This is the gate the
// page-load-optimization work (the "50ms" target) is measured against: the
// budgets below are the current regression CEILING — generous enough that
// today's behavior passes — and are meant to be TIGHTENED as that work lands.
//
// Everything is git/repo-derived and stateless per the submodule invariant: the
// synthetic case builds a session-heavy fixture repo on disk, and the live case
// exercises the real, session-heavy `infra-beehive` hive when it is reachable.
// No out-of-repo state (no opencode-db, no cache dir) is read.

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/config"
	"github.com/spencerharmon/beehive/internal/repo"
)

// Per-page regression ceilings: a page's measured load time must stay under its
// budget. There are two scales because the two performance cases are orders of
// magnitude apart in size — the synthetic fixture is a few hundred sessions; the
// live infra-beehive hive is thousands with deep plan/session history. Both
// scales are set so CURRENT behavior passes (the budget is the ceiling the
// page-load-optimization "50ms" work drives down and then tightens), and both
// are overridable at runtime without a code change:
//   - synthetic: BEEHIVE_PAGELOAD_BUDGET_MS_<PAGE>
//   - live:      BEEHIVE_PAGELOAD_LIVE_BUDGET_MS_<PAGE>
//
// The live stats budget is large ON PURPOSE: today /stats over the real hive
// scans every session transcript and takes tens of seconds — that is exactly the
// regression this gate exists to pin and the optimization work exists to fix.
// When that work lands it slashes the real time and this ceiling gets ratcheted
// down (ideally toward the 50ms target) via the env override or a follow-up edit.
var syntheticBudgets = map[string]time.Duration{
	"dashboard": 2 * time.Second,
	"stats":     5 * time.Second,
	"sessions":  2 * time.Second,
	"session":   3 * time.Second,
	"plan":      2 * time.Second,
}

var liveBudgets = map[string]time.Duration{
	"dashboard": 5 * time.Second,
	"stats":     180 * time.Second,
	"sessions":  30 * time.Second,
	"session":   30 * time.Second,
	"plan":      15 * time.Second,
}

func pageBudget(scale, page string) time.Duration {
	envKey := "BEEHIVE_PAGELOAD_BUDGET_MS_"
	table := syntheticBudgets
	if scale == "live" {
		envKey = "BEEHIVE_PAGELOAD_LIVE_BUDGET_MS_"
		table = liveBudgets
	}
	if v := os.Getenv(envKey + strings.ToUpper(page)); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	d, ok := table[page]
	if !ok {
		return time.Second
	}
	return d
}

// measurePage serves path best-of-iters times against the handler and returns
// the fastest observed latency plus the last status code. Best-of-N (rather than
// a single sample or a mean) discounts GC/scheduler jitter so the gate keys off
// the page's true render cost, not one unlucky sample — a regression shows up as
// the whole distribution shifting, so even the best sample crosses the budget.
// The live case passes iters=1 because a single render there already costs tens
// of seconds; repeating it would only burn wall-clock without sharpening the gate.
func measurePage(h http.Handler, path string, iters int) (time.Duration, int) {
	if iters < 1 {
		iters = 1
	}
	best := time.Duration(math.MaxInt64)
	var code int
	for i := 0; i < iters; i++ {
		w := httptest.NewRecorder()
		start := time.Now()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		took := time.Since(start)
		code = w.Code
		if took < best {
			best = took
		}
	}
	return best, code
}

// gatePage measures one page and fails when it errors or exceeds its budget.
func gatePage(t *testing.T, h http.Handler, scale, page, path string, iters int) {
	t.Helper()
	best, code := measurePage(h, path, iters)
	if code != http.StatusOK {
		t.Fatalf("%s page (%s) returned status %d, want 200", page, path, code)
	}
	budget := pageBudget(scale, page)
	t.Logf("pageload[%s] %-9s %-48s best=%v budget=%v", scale, page, path, best.Round(time.Microsecond), budget)
	if best > budget {
		key := "BEEHIVE_PAGELOAD_BUDGET_MS_"
		if scale == "live" {
			key = "BEEHIVE_PAGELOAD_LIVE_BUDGET_MS_"
		}
		t.Fatalf("PAGELOAD REGRESSION: %s %s page (%s) took %v, exceeds budget %v (set %s%s to retune)",
			scale, page, path, best.Round(time.Microsecond), budget, key, strings.ToUpper(page))
	}
}

// perfTranscript builds a realistic finished (non-stub) session transcript. The
// header line carries the model so /stats reconstructs per-model figures from it
// (git-derived, never the opencode db), and a handful of turns give the sessions
// list and stats scan real content to chew through.
func perfTranscript(sm, branch, model string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# session %s\n\n", branch)
	fmt.Fprintf(&b, "submodule: %s · kind: work · branch: %s · model: %s\n\n", sm, branch, model)
	for turn := 0; turn < 6; turn++ {
		b.WriteString("## user\n\ndo the thing, then verify it and report back with detail.\n\n")
		b.WriteString("## assistant\n\nDid the **work** and verified it across several files and paths.\n\n")
	}
	return b.String()
}

// seedPerfServer builds a session-heavy fixture repo (nSessions transcripts, a
// nTasks-item plan) and returns a Server over it plus a branch name of one
// finished session. This is the synthetic performance case: it always runs, so
// the gate has teeth even when the live hive is not mounted.
func seedPerfServer(t *testing.T, nSessions, nTasks int) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	if err := repo.Init(root); err != nil {
		t.Fatal(err)
	}
	g := exec.Command("git", "init")
	g.Dir = root
	if err := g.Run(); err != nil {
		t.Fatal(err)
	}
	for _, kv := range [][]string{{"user.email", "t@t"}, {"user.name", "t"}} {
		c := exec.Command("git", "config", kv[0], kv[1])
		c.Dir = root
		c.Run()
	}
	const sm = "perf"
	smDir := filepath.Join(root, "submodules", sm)
	if err := os.MkdirAll(smDir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(smDir, repo.ROIFile), []byte("# perf target\n"), 0o644)

	// A large plan stresses the plan view (parse + render every task).
	var plan strings.Builder
	plan.WriteString("<!-- Beehive-ROI: abc123 -->\n# Plan\n\n")
	for i := 0; i < nTasks; i++ {
		status := "DONE"
		if i%3 == 0 {
			status = "TODO"
		}
		fmt.Fprintf(&plan, "## task-%d [%s] <!-- attempts=%d deps= weight=16 -->\n", i, status, i%5)
		fmt.Fprintf(&plan, "implement feature %d with care and detail\nFiles: f%d.go\nDoc: br-task-%d.md\nAccept: works\n\n", i, i, i)
	}
	os.WriteFile(filepath.Join(smDir, repo.PlanFile), []byte(plan.String()), 0o644)

	sessDir := filepath.Join(smDir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	models := []string{"github-copilot/claude-sonnet-5", "github-copilot/claude-opus-4.8", "github-copilot/gpt-5"}
	var oneBranch string
	for i := 0; i < nSessions; i++ {
		branch := fmt.Sprintf("bee-perf-%04d", i)
		if oneBranch == "" {
			oneBranch = branch
		}
		body := perfTranscript(sm, branch, models[i%len(models)])
		if err := os.WriteFile(filepath.Join(sessDir, branch+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	r, err := repo.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(r, config.Defaults(root))
	if err != nil {
		t.Fatal(err)
	}
	return s, oneBranch
}

// TestPageLoadBudgetsSynthetic is the always-on regression gate: it stands up a
// session-heavy synthetic hive and asserts every measured page renders within
// its budget. It defines the target the page-load-optimization work drives down.
func TestPageLoadBudgetsSynthetic(t *testing.T) {
	s, branch := seedPerfServer(t, 400, 120)
	h := s.Routes()
	const iters = 5
	gatePage(t, h, "synthetic", "dashboard", "/", iters)
	gatePage(t, h, "synthetic", "stats", "/stats", iters)
	gatePage(t, h, "synthetic", "sessions", "/submodule/perf/sessions", iters)
	gatePage(t, h, "synthetic", "session", "/submodule/perf/session/"+branch, iters)
	gatePage(t, h, "synthetic", "plan", "/submodule/perf/plan", iters)
}

// liveHiveRoot locates the real, session-heavy infra-beehive hive to exercise as
// the performance case. It honors BEEHIVE_LIVE_REPO first (an explicit pointer),
// then walks up from the test's working directory looking for a beehive repo
// (AGENTS.md + submodules/) that actually has a session-bearing submodule. It
// returns "" when none is reachable so the live case skips cleanly (e.g. a
// standalone submodule CI checkout), keeping `go test ./...` green.
func liveHiveRoot(t *testing.T) string {
	t.Helper()
	consider := func(root string) string {
		if root == "" {
			return ""
		}
		r, err := repo.Open(root)
		if err != nil {
			return ""
		}
		subs, err := r.Submodules()
		if err != nil {
			return ""
		}
		for _, sm := range subs {
			ents, err := os.ReadDir(sm.SessionsDir())
			if err != nil {
				continue
			}
			for _, e := range ents {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
					return root
				}
			}
		}
		return ""
	}
	if env := os.Getenv("BEEHIVE_LIVE_REPO"); env != "" {
		if root := consider(env); root != "" {
			return root
		}
		t.Fatalf("BEEHIVE_LIVE_REPO=%s is not a session-bearing beehive repo", env)
	}
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if root := consider(dir); root != "" {
			return root
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// liveTarget picks a submodule of the live hive that has a plan and a finished
// (non-stub) session to drive the sessions/session/plan pages against. Returns
// the submodule name and one non-stub session branch; ok is false when no
// suitable target exists.
func liveTarget(r *repo.Repo) (name, branch string, ok bool) {
	subs, err := r.Submodules()
	if err != nil {
		return "", "", false
	}
	for _, sm := range subs {
		if _, err := os.Stat(sm.PlanPath()); err != nil {
			continue
		}
		ents, err := os.ReadDir(sm.SessionsDir())
		if err != nil {
			continue
		}
		for _, e := range ents {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(sm.SessionsDir(), e.Name()))
			if err != nil {
				continue
			}
			// Prefer a finished transcript: it renders straight off disk, so the
			// measurement is a pure render (no live-branch git resolution).
			if _, isStub := repo.ParseSessionStub(string(data)); isStub {
				continue
			}
			return sm.Name, strings.TrimSuffix(e.Name(), ".md"), true
		}
	}
	return "", "", false
}

// TestPageLoadBudgetsLiveHive exercises the regression budgets against the real
// infra-beehive hive — the actual session-heavy repo the ROI names — when it is
// reachable. This is the performance case that catches a slowdown the synthetic
// fixture might not, since the live hive carries far more sessions/plan history.
// It skips (never fails) when the live hive is not mounted, and can be skipped
// explicitly with BEEHIVE_PAGELOAD_SKIP_LIVE=1 (its live /stats render alone
// costs tens of seconds today, so a caller who only wants the fast synthetic
// gate can opt out). Each live page is measured ONCE — a single render already
// dominates wall-clock, so repetition would only lengthen the run.
func TestPageLoadBudgetsLiveHive(t *testing.T) {
	if os.Getenv("BEEHIVE_PAGELOAD_SKIP_LIVE") != "" {
		t.Skip("BEEHIVE_PAGELOAD_SKIP_LIVE set; skipping the live-hive performance case")
	}
	root := liveHiveRoot(t)
	if root == "" {
		t.Skip("live infra-beehive hive not reachable (set BEEHIVE_LIVE_REPO to exercise the live performance case)")
	}
	r, err := repo.Open(root)
	if err != nil {
		t.Skipf("live hive at %s did not open: %v", root, err)
	}
	s, err := New(r, config.Defaults(root))
	if err != nil {
		t.Skipf("could not build server over live hive %s: %v", root, err)
	}
	h := s.Routes()
	t.Logf("exercising live hive at %s", root)
	gatePage(t, h, "live", "dashboard", "/", 1)
	gatePage(t, h, "live", "stats", "/stats", 1)

	name, branch, ok := liveTarget(r)
	if !ok {
		t.Log("live hive has no plan+non-stub-session submodule; measured dashboard/stats only")
		return
	}
	gatePage(t, h, "live", "sessions", "/submodule/"+name+"/sessions", 1)
	gatePage(t, h, "live", "session", "/submodule/"+name+"/session/"+branch, 1)
	gatePage(t, h, "live", "plan", "/submodule/"+name+"/plan", 1)
}
