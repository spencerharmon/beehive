package swarm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/checkpolicy"
	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

func nestedCheckGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := git.New(dir).Run(context.Background(), args...)
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return out
}

func nestedCheckWrite(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

type nestedCheckFixture struct {
	r                          *Runner
	sel                        *selectt.Selection
	root, wt, origin, marker   string
	pin, childPin, implementer string
}

// Both levels have a newer remote tip than their gitlink. Updating with --remote
// is therefore just as wrong as leaving the initially initialized old pin alone.
func newNestedCheckFixture(t *testing.T, policy bool, unavailable bool) *nestedCheckFixture {
	t.Helper()
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("BEEHIVE_HONEYBEE", "")
	// TestMain supplies the private global Git identity/config. Do not inherit a
	// hook's repository routing or a shell startup script into these real checks.
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "ENV", "BASH_ENV"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(t.TempDir(), "check-launched")
	check := fmt.Sprintf("touch %q; cmp expected.txt nested/value.txt && cmp expected.txt nested/child/value.txt", marker)
	r, root, wt, sel, origin, _ := reviewMergeFixture(t, check)
	if policy {
		r.CheckPolicy = &checkpolicy.Policy{Sandbox: checkpolicy.SandboxOff}
	}
	f := &nestedCheckFixture{r: r, sel: sel, root: root, wt: wt, origin: origin, marker: marker}

	child := t.TempDir()
	gitInit(t, child)
	var childOld string
	for _, value := range []string{"old", "pinned", "newest"} {
		nestedCheckWrite(t, filepath.Join(child, "value.txt"), value+"\n")
		nestedCheckGit(t, child, "add", "value.txt")
		nestedCheckGit(t, child, "commit", "-qm", value)
		switch value {
		case "old":
			childOld = nestedCheckGit(t, child, "rev-parse", "HEAD")
		case "pinned":
			f.childPin = nestedCheckGit(t, child, "rev-parse", "HEAD")
		}
	}
	nested := t.TempDir()
	gitInit(t, nested)
	nestedCheckGit(t, nested, "submodule", "add", "-q", child, "child")
	var old string
	for _, value := range []string{"old", "pinned", "newest"} {
		childRev := f.childPin
		if value == "old" {
			childRev = childOld
		}
		nestedCheckGit(t, filepath.Join(nested, "child"), "checkout", "-q", childRev)
		nestedCheckWrite(t, filepath.Join(nested, "value.txt"), value+"\n")
		nestedCheckGit(t, nested, "add", ".gitmodules", "child", "value.txt")
		nestedCheckGit(t, nested, "commit", "-qm", value)
		switch value {
		case "old":
			old = nestedCheckGit(t, nested, "rev-parse", "HEAD")
		case "pinned":
			f.pin = nestedCheckGit(t, nested, "rev-parse", "HEAD")
		}
	}

	code := sel.Submodule.RepoDir()
	// Fetching the parent must not hide missing-pin failures before the check
	// boundary; explicit recursive submodule update is still required there.
	nestedCheckGit(t, code, "config", "fetch.recurseSubmodules", "false")
	nestedCheckGit(t, code, "submodule", "add", "-q", nested, "nested")
	nestedCheckGit(t, filepath.Join(code, "nested"), "checkout", "-q", old)
	nestedCheckWrite(t, filepath.Join(code, "expected.txt"), "old\n")
	nestedCheckGit(t, code, "add", ".gitmodules", "nested", "expected.txt")
	nestedCheckGit(t, code, "commit", "-qm", "seed old nested pin")
	nestedCheckGit(t, code, "push", "origin", "HEAD:main")

	// The review worktree starts clean, with both OLD pins initialized.
	nestedCheckGit(t, wt, "fetch", "origin", "main")
	nestedCheckGit(t, wt, "merge", "--ff-only", "origin/main")
	nestedCheckGit(t, wt, "submodule", "update", "--init", "--recursive")
	if got := nestedCheckGit(t, filepath.Join(wt, "nested"), "rev-parse", "HEAD"); got != old {
		t.Fatalf("initial nested HEAD = %s, want old pin %s", got, old)
	}

	impl := filepath.Join(t.TempDir(), "implementer")
	nestedCheckGit(t, code, "clone", "-q", "-b", "bee-R1", origin, impl)
	gitConfig(t, impl)
	nestedCheckGit(t, impl, "merge", "--no-edit", "origin/main")
	nestedCheckWrite(t, filepath.Join(impl, "expected.txt"), "pinned\n")
	pin := f.pin
	if unavailable {
		// A real commit in an unrelated object store, absent from the declared
		// nested remote: cloning succeeds, but fetching this exact gitlink fails.
		missing := t.TempDir()
		gitInit(t, missing)
		nestedCheckWrite(t, filepath.Join(missing, "unpublished.txt"), "unavailable\n")
		nestedCheckGit(t, missing, "add", "unpublished.txt")
		nestedCheckGit(t, missing, "commit", "-qm", "unpublished nested pin")
		pin = nestedCheckGit(t, missing, "rev-parse", "HEAD")
	}
	nestedCheckGit(t, impl, "update-index", "--cacheinfo", "160000,"+pin+",nested")
	nestedCheckGit(t, impl, "add", "expected.txt")
	nestedCheckGit(t, impl, "commit", "-qm", "advance nested pin")
	f.implementer = nestedCheckGit(t, impl, "rev-parse", "HEAD")
	nestedCheckGit(t, impl, "push", "origin", "HEAD:bee-R1")
	return f
}

func (f *nestedCheckFixture) task(t *testing.T) *plan.Task {
	t.Helper()
	b, err := os.ReadFile(f.sel.Submodule.PlanPath())
	if err != nil {
		t.Fatal(err)
	}
	p, err := plan.Parse(string(b))
	if err != nil {
		t.Fatal(err)
	}
	task := p.Find("R1")
	if task == nil {
		t.Fatal("R1 disappeared from PLAN.md")
	}
	return task
}

func (f *nestedCheckFixture) assertPins(t *testing.T, cwd string) {
	t.Helper()
	for path, want := range map[string]string{"nested": f.pin, "nested/child": f.childPin} {
		if got := nestedCheckGit(t, filepath.Join(cwd, path), "rev-parse", "HEAD"); got != want {
			t.Errorf("%s HEAD = %s, want exact gitlink %s", path, got, want)
		}
	}
	if _, err := os.Stat(f.marker); err != nil {
		t.Errorf("real shell check did not launch: %v", err)
	}
}

func (f *nestedCheckFixture) prepareRecovery(t *testing.T, byRecord bool) {
	t.Helper()
	// Simulate a review that pushed its merge, but died before the DONE flip.
	nestedCheckGit(t, f.wt, "fetch", "origin", "bee-R1")
	nestedCheckGit(t, f.wt, "merge", "--no-edit", "origin/bee-R1")
	nestedCheckGit(t, f.wt, "push", "origin", "HEAD:main")
	if byRecord {
		nestedCheckGit(t, f.wt, "push", "origin", "--delete", "bee-R1")
	}
	code := f.sel.Submodule.RepoDir()
	nestedCheckGit(t, code, "submodule", "deinit", "-f", "--all")
	entries, err := os.ReadDir(filepath.Join(code, "nested"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("recovery must start with an empty nested checkout: entries=%v err=%v", entries, err)
	}
	task := f.task(t)
	task.Status = plan.NeedsReview
	task.Attempts = 1
	if byRecord {
		task.ReviewCommit = f.implementer
	}
	nestedCheckWrite(t, f.sel.Submodule.PlanPath(), task.Card()+"\n")
	f.sel.Task = *task
	nestedCheckGit(t, f.root, "add", "submodules/sm/PLAN.md", "submodules/sm/repo")
	nestedCheckGit(t, f.root, "commit", "-qm", "seed interrupted review")
}

func TestReviewMergePreparesNestedPins(t *testing.T) {
	for _, kind := range []selectt.Kind{selectt.Review, selectt.Arbitrate} {
		for _, policy := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/policy=%t", kind, policy), func(t *testing.T) {
				f := newNestedCheckFixture(t, policy, false)
				f.sel.Kind = kind
				hint, revert, err := f.r.finalizeReviewMerge(context.Background(), f.sel, f.wt, f.root)
				if err != nil || hint != "" || revert != "" {
					t.Fatalf("valid merged nested pins must pass: hint=%q revert=%s err=%v", hint, revert, err)
				}
				f.assertPins(t, f.wt)
				if !originMainContains(t, f.origin, f.wt, "feature.txt") {
					t.Error("passing merge was not pushed")
				}
				if task := f.task(t); task.Status != plan.Done || task.Attempts != 0 {
					t.Errorf("valid approve changed status/attempts: %+v", task)
				}
			})
		}
	}
}

