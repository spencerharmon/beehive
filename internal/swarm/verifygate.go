package swarm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

// The handoff verify gate (task handoff-verify-gate) is a runner-owned MECHANICAL
// check that a Work task's code worktree must pass BEFORE its local flip to
// NEEDS-REVIEW is allowed to publish. Audit session-audit-001 found whole
// review/arbitration/rework sessions spent rejecting regressions a deterministic
// check catches (gofmt/vet/test); moving that check into the runner is a
// responsibility-removal that saves those tokens and lets a reviewer judge DESIGN
// rather than mechanics.
//
// The gate rides two seams already owned by the runner:
//   - Runner.VerifyGate: the injectable check (nil = INERT, the byte-identical
//     historical handoff path — so every pre-existing completion test still holds;
//     cmd/honeybee wires DefaultVerifyGate; tests inject stubs).
//   - Runner.BuildEnv: the resolved static host build env (CGO_ENABLED=0 + a
//     redirected GOCACHE/TMPDIR off a broken /tmp) that buildenv.go documents the
//     gate would consume — applied over the process env for every gate subprocess.
//
// On RED the runner WITHHOLDS completion: it reverts the premature NEEDS-REVIEW
// back to TODO (committed, so the claim's per-turn heartbeat keeps the task held
// and the red flip never reaches main) and hands the SAME session the failure to
// fix forward. On GREEN it records the pass durably and lets the normal
// completion/publish proceed.

// gateSteps is the fixed, STATIC mechanical-verification command list the gate
// runs in a Work task's code worktree, in order: gofmt must report no unformatted
// files, then go vet, then the full test suite. The `go test` invocation is
// deliberately a plain `go test ./...` and NEVER `go test -race`: the mandated
// host build env (LOCALS.md — CGO_ENABLED=0 static link) cannot run the race
// detector on this host, so a bare `-race` would fail the gate for an environment
// reason rather than a code one. The accept-criterion "no bare `go test -race` on
// this host" is pinned by TestGateStepsAreStaticNoRace.
func gateSteps() [][]string {
	return [][]string{
		{"gofmt", "-l", "."},
		{"go", "vet", "./..."},
		{"go", "test", "./..."},
	}
}

// DefaultVerifyGate is the production gate cmd/honeybee wires into
// Runner.VerifyGate. It runs gateSteps in the code worktree dir with the runner's
// resolved host build env layered over the process environment, and reports
// pass/fail plus a bounded failure report. The error return is reserved for
// INFRASTRUCTURE faults (a step that could not START — a missing toolchain — or an
// unreadable worktree), never a clean red; a gofmt/vet/test regression is a normal
// (false, report, nil). A worktree with no go.mod (a non-Go target) passes
// trivially: there is nothing to gofmt/vet/test, so the gate never blocks a target
// it cannot mechanically check.
func DefaultVerifyGate(ctx context.Context, dir string, env map[string]string) (bool, string, error) {
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		if os.IsNotExist(err) {
			return true, "", nil
		}
		return false, "", fmt.Errorf("verify gate: stat go.mod in %s: %w", dir, err)
	}
	for _, step := range gateSteps() {
		ok, report, err := runGateStep(ctx, dir, env, step)
		if err != nil {
			return false, "", err
		}
		if !ok {
			return false, report, nil
		}
	}
	return true, "", nil
}

// runGateStep runs one gate command in dir with env applied over the process
// environment. It separates two failure modes: a command that could not START
// (binary missing, dir gone) is an INFRASTRUCTURE error (returned as err — the gate
// neither passes nor fails on it, the caller surfaces it), while a command that RAN
// and exited non-zero is a GATE FAILURE (ok=false + a bounded report), the normal
// red path. gofmt is special: `gofmt -l` exits 0 even when files need formatting
// and instead LISTS them on stdout, so a non-empty output is also a failure.
func runGateStep(ctx context.Context, dir string, env map[string]string, args []string) (bool, string, error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = gateEnv(env)
	out, err := cmd.CombinedOutput()
	label := strings.Join(args, " ")
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			// Ran and exited non-zero: a real gate failure (a vet/test regression).
			return false, gateReport(label, out), nil
		}
		// Could not start: an infrastructure fault, not the agent's regression.
		return false, "", fmt.Errorf("verify gate: running %q: %w", label, err)
	}
	if args[0] == "gofmt" && strings.TrimSpace(string(out)) != "" {
		return false, gateReport(label+" — the following files are not gofmt-clean", out), nil
	}
	return true, "", nil
}

