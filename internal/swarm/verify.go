package swarm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spencerharmon/beehive/internal/plan"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

// The handoff verify-gate is the runner-owned MECHANICAL check a Work task's code
// worktree must pass BEFORE its flip to NEEDS-REVIEW is accepted as complete. Its
// job is responsibility-removal: a reviewer — and the arbitration/rework churn a
// reject triggers — must never be spent rejecting a mechanical regression a
// deterministic check catches (audit session-audit-001 F3: whole review sessions
// burned on gofmt/vet/test regressions). Each would-be handoff therefore runs, in
// the code worktree and under the host-mandated static Go env (exportBuildEnv has
// already put CGO_ENABLED=0 etc. into this process, which the child inherits — so
// it is the static invocation, never a `go test -race`):
//
//	1. gofmt -l .    — must print NOTHING (every file is already gofmt-clean)
//	2. go vet ./...  — must exit 0 (no vet findings; it also has to compile)
//	3. go test ./... — must exit 0 (the suite is green on LIVE inputs, not fixtures)
//
// cheap->expensive and fail-fast: the first red is handed straight back, so a
// formatting slip never pays for a full `go test`. Red => the caller does NOT
// complete; it keeps the claim and feeds the failure to the agent as the next
// prompt (fix forward, same session). Green (or inapplicable) => the flip stands.

// verifyOutcome is one gate command's result as the gate consumes it: its combined
// output plus whether the command RAN and exited non-zero (a red). A separate
// non-nil error from the runner means the command could not be run at all — an
// infra failure the caller handles fail-closed (block completion).
type verifyOutcome struct {
	out     string
	exitErr bool
}

// runVerify dispatches one gate command through the injectable seam. A nil
// RunVerify uses realRunVerify (real exec, the process env — already carrying the
// exported BuildEnv — governs the child). Tests set RunVerify to force red/green
// deterministically and to assert the exact static invocation.
func (r *Runner) runVerify(ctx context.Context, dir, name string, args ...string) (verifyOutcome, error) {
	if r.RunVerify != nil {
		return r.RunVerify(ctx, dir, name, args...)
	}
	return realRunVerify(ctx, dir, name, args...)
}

// realRunVerify runs one gate command in dir, inheriting the honeybee process env
// (into which exportBuildEnv has already applied the host BuildEnv, so the child
// runs the mandated static invocation without re-plumbing it here — buildenv.go is
// the single source). A clean exit is green; a process exit-non-zero is a red
// (exitErr set); any OTHER error means the command could not be executed and is
// returned as an infra failure.
func realRunVerify(ctx context.Context, dir, name string, args ...string) (verifyOutcome, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return verifyOutcome{out: string(out)}, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return verifyOutcome{out: string(out), exitErr: true}, nil
	}
	return verifyOutcome{out: string(out)}, err
}