func TestReviewMergeRestoresNestedPinsAfterFailure(t *testing.T) {
	f := newNestedCheckFixture(t, false, false)
	before := nestedCheckGit(t, f.wt, "submodule", "status", "--recursive")
	f.r.RunVerify = func(ctx context.Context, dir, name string, args ...string) (verifyOutcome, error) {
		o, err := realRunVerify(ctx, dir, name, args...)
		if err != nil || o.exitErr {
			t.Fatalf("prepared check failed unexpectedly: %+v %v", o, err)
		}
		o.exitErr = true
		return o, nil
	}
	if hint, _, err := f.r.finalizeReviewMerge(context.Background(), f.sel, f.wt, f.root); err != nil || hint == "" {
		t.Fatalf("failed check must reject merge: hint=%q err=%v", hint, err)
	}
	if after := nestedCheckGit(t, f.wt, "submodule", "status", "--recursive"); after != before {
		t.Fatalf("rejected merge left stale pins: before=%s after=%s", before, after)
	}
	if status := nestedCheckGit(t, f.wt, "status", "--porcelain"); status != "" {
		t.Fatalf("rejected merge left dirty checkout: %s", status)
	}
	f.r.RunVerify = nil
	if hint, _, err := f.r.finalizeReviewMerge(context.Background(), f.sel, f.wt, f.root); err != nil || hint != "" {
		t.Fatalf("retry must succeed: hint=%q err=%v", hint, err)
	}
	f.assertPins(t, f.wt)
}

