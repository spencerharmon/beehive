package swarm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"

	"github.com/spencerharmon/beehive/internal/plan"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

// The handoff gate is the runner-owned check a Work task's code worktree must pass
// BEFORE its flip to NEEDS-REVIEW is accepted as complete. It verifies exactly ONE
// thing, and it is a PROTOCOL invariant, not a code-correctness judgment:
//
//	the code worktree has NO uncommitted work (`git status --porcelain` is empty).
//
// This is the division of labor the whole swarm rests on (see
// docs/runner-protocol-vs-correctness.md): the RUNNER verifies adherence to the
// honeybee protocol — a change doc exists, the plan status transitioned, and the
// work the agent did is actually COMMITTED so finish() can merge it — and NEVER
// judges whether that code is correct. CORRECTNESS is owned by the honeybees
// themselves: a work agent writes and runs its own regression test, a review agent
// re-verifies the evidence, an arbiter breaks ties — using whatever unit tests,
// integration tests, or build-pipeline results the target's INFRASTRUCTURE.md /
// LOCALS.md / submodule AGENTS.md describe. The runner cannot know a submodule's
// toolchain and must not assume one; a git worktree is either clean or it is not,
// and that is a fact the runner CAN and MUST check in any language.
//
// Why this specific check: finish() merges only commits that already exist on the
// bee-<taskid> branch. An agent that edits/creates files in the worktree but never
// commits them leaves finish() merging an EMPTY branch — the gitlink never
// advances and the task lands with NONE of its code (observed live 2026-07-21,
// flux zuul-build-publish-image-base-job: the base-job manifests + push-secret
// script were written in the worktree, the task flipped NEEDS-REVIEW, nothing was
// ever committed, and the dependent gostream task then escalated NEEDS-HUMAN on the
// still-absent dependency). A dirty tree at this handoff is that bug in progress.
// Red => the caller does NOT complete: it keeps the claim and feeds the failure to
// the agent as the next prompt (commit forward, same session). Clean (or
// inapplicable) => the flip stands.

// verifyOutcome is the gate command's result as the gate consumes it: its combined
// output plus whether the command RAN and exited non-zero. A separate non-nil
// error from the runner means the command could not be run at all — an infra
// failure the caller handles fail-closed (block completion).
type verifyOutcome struct {
	out     string
	exitErr bool
}

// runVerify dispatches the gate command through the injectable seam. A nil
// RunVerify uses realRunVerify (real exec); tests set RunVerify to force a
// clean/dirty tree deterministically and to assert the exact invocation.
func (r *Runner) runVerify(ctx context.Context, dir, name string, args ...string) (verifyOutcome, error) {
	if r.RunVerify != nil {
		return r.RunVerify(ctx, dir, name, args...)
	}
	return realRunVerify(ctx, dir, name, args...)
}

// realRunVerify runs the gate command in dir. A clean exit is a pass; a process
// exit-non-zero is a red (exitErr set); any OTHER error means the command could
// not be executed and is returned as an infra failure.
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

