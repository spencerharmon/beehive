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
// NEEDS-REVIEW. It returns "" when the gate PASSES or is INAPPLICABLE (any other
// kind or status, no code worktree, or a worktree that is not a Go module); a
// non-empty RED failure prompt to hand the agent so it fixes forward; or a non-nil
// error when a check could not be run (fail-closed: the caller blocks completion).
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