func TestAlreadyMergedRecoveryPreparesNestedPins(t *testing.T) {
	for _, byRecord := range []bool{false, true} {
		for _, policy := range []bool{false, true} {
			t.Run(fmt.Sprintf("by-record=%t/policy=%t", byRecord, policy), func(t *testing.T) {
				f := newNestedCheckFixture(t, policy, false)
				f.prepareRecovery(t, byRecord)
				var done bool
				var err error
				if byRecord {
					done, err = f.r.finalizeIfMergedByRecord(context.Background(), f.sel, f.root)
				} else {
					done, err = f.r.finalizeIfAlreadyMerged(context.Background(), f.sel, "bee-R1", f.root)
				}
				if err != nil || !done {
					t.Fatalf("valid already-merged work must finalize: done=%t err=%v", done, err)
				}
				if task := f.task(t); task.Status != plan.Done || task.Attempts != 1 {
					t.Errorf("empty nested checkout must not reopen valid work: status=%s attempts=%d", task.Status, task.Attempts)
				}
				f.assertPins(t, f.sel.Submodule.RepoDir())
			})
		}
	}
}

func TestUnavailableNestedPinIsInfrastructureError(t *testing.T) {
	for _, path := range []string{"review", "arbitration", "already-merged", "merged-by-record"} {
		for _, policy := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/policy=%t", path, policy), func(t *testing.T) {
				f := newNestedCheckFixture(t, policy, true)
				var err error
				if path == "review" || path == "arbitration" {
					if path == "arbitration" {
						f.sel.Kind = selectt.Arbitrate
					}
					var hint string
					var revert plan.Status
					hint, revert, err = f.r.finalizeReviewMerge(context.Background(), f.sel, f.wt, f.root)
					if hint != "" || revert != "" {
						t.Errorf("nested preparation failure must not request rework: hint=%q revert=%s", hint, revert)
					}
					if originMainContains(t, f.origin, f.wt, "feature.txt") {
						t.Error("unverifiable merge was pushed")
					}
					if task := f.task(t); task.Status != plan.Done || task.Attempts != 0 {
						t.Errorf("infrastructure failure changed approved task: %+v", task)
					}
				} else {
					byRecord := path == "merged-by-record"
					f.prepareRecovery(t, byRecord)
					var handled bool
					if byRecord {
						handled, err = f.r.finalizeIfMergedByRecord(context.Background(), f.sel, f.root)
					} else {
						handled, err = f.r.finalizeIfAlreadyMerged(context.Background(), f.sel, "bee-R1", f.root)
					}
					if handled {
						t.Error("unavailable nested pin must not finalize or reopen work")
					}
					if task := f.task(t); task.Status != plan.NeedsReview || task.Attempts != 1 {
						t.Errorf("infrastructure failure reopened work: status=%s attempts=%d", task.Status, task.Attempts)
					}
				}
				if err == nil {
					t.Error("unavailable nested pin must return an infrastructure error")
				}
				if _, statErr := os.Stat(f.marker); !os.IsNotExist(statErr) {
					t.Errorf("check must not launch before nested preparation succeeds: stat err=%v", statErr)
				}
			})
		}
	}
}