// verifyGate runs the handoff protocol gate for a Work task the agent has just
// driven to NEEDS-REVIEW. It enforces TWO protocol invariants, both language-
// agnostic and neither a correctness judgment (see
// docs/runner-protocol-vs-correctness.md):
//
//  1. the CODE worktree (wtAbs) carries NO uncommitted work — else finish() would
//     merge an empty bee-<taskid> branch and the task lands with none of its code;
//  2. the change DOC is COMMITTED in the HIVE worktree (hiveAbs) — else the task
//     lands NEEDS-REVIEW on main with NO change doc.
//
// Invariant 2 exists because of an asymmetry: the runner's own claim/heartbeat/
// release commits carry the agent's PLAN.md status flip to main (planRel is
// runner-committed), but NOTHING commits the change doc for the agent. An agent
// that writes the doc to disk but never commits it satisfies the on-disk
// docPresent completion check, yet publish (which pushes only committed HEAD)
// carries the PLAN flip and NOT the untracked doc — the task lands NEEDS-REVIEW
// with no doc, the reviewer rejects a doc-less handoff, and the task thrashes
// review->arbitration->TODO (observed 2026-07-22, flux
// zuul-github-readonly-image-source). Requiring the doc committed at THIS handoff
// keeps the flip and its doc atomic on main.
//
// It returns "" when both invariants hold or the gate is INAPPLICABLE (any other
// kind or status, or no code worktree); a non-empty commit-forward prompt to hand
// the agent when either invariant fails; or a non-nil error when a check could not
// be run (fail-closed: the caller blocks completion).
func (r *Runner) verifyGate(ctx context.Context, sel *selectt.Selection, wtAbs, hiveAbs, branch string) (string, error) {
	if sel.Kind != selectt.Work || wtAbs == "" {
		return "", nil
	}
	// Only the TODO->NEEDS-REVIEW handoff is gated. A Work pass that escalates
	// (NEEDS-HUMAN) or flags a conflict (NEEDS-ARBITRATION) is a DIFFERENT handoff
	// the protocol does not target here, and blocking it would trap a legitimate
	// escalation; a direct-to-DONE Work pass is likewise out of scope. complete()
	// re-reads the plan, so re-read here to learn the specific status it just
	// accepted.
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
	// Uncommitted-work gate — the ONE protocol invariant this gate enforces, for
	// EVERY work submodule regardless of language. A code worktree still carrying
	// ANY change (modified OR untracked) at this handoff means the agent
	// edited/created files but never committed them to bee-<taskid>, so finish()
	// would merge an EMPTY branch and the task would land with none of its code.
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
	// Committed-doc gate — the change doc must already be COMMITTED in the hive
	// worktree HEAD (the ref publish merges to main), not merely present on disk.
	// Unlike PLAN.md (carried to main by the runner's own claim/heartbeat/release
	// commits), the doc is never runner-committed: an uncommitted doc publishes as a
	// NEEDS-REVIEW with no change doc. Prefix-match the docs tree exactly as
	// docPresent does, but against committed HEAD rather than the working tree.
	docDir := path.Join("submodules", sel.Submodule.Name, "docs")
	lt, err := r.runVerify(ctx, hiveAbs, "git", "ls-tree", "-r", "--name-only", "HEAD", "--", docDir)
	if err != nil {
		return "", fmt.Errorf("verify gate: listing committed docs in %s: %w", hiveAbs, err)
	}
	stem := branch + "-" + sel.Task.ID
	docPath := path.Join(docDir, stem+".md")
	if lt.exitErr {
		// No HEAD yet (nothing committed at all) => the doc cannot be committed.
		return docUncommittedFailPrompt(docPath), nil
	}
	committed := false
	for _, line := range strings.Split(strings.TrimSpace(lt.out), "\n") {
		if pathHasPrefix(path.Base(strings.TrimSpace(line)), stem) {
			committed = true
			break
		}
	}
	if !committed {
		return docUncommittedFailPrompt(docPath), nil
	}
	return "", nil
}

// gateVerifyOutputCap bounds how much command output rides back in the
// commit-forward prompt so a large porcelain listing cannot blow the turn's token
// budget. The TAIL is kept.
const gateVerifyOutputCap = 4000

// dirtyTreeFailPrompt renders the commit-forward continue prompt: the code
// worktree still has uncommitted changes at the completion handoff, so the task is
// NOT accepted as done (finish() would merge an empty branch and lose every edit).
// The porcelain listing is tail-capped so a large diff cannot blow the turn's
// token budget.
func dirtyTreeFailPrompt(out string) string {
	out = strings.TrimRight(out, "\n")
	if len(out) > gateVerifyOutputCap {
		out = "…(truncated; showing the tail)\n" + out[len(out)-gateVerifyOutputCap:]
	}
	return fmt.Sprintf(
		"Handoff gate FAILED: your code worktree still has UNCOMMITTED changes, so "+
			"the task is NOT accepted as done — the runner only ever merges commits that already "+
			"exist on your bee-<taskid> branch, so an uncommitted edit would be silently discarded "+
			"and the task would land with none of its code. Commit ALL of these to your "+
			"bee-<taskid> branch (with the `Beehive: <task-id> <doc-path>` stamp) and push it to "+
			"the submodule origin THIS session; if a listed file is scratch that must not ship, "+
			"delete it. Then the gate re-runs automatically (leave the task status as-is). "+
			"`git status --porcelain`:\n\n%s",
		out)
}

// docUncommittedFailPrompt renders the commit-forward prompt for a NEEDS-REVIEW
// handoff whose change doc is not committed in the hive worktree. The doc is the
// one completion artifact the runner never commits for the agent (PLAN.md rides
// to main on the claim/heartbeat/release commits; the doc does not), so an
// uncommitted doc would publish a doc-less NEEDS-REVIEW the reviewer must reject.
func docUncommittedFailPrompt(docPath string) string {
	return fmt.Sprintf(
		"Handoff gate FAILED: your change doc is NOT committed, so the task is NOT accepted "+
			"as done — the runner publishes only committed history, and unlike PLAN.md (which the "+
			"runner commits for you) NOTHING commits your change doc, so an uncommitted doc would "+
			"land this task NEEDS-REVIEW on main with NO change doc and the reviewer would reject it. "+
			"Write the change doc at EXACTLY %[1]s, then `git add %[1]s` and COMMIT it (with your "+
			"PLAN.md status flip) to the hive worktree THIS session. Then the gate re-runs "+
			"automatically (leave the task status as-is).",
		docPath)
}