// gateEnv layers the runner's resolved build env over the current process
// environment so gate subprocesses run under the same static host settings
// (CGO_ENABLED=0, redirected GOCACHE/TMPDIR, …) the runner exports for the agent's
// own build/test commands. exec uses the LAST value for a duplicated key, so the
// appended overrides win. A nil/empty env leaves os.Environ() untouched.
func gateEnv(env map[string]string) []string {
	base := os.Environ()
	if len(env) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(env))
	out = append(out, base...)
	for _, k := range sortedKeys(env) {
		out = append(out, k+"="+env[k])
	}
	return out
}

// gateReport formats a bounded failure report: the failed command plus the tail of
// its combined output (the compiler/vet/test errors), capped so a runaway log can't
// bloat the fix-forward prompt handed back to the agent.
func gateReport(label string, out []byte) string {
	return fmt.Sprintf("$ %s\n%s", label, tailBytes(out, 4000))
}

// tailBytes returns the last max bytes of b (trailing newlines trimmed), prefixed
// with a truncation marker when it had to cut, so the most relevant error output —
// which tooling prints last — always survives the cap.
func tailBytes(b []byte, max int) string {
	s := strings.TrimRight(string(b), "\n")
	if len(s) <= max {
		return s
	}
	return "…(truncated)…\n" + s[len(s)-max:]
}

// gateHandoff is the pre-handoff mechanical gate invoked at each Work completion
// site once complete() reports done. It returns (pass, fixForwardPrompt):
//   - pass=true  => allow the normal completion/publish to proceed. On a real gate
//     run it also records the pass durably first.
//   - pass=false => WITHHOLD: the local NEEDS-REVIEW flip has been reverted to TODO
//     and committed (so the claim's heartbeat keeps the still-open task held and the
//     red flip never reaches main), and fixForwardPrompt carries the failing
//     check's output for the SAME session to act on.
//
// It is INERT (always pass, empty prompt, byte-identical historical path) unless
// ALL hold: the seam is wired, the pass is Work, a code worktree exists, and the
// local PLAN status is exactly NEEDS-REVIEW. A direct DONE/NEEDS-ARBITRATION or a
// NEEDS-HUMAN blocker is not the mechanical-handoff case and is never gated
// (NEEDS-HUMAN especially must never be blocked — it is the blocker escape hatch).
func (r *Runner) gateHandoff(ctx context.Context, sel *selectt.Selection, wtAbs, branch string) (bool, string) {
	if r.VerifyGate == nil || sel.Kind != selectt.Work || wtAbs == "" {
		return true, ""
	}
	status, err := r.localStatus(sel)
	if err != nil || status != plan.NeedsReview {
		return true, "" // unreadable, or not the NEEDS-REVIEW handoff -> don't gate
	}
	ok, report, err := r.VerifyGate(ctx, wtAbs, r.BuildEnv)
	if err != nil {
		// Infrastructure fault RUNNING the gate (not a code regression). Fail OPEN so
		// a runner-side toolchain fault never wedges an otherwise-complete handoff;
		// surface it to the journal for the log-review audit.
		fmt.Fprintf(os.Stderr, "honeybee: WARNING verify gate could not run for %s (allowing handoff): %v\n", taskID(sel), err)
		return true, ""
	}
	if ok {
		r.recordVerifyPass(ctx, sel)
		return true, ""
	}
	if rerr := r.revertHandoff(ctx, sel); rerr != nil {
		// The revert is what keeps the flip off main; if it fails, refuse the handoff
		// anyway (report the withholding) and let the stale claim -> GC path re-drive,
		// never publish an ungated NEEDS-REVIEW.
		fmt.Fprintf(os.Stderr, "honeybee: WARNING verify gate: reverting %s NEEDS-REVIEW->TODO failed: %v\n", taskID(sel), rerr)
	}
	if r.Debug != nil {
		fmt.Fprintf(r.Debug, "\n⚠️  verify gate WITHHELD %s handoff (reset to TODO, fixing forward):\n%s\n", taskID(sel), report)
	}
	return false, verifyGatePrompt(sel, branch, report)
}

// localStatus reads the current on-disk PLAN status for the selected task in this
// honeybee's beehive worktree.
func (r *Runner) localStatus(sel *selectt.Selection) (plan.Status, error) {
	b, err := os.ReadFile(sel.Submodule.PlanPath())
	if err != nil {
		return "", err
	}
	p, err := plan.Parse(string(b))
	if err != nil {
		return "", err
	}
	t := p.Find(sel.Task.ID)
	if t == nil {
		return "", fmt.Errorf("verify gate: task %s absent from PLAN.md", sel.Task.ID)
	}
	return t.Status, nil
}