func TestCheckGroundTruthPreparesNestedPins(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		for _, policy := range []bool{false, true} {
			t.Run(fmt.Sprintf("unavailable=%t/policy=%t", unavailable, policy), func(t *testing.T) {
				f := newNestedCheckFixture(t, policy, unavailable)
				otherHead := nestedCheckGit(t, f.wt, "rev-parse", "HEAD")
				otherPins := nestedCheckGit(t, f.wt, "submodule", "status", "--recursive")
				otherStatus := nestedCheckGit(t, f.wt, "status", "--porcelain")
				code := f.sel.Submodule.RepoDir()
				nestedCheckGit(t, code, "submodule", "deinit", "-f", "--all")
				nestedCheckGit(t, code, "fetch", "origin", "bee-R1")
				nestedCheckGit(t, code, "merge", "--ff-only", "origin/bee-R1")
				entries, err := os.ReadDir(filepath.Join(code, "nested"))
				if err != nil || len(entries) != 0 {
					t.Fatalf("ground truth must start with empty nested checkout: entries=%v err=%v", entries, err)
				}
				f.sel.Task = *f.task(t)
				got := f.r.checkGroundTruth(context.Background(), f.sel, f.root)
				if unavailable {
					if !strings.Contains(got, "UNKNOWN") || strings.Contains(got, "PASSED") || strings.Contains(got, "FAILED") {
						t.Errorf("unavailable nested pin must report UNKNOWN, got: %s", got)
					}
					if _, err := os.Stat(f.marker); !os.IsNotExist(err) {
						t.Errorf("unknown ground truth must not launch check: stat err=%v", err)
					}
				} else {
					if !strings.Contains(got, "PASSED (exit 0)") {
						t.Errorf("initialized exact nested pins must report PASSED, got: %s", got)
					}
					f.assertPins(t, code)
				}
				if got := nestedCheckGit(t, f.wt, "rev-parse", "HEAD"); got != otherHead {
					t.Errorf("ground truth moved other checkout: %s -> %s", otherHead, got)
				}
				if got := nestedCheckGit(t, f.wt, "submodule", "status", "--recursive"); got != otherPins {
					t.Errorf("ground truth changed other initialized pins:\nbefore: %s\nafter: %s", otherPins, got)
				}
				if got := nestedCheckGit(t, f.wt, "status", "--porcelain"); got != otherStatus {
					t.Errorf("ground truth dirtied other checkout: %q -> %q", otherStatus, got)
				}
			})
		}
	}
}

func TestCheckPreparesInactiveNestedPins(t *testing.T) {
	f := newNestedCheckFixture(t, false, false)
	code := f.sel.Submodule.RepoDir()
	nestedCheckGit(t, code, "fetch", "origin", "bee-R1")
	nestedCheckGit(t, code, "merge", "--ff-only", "origin/bee-R1")
	nestedCheckGit(t, code, "submodule", "deinit", "-f", "--all")
	nestedCheckGit(t, code, "config", "submodule.nested.active", "false")
	nestedCheckGit(t, code, "config", "submodule.nested.update", "none")
	f.sel.Task = *f.task(t)
	got := f.r.checkGroundTruth(context.Background(), f.sel, f.root)
	if !strings.Contains(got, "PASSED (exit 0)") {
		t.Fatalf("recorded inactive pins still need preparing: %s", got)
	}
	f.assertPins(t, code)
}

func TestCheckRefusesEmptySourceCheckout(t *testing.T) {
	f := newNestedCheckFixture(t, false, false)
	empty := filepath.Join(f.root, "empty")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	o, err := f.r.runCheckIn(context.Background(), f.sel, f.task(t).Check(), empty)
	if err == nil || o.exitErr {
		t.Fatalf("empty checkout must fail preparation, not execute a test: outcome=%+v err=%v", o, err)
	}
	if _, err := os.Stat(f.marker); !os.IsNotExist(err) {
		t.Fatalf("check must not launch in an empty checkout: %v", err)
	}
}