// verifyGate runs the handoff gate for a Work task the agent has just driven to
// NEEDS-REVIEW. Two layers, both scoped to that handoff: (1) an uncommitted-work
// check for EVERY work submodule (a dirty code worktree means edits that were
// never committed and would be silently dropped by finish()); and (2) the Go
// toolchain checks (gofmt/vet/test) for a Go module. It returns "" when both PASS
// or are INAPPLICABLE (any other kind or status, no code worktree, or — for the Go
// checks — a worktree that is not a Go module); a non-empty RED failure prompt to
// hand the agent so it fixes forward; or a non-nil error when a check could not be
// run (fail-closed: the caller blocks completion).
func (r *Runner) verifyGate(ctx context.Context, sel *selectt.Selection, wtAbs string) (string, error) {
	if sel.Kind != selectt.Work || wtAbs == "" {
		return "", nil
	}
	// Only the TODO->NEEDS-REVIEW handoff is gated. A Work pass that escalates
	// (NEEDS-HUMAN) or flags a conflict (NEEDS-ARBITRATION) is a DIFFERENT handoff
	// the ROI does not target, and blocking it would trap a legitimate escalation;
	// a direct-to-DONE Work pass is likewise out of scope. complete() re-reads the
	// plan, so re-read here to learn the specific status it just accepted.
	b, err := os.ReadFile(sel.Submodule.PlanPath())
	if err != nil {
		return "", err
	}
	p, err := plan.Parse(string(b))
	if err != nil {
		return "", err
	}
	t := p.Find(sel.Task.ID)
	if t == nil || t.Status != plan.NeedsReview {
		return "", nil
	}
	// Uncommitted-work gate — applies to EVERY work submodule (Go or not) on the
	// TODO->NEEDS-REVIEW handoff (the only completion edge a work pass may take;
	// DONE is reached only through review/arbitration). A code worktree still
	// carrying ANY change (modified OR untracked) at this handoff means the agent
	// edited/created files but never committed them to bee-<taskid> — so finish()
	// merges an EMPTY branch, the gitlink never advances, and the task lands with
	// NONE of its code. Observed live 2026-07-21 (flux
	// zuul-build-publish-image-base-job): the base-job manifests + push-secret
	// script were written in the worktree, the task flipped NEEDS-REVIEW, but
	// nothing was ever committed; the flux tip stayed at the tip that verifiably
	// lacks the infra, and the dependent gostream task then escalated NEEDS-HUMAN on
	// the still-absent dependency. Red => hand back to commit, same session (fix
	// forward), exactly like the Go checks below. This runs BEFORE the go.mod gate
	// so it protects non-Go submodules (flux, gostream, …) too.
	{
		o, err := r.runVerify(ctx, wtAbs, "git", "status", "--porcelain")
		if err != nil {
			return "", fmt.Errorf("verify gate: running `git status --porcelain` in %s: %w", wtAbs, err)
		}
		if o.exitErr {
			return "", fmt.Errorf("verify gate: `git status --porcelain` failed in %s: %s", wtAbs, strings.TrimSpace(o.out))
		}
		if strings.TrimSpace(o.out) != "" {
			return dirtyTreeFailPrompt(o.out), nil
		}
	}
	// The gate runs the Go toolchain; a worktree with no go.mod at its root is not a
	// Go module and there is nothing to verify — running `go vet ./...` there would
	// only error spuriously (a false red). The beehive submodule always has one.
	if _, err := os.Stat(filepath.Join(wtAbs, "go.mod")); err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	checks := []struct {
		name string
		args []string
	}{
		{"gofmt", []string{"-l", "."}},
		{"go", []string{"vet", "./..."}},
		{"go", []string{"test", "./..."}},
	}
	for _, c := range checks {
		o, err := r.runVerify(ctx, wtAbs, c.name, c.args...)
		if err != nil {
			return "", fmt.Errorf("verify gate: running `%s %s`: %w", c.name, strings.Join(c.args, " "), err)
		}
		label := strings.TrimSpace(c.name + " " + strings.Join(c.args, " "))
		// gofmt -l is green ONLY on exit 0 with NO output; a listed file (unformatted)
		// or a parse error (which also non-zeros) is a red.
		if c.name == "gofmt" {
			if o.exitErr || strings.TrimSpace(o.out) != "" {
				return gateFailPrompt(label, o.out), nil
			}
			continue
		}
		if o.exitErr {
			return gateFailPrompt(label, o.out), nil
		}
	}
	return "", nil
}

// gateVerifyOutputCap bounds how much command output rides back in the fix-forward
// prompt so a large `go test` log cannot blow the turn's token budget. The TAIL is
// kept — Go prints the failing packages/tests last.
const gateVerifyOutputCap = 4000

// dirtyTreeFailPrompt renders the fix-forward continue prompt for the uncommitted-
// work gate: the code worktree still has uncommitted changes at a completion
// handoff, so the task is NOT accepted as done (finish() would merge an empty
// branch and lose every edit). The porcelain listing is tail-capped like the Go
// gate so a large diff cannot blow the turn's token budget.
func dirtyTreeFailPrompt(out string) string {
	out = strings.TrimRight(out, "\n")
	if len(out) > gateVerifyOutputCap {
		out = "…(truncated; showing the tail)\n" + out[len(out)-gateVerifyOutputCap:]
	}
	return fmt.Sprintf(
		"Handoff verify-gate FAILED: your code worktree still has UNCOMMITTED changes, so "+
			"the task is NOT accepted as done — the runner only ever merges commits that already "+
			"exist on your bee-<taskid> branch, so an uncommitted edit would be silently discarded "+
			"and the task would land with none of its code. Commit ALL of these to your "+
			"bee-<taskid> branch (with the `Beehive: <task-id> <doc-path>` stamp) and push it to "+
			"the submodule origin THIS session; if a listed file is scratch that must not ship, "+
			"delete it. Then the gate re-runs automatically (leave the task status as-is). "+
			"`git status --porcelain`:\n\n%s",
		out)
}

// gateFailPrompt renders the fix-forward continue prompt for a red gate: what
// failed, that the task is NOT handed to review until it is green, and the
// (tail-capped) command output to act on.
func gateFailPrompt(label, out string) string {
	out = strings.TrimRight(out, "\n")
	if len(out) > gateVerifyOutputCap {
		out = "…(truncated; showing the tail)\n" + out[len(out)-gateVerifyOutputCap:]
	}
	if strings.TrimSpace(out) == "" {
		out = "(no output)"
	}
	return fmt.Sprintf(
		"Handoff verify-gate FAILED: `%s` did not pass in your worktree, so the task stays "+
			"claimed and is NOT handed to review yet. Fix it in your worktree this session "+
			"(leave the task status as-is; the gate re-runs automatically). Output:\n\n%s",
		label, out)
}