// revertHandoff rewrites the selected task's status from NEEDS-REVIEW back to TODO
// in the honeybee's beehive worktree and commits ONLY PLAN.md, preserving the
// task's session+heartbeat claim (String() round-trips them) so the runner's
// per-turn heartbeat keeps the task held and the next turn stays on the normal
// main completion path rather than tripping ErrResolved. Scoping the commit to
// PLAN.md alone (as claim does) keeps the co-located code worktree out of the index.
func (r *Runner) revertHandoff(ctx context.Context, sel *selectt.Selection) error {
	b, err := os.ReadFile(sel.Submodule.PlanPath())
	if err != nil {
		return err
	}
	p, err := plan.Parse(string(b))
	if err != nil {
		return err
	}
	t := p.Find(sel.Task.ID)
	if t == nil {
		return fmt.Errorf("verify gate: task %s absent from PLAN.md", sel.Task.ID)
	}
	if t.Status != plan.NeedsReview {
		return nil // already not a pending handoff; nothing to revert
	}
	t.Status = plan.TODO
	if err := os.WriteFile(sel.Submodule.PlanPath(), []byte(p.String()), 0o644); err != nil {
		return err
	}
	planRel := filepath.Join("submodules", sel.Submodule.Name, repo.PlanFile)
	if err := r.Git.CommitPaths(ctx, verifyRevertMsg(sel.Task.ID), planRel); err != nil && !errors.Is(err, git.ErrNothing) {
		return err
	}
	return nil
}

// recordVerifyPass durably notes that the mechanical gate passed for this handoff,
// so a reviewer reads it and judges DESIGN rather than re-running mechanics (the
// ROI goal that motivates the gate). Best-effort: it writes a small marker under
// the submodule docs/ and commits only that file; any failure is logged and
// swallowed, never blocking the (green) completion it annotates.
func (r *Runner) recordVerifyPass(ctx context.Context, sel *selectt.Selection) {
	name := sel.Task.ID + "-verify-pass.md"
	abs := filepath.Join(sel.Submodule.Path, "docs", name)
	rel := filepath.Join("submodules", sel.Submodule.Name, "docs", name)
	body := fmt.Sprintf(
		"# verify-gate PASS — %s\n\nThe runner-owned handoff gate ran `gofmt -l .` + `go vet ./...` + "+
			"`go test ./...` (static host build env) against this task's code worktree before the "+
			"NEEDS-REVIEW handoff, and it passed. This handoff carries no mechanical regression — "+
			"reviewers can judge design, not mechanics.\n",
		sel.Task.ID)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		r.gateDebugf("record pass mkdir failed: %v", err)
		return
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		r.gateDebugf("record pass write failed: %v", err)
		return
	}
	if err := r.Git.CommitPaths(ctx, verifyPassMsg(sel.Task.ID), rel); err != nil && !errors.Is(err, git.ErrNothing) {
		r.gateDebugf("record pass commit failed: %v", err)
	}
}

func (r *Runner) gateDebugf(format string, a ...any) {
	if r.Debug != nil {
		fmt.Fprintf(r.Debug, "\n  · verify gate: "+format+"\n", a...)
	}
}

// verifyGatePrompt is the fix-forward instruction handed to the SAME session when
// the gate withholds a handoff: it states the withholding, shows the failing
// check's output, and tells the agent to fix it in the worktree and re-complete.
// The runner has already reverted the premature NEEDS-REVIEW to TODO, so the agent
// simply continues and flips it again once the gate is green.
func verifyGatePrompt(sel *selectt.Selection, branch, report string) string {
	return fmt.Sprintf(
		"The runner's mechanical verify gate FAILED, so your NEEDS-REVIEW handoff was WITHHELD and the "+
			"task was reset to TODO (the flip did not reach main). Fix the failure below in your code "+
			"worktree (submodules/%[1]s/worktrees/%[2]s), then re-complete the task (commit on branch "+
			"%[2]s, push it to the submodule origin, bump the pointer, and flip PLAN.md to NEEDS-REVIEW). "+
			"The gate runs `gofmt -l .`, `go vet ./...` and `go test ./...` under the host build env and "+
			"must be clean — do NOT add `-race`.\n\nFailing check output:\n%[3]s",
		sel.Submodule.Name, branch, report)
}

// verifyRevertMsg / verifyPassMsg keep the gate's PLAN.md/docs commits linkable in
// the frontend, mirroring claim's `Beehive: <id> plan` trailer convention.
func verifyRevertMsg(taskID string) string {
	return fmt.Sprintf("plan: verify gate withheld %s handoff (NEEDS-REVIEW -> TODO)\n\nBeehive: %s plan", taskID, taskID)
}

func verifyPassMsg(taskID string) string {
	return fmt.Sprintf("docs: verify gate passed for %s handoff\n\nBeehive: %s plan", taskID, taskID)
}