func TestTaskCheckPreparesFreshAndReusedNestedWorktree(t *testing.T) {
	f := newNestedCheckFixture(t, false, false)
	ctx := context.Background()
	code := f.sel.Submodule.RepoDir()
	otherHead := nestedCheckGit(t, code, "rev-parse", "HEAD")
	otherPins := nestedCheckGit(t, code, "submodule", "status", "--recursive")
	wt := filepath.Join(t.TempDir(), "task")
	nestedCheckGit(t, code, "worktree", "add", "--detach", wt, "HEAD")
	entries, err := os.ReadDir(filepath.Join(wt, "nested"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("fresh task worktree must have empty nested source: %v %v", entries, err)
	}
	check := f.task(t).Check()
	// Negative control: this same real check cannot pass on empty dependencies.
	if o, err := realRunVerify(ctx, wt, "sh", "-c", check); err != nil || !o.exitErr {
		t.Fatalf("unprepared check must fail: %+v %v", o, err)
	}
	nestedCheckGit(t, wt, "fetch", "origin", "bee-R1")
	for _, revision := range []string{"HEAD", f.implementer, f.implementer} {
		nestedCheckGit(t, wt, "checkout", "--detach", revision)
		parent := nestedCheckGit(t, wt, "rev-parse", "HEAD")
		o, err := f.r.runCheckIn(ctx, f.sel, check, wt)
		if err != nil || o.exitErr {
			t.Fatalf("prepared task check at %s failed: %+v %v", revision, o, err)
		}
		if got := nestedCheckGit(t, wt, "rev-parse", "HEAD"); got != parent {
			t.Fatalf("preparation moved parent: %s -> %s", parent, got)
		}
		if got := nestedCheckGit(t, wt, "status", "--porcelain"); got != "" {
			t.Fatalf("preparation left stale pins: %s", got)
		}
	}
	f.assertPins(t, wt)
	if got := nestedCheckGit(t, code, "rev-parse", "HEAD"); got != otherHead {
		t.Errorf("preparation moved original parent: %s -> %s", otherHead, got)
	}
	if got := nestedCheckGit(t, code, "submodule", "status", "--recursive"); got != otherPins {
		t.Errorf("preparation changed original nested checkout: %s -> %s", otherPins, got)
	}
}

// Opt-in operational probe: clone without dependencies, then execute the supplied
// check through the runner's real preparation and sandbox boundary. No task state
// or existing checkout is modified; ordinary CI uses the local fixtures above.
func TestLiveNestedTaskCheck(t *testing.T) {
	url := os.Getenv("BEEHIVE_NESTED_CHECK_REPOSITORY")
	if url == "" {
		t.Skip("set BEEHIVE_NESTED_CHECK_REPOSITORY and BEEHIVE_NESTED_CHECK_COMMAND")
	}
	check := os.Getenv("BEEHIVE_NESTED_CHECK_COMMAND")
	if check == "" {
		t.Fatal("BEEHIVE_NESTED_CHECK_COMMAND is required")
	}
	root := t.TempDir()
	wt := filepath.Join(root, "source")
	nestedCheckGit(t, root, "clone", "--no-recurse-submodules", url, wt)
	parent := nestedCheckGit(t, wt, "rev-parse", "HEAD")
	before := nestedCheckGit(t, wt, "submodule", "status")
	if !strings.Contains(before, "-") {
		t.Fatalf("probe requires initially uninitialized nested source: %s", before)
	}
	t.Logf("fresh parent %s; initial nested status:\n%s", parent, before)
	r := &Runner{CheckPolicy: &checkpolicy.Policy{Sandbox: checkpolicy.SandboxAuto}}
	o, err := r.runCheckIn(context.Background(), &selectt.Selection{}, check, wt)
	t.Logf("Check: %s\n%s", check, o.out)
	if err != nil || o.exitErr {
		t.Fatalf("runner-prepared check failed: %+v %v", o, err)
	}
	if got := nestedCheckGit(t, wt, "rev-parse", "HEAD"); got != parent {
		t.Fatalf("preparation moved parent: %s -> %s", parent, got)
	}
	t.Logf("prepared nested status:\n%s", nestedCheckGit(t, wt, "submodule", "status", "--recursive"))
}

func TestReviewMergePushRetryRechecksNestedPins(t *testing.T) {
	for _, policy := range []bool{false, true} {
		t.Run(fmt.Sprintf("policy=%t", policy), func(t *testing.T) {
			f := newNestedCheckFixture(t, policy, false)
			// The peer advances only the outer pin; the child's expected content and
			// exact gitlink stay pinned across both checked revisions.
			childExpected := filepath.Join(t.TempDir(), "child-expected.txt")
			nestedCheckWrite(t, childExpected, "pinned\n")
			task := f.task(t)
			task.Body = []string{"Review nested pins.", fmt.Sprintf("Check: touch %q; cmp expected.txt nested/value.txt && cmp %q nested/child/value.txt", f.marker, childExpected)}
			nestedCheckWrite(t, f.sel.Submodule.PlanPath(), task.Card()+"\n")
			nestedCheckGit(t, f.root, "add", "submodules/sm/PLAN.md")
			nestedCheckGit(t, f.root, "commit", "-qm", "check child independently during retry")
			peer := filepath.Join(t.TempDir(), "peer")
			nestedCheckGit(t, f.wt, "clone", "-q", "-b", "bee-R1", f.origin, peer)
			gitConfig(t, peer)
			var checkedHeads []string
			var peerTip string
			f.r.RunVerify = func(ctx context.Context, dir, name string, args ...string) (verifyOutcome, error) {
				if dir != f.wt || name != "sh" {
					t.Fatalf("check must run in review worktree: dir=%s command=%s %v", dir, name, args)
				}
				o, err := realRunVerify(ctx, dir, name, args...)
				if err != nil || o.exitErr {
					t.Fatalf("real check failed: outcome=%+v err=%v", o, err)
				}
				f.assertPins(t, dir)
				checkedHeads = append(checkedHeads, nestedCheckGit(t, dir, "rev-parse", "HEAD"))
				if len(checkedHeads) == 1 {
					// Race the runner only AFTER its first real check passes. This
					// descendant is not in the checked HEAD, forcing a non-ff push.
					nestedCheckGit(t, peer, "submodule", "update", "--init", "--recursive")
					newPin := nestedCheckGit(t, filepath.Join(peer, "nested"), "rev-parse", "origin/main")
					if newPin == f.pin {
						t.Fatal("peer must advance to a distinct nested pin")
					}
					nestedCheckGit(t, filepath.Join(peer, "nested"), "checkout", "-q", newPin)
					nestedCheckWrite(t, filepath.Join(peer, "expected.txt"), "newest\n")
					nestedCheckGit(t, peer, "add", "nested", "expected.txt")
					nestedCheckGit(t, peer, "commit", "-qm", "peer advances nested pin after first check")
					peerTip = nestedCheckGit(t, peer, "rev-parse", "HEAD")
					nestedCheckGit(t, peer, "push", "origin", "HEAD:main")
					f.pin = newPin
				}
				return o, err
			}
			hint, revert, err := f.r.finalizeReviewMerge(context.Background(), f.sel, f.wt, f.root)
			if err != nil || hint != "" || revert != "" {
				t.Fatalf("push retry must approve checked peer revision: hint=%q revert=%s err=%v", hint, revert, err)
			}
			if len(checkedHeads) != 2 {
				t.Fatalf("changed retry merge must run check again, checked revisions: %v", checkedHeads)
			}
			finalHead := nestedCheckGit(t, f.wt, "rev-parse", "HEAD")
			if finalHead == checkedHeads[0] || finalHead != checkedHeads[len(checkedHeads)-1] {
				t.Errorf("final revision %s must differ from initial check and match last check: %v", finalHead, checkedHeads)
			}
			nestedCheckGit(t, f.wt, "merge-base", "--is-ancestor", peerTip, finalHead)
			if got := nestedCheckGit(t, f.origin, "rev-parse", "main"); got != finalHead {
				t.Errorf("origin/main = %s, want checked final revision %s", got, finalHead)
			}
			f.assertPins(t, f.wt)
			if task := f.task(t); task.Status != plan.Done || task.Attempts != 0 || !task.CommitsSet || len(task.Commits) == 0 {
				t.Errorf("successful retry must preserve approval and stamp commits: %+v", task)
			}
		})
	}
}
