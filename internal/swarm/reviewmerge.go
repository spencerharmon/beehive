package swarm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

// reviewBranchFor names the throwaway local branch a Review/Arbitrate inspection
// worktree checks out. It is DISTINCT from the implementer's bee-<id> branch
// (branchFor) so materializing it never clobbers the worker's branch ref, and it
// is based at the tracked-branch tip: the agent diffs/tests/merges the implementer
// branch INTO it, and the runner fast-forwards the tracked branch to it on approve.
func reviewBranchFor(sel *selectt.Selection) string {
	return "bee-rev-" + sel.Task.ID
}

// setupReviewWorktree cuts the Review/Arbitrate inspection/merge worktree at the
// tracked-branch tip (on a throwaway bee-rev-<id> branch), fetching the
// implementer branch so the agent can diff/test/merge it. Returns the worktree's
// absolute path, a submodule git handle (for teardown), and ok=false when the
// submodule has no usable checkout (a degenerate/unreachable gitlink or a minimal
// test fixture) — in which case the caller proceeds without a worktree and
// finalizeReviewMerge no-ops. BEST-EFFORT: never returns an error.
func (r *Runner) setupReviewWorktree(ctx context.Context, sel *selectt.Selection, beeBranch, absRoot string) (wtAbs string, wg *git.Repo, ok bool) {
	repoDir := sel.Submodule.RepoDir()
	if !isSourceCheckout(ctx, repoDir) {
		rel, relErr := filepath.Rel(absRoot, repoDir)
		if relErr != nil {
			return "", nil, false
		}
		if _, err := r.Git.Run(ctx, "submodule", "update", "--init", "--", rel); err != nil {
			return "", nil, false
		}
	}
	if !isSourceCheckout(ctx, repoDir) {
		return "", nil, false
	}
	wg = git.New(repoDir)
	if err := r.syncWorktreeBase(ctx, wg, sel.Submodule, absRoot); err != nil {
		return "", nil, false
	}
	wtBranch := reviewBranchFor(sel)
	wtAbs = filepath.Join(sel.Submodule.WorktreesDir(), wtBranch)
	base := "HEAD"
	if rel, relErr := filepath.Rel(absRoot, repoDir); relErr == nil {
		if rem, rerr := wg.Remote(ctx); rerr == nil && rem != "" {
			tracked := r.trackedBranch(ctx, rel)
			if err := wg.Fetch(ctx, rem, tracked); err != nil {
				return "", nil, false
			}
			// A bee-<id> absent from origin (zero-code-diff work) is expected; the
			// completion guards handle it. Never fail the setup on it.
			_ = wg.Fetch(ctx, rem, beeBranch)
			base = rem + "/" + tracked
		}
	}
	if err := wg.WorktreeAdd(ctx, wtAbs, wtBranch, base); err != nil {
		return "", nil, false
	}
	return wtAbs, wg, true
}

// finalizeReviewMerge is the runner-owned submodule-side completion for a
// Review/Arbitrate pass that APPROVED the work (left the task at DONE). The
// honeybee owns only the beehive-layer status flip + doc; the runner owns the
// submodule commit and the gate. This performs, in the agent's inspection
// worktree (wtAbs, checked out at the tracked tip):
//
//  1. If the tracked branch advanced since the worktree was cut, merge it in.
//  2. If the implementer branch (bee-<id>) is not already merged (the agent MAY
//     have merged it proactively — the encouraged, no-extra-turn path), merge it.
//     Either merge that CONFLICTS is left in the worktree (case A): the runner
//     returns a fix-forward hint and a revertTo status so the caller reverts the
//     superrepo flip and hands the conflicted files back to the SAME agent to
//     resolve (`beehive submodule git add`/`commit`) without escalating.
//  3. Runs the task's `Check:` DoD against the MERGED tree, BEFORE any push. A
//     failure discards the pending merge (never reaches the tracked origin) and
//     returns a fix-forward hint + revertTo.
//  4. On success, pushes the merge to the tracked branch on the submodule origin
//     and stamps the real merge sha into the PLAN `commits=` tag + the change
//     doc's `<!-- Beehive-Commits: -->` header on the hive branch, so the gate's
//     commit invariants and finish()'s gitlink pin observe the merged tip.
//
// Returns ("", "", nil) for a non-approve flip (REJECT/rework — no merge), a
// non-Review/Arbitrate kind, or full success. A non-empty hint (with revertTo set)
// means "revert the flip and re-prompt"; a non-nil err is an infra failure
// (fail-closed, block completion).
func (r *Runner) finalizeReviewMerge(ctx context.Context, sel *selectt.Selection, wtAbs, absRoot string) (hint string, revertTo plan.Status, err error) {
	if sel.Kind != selectt.Review && sel.Kind != selectt.Arbitrate {
		return "", "", nil
	}
	// Only an APPROVE / side-with-implementer (on-disk DONE) merges. A REJECT
	// (NEEDS-ARBITRATION) or side-with-reviewer (TODO) writes no submodule code.
	// Read the CURRENT plan task (status + Check DoD) — the dispatch-time sel.Task
	// snapshot may lack the Check the reviewer must gate on.
	pb, perr := os.ReadFile(sel.Submodule.PlanPath())
	if perr != nil {
		return "", "", fmt.Errorf("review-merge: reading plan: %w", perr)
	}
	pp, perr := plan.Parse(string(pb))
	if perr != nil {
		return "", "", fmt.Errorf("review-merge: parsing plan: %w", perr)
	}
	cur := pp.Find(sel.Task.ID)
	if cur == nil {
		return "", "", nil // task vanished; the removed-guard owns it
	}
	if cur.Status != plan.StatusDone {
		return "", "", nil
	}
	revertTo = plan.StatusReview
	if sel.Kind == selectt.Arbitrate {
		revertTo = plan.StatusTODO
	}
	if wtAbs == "" {
		// No inspection worktree (degenerate/unreachable submodule, or a minimal test
		// fixture). Nothing to merge here; a real approve with reachable code always
		// has a worktree. The commits=none flip stands; completion guards own the rest.
		return "", "", nil
	}
	wtg := git.New(wtAbs)

	// An unresolved/mid-resolution merge left by a PRIOR conflict hand-back: the
	// worktree is dirty. Do not attempt more git plumbing on top of it — tell the
	// agent to finish resolving and commit, and revert the premature flip.
	if clean, cerr := wtg.Clean(ctx); cerr != nil {
		return "", "", fmt.Errorf("review-merge: checking worktree cleanliness: %w", cerr)
	} else if !clean {
		return unresolvedMergeHint(sel, wtAbs), revertTo, nil
	}

	// The change doc must exist before we merge/push: the handoff gate requires it,
	// and pushing an approve's merge for a doc-less flip would only be reverted. Gate
	// on it here so the tracked branch never advances for an incomplete handoff.
	if _, _, ok := changeDocPath(sel, absRoot); !ok {
		return docMissingReviewHint(sel), revertTo, nil
	}

	repoDir := sel.Submodule.RepoDir()
	rel, rerr := filepath.Rel(absRoot, repoDir)
	if rerr != nil {
		return "", "", fmt.Errorf("review-merge: resolving submodule path: %w", rerr)
	}
	tracked := r.trackedBranch(ctx, rel)
	beeBranch := branchFor(sel)
	rem, _ := wtg.Remote(ctx) // "" on a no-remote install / tests

	// Pre-merge HEAD, captured so a check failure can discard the merge on ANY path
	// (remote or local-sharing), never leaving a failed merge on the worktree branch.
	preHead, err := wtg.RevParse(ctx, "HEAD")
	if err != nil {
		return "", "", fmt.Errorf("review-merge: resolving pre-merge HEAD: %w", err)
	}

	trackedRef, beeRef := "HEAD", beeBranch
	if rem != "" {
		if ferr := wtg.Fetch(ctx, rem, tracked); ferr != nil {
			return "", "", fmt.Errorf("review-merge: fetching tracked branch %q: %w", tracked, ferr)
		}
		// The implementer branch may be ABSENT (a zero-code-diff approve never pushed
		// one) — that is not an error, there is simply nothing to merge. Distinguish an
		// absent ref (nothing to do) from a TRANSIENT fetch failure (fail-closed) via
		// ls-remote, so a network blip never silently no-op-approves real code.
		if refSha, lerr := wtg.LsRemoteBranch(ctx, rem, beeBranch); lerr != nil {
			return "", "", fmt.Errorf("review-merge: checking implementer branch %q on origin: %w", beeBranch, lerr)
		} else if refSha == "" {
			return "", "", nil // branch genuinely absent
		}
		if ferr := wtg.Fetch(ctx, rem, beeBranch); ferr != nil {
			return "", "", fmt.Errorf("review-merge: fetching implementer branch %q: %w", beeBranch, ferr)
		}
		trackedRef = rem + "/" + tracked
		beeRef = rem + "/" + beeBranch
	} else {
		// No-remote (local-sharing hive / tests): the implementer branch is a local
		// ref in the shared store. Absent -> nothing to merge (zero-diff approve).
		if _, err := wtg.RevParse(ctx, beeBranch); err != nil {
			return "", "", nil
		}
	}

	// (1) Fold in a tracked-branch tip that advanced since the worktree was cut, so
	// the eventual push is a fast-forward.
	if rem != "" {
		anc, aerr := wtg.IsAncestor(ctx, trackedRef, "HEAD")
		if aerr != nil {
			return "", "", fmt.Errorf("review-merge: ancestry check (tracked): %w", aerr)
		}
		if !anc {
			if merr := wtg.Merge(ctx, trackedRef); merr != nil {
				if errors.Is(merr, git.ErrConflict) {
					return conflictReviewHint(sel, wtAbs, tracked, "the tracked branch advanced and conflicts with the implementer's work"), revertTo, nil
				}
				return "", "", fmt.Errorf("review-merge: merging tracked branch: %w", merr)
			}
		}
	}

	// (2) Fold in the implementer branch unless the agent already merged it.
	anc, aerr := wtg.IsAncestor(ctx, beeRef, "HEAD")
	if aerr != nil {
		return "", "", fmt.Errorf("review-merge: ancestry check (implementer): %w", aerr)
	}
	if !anc {
		if merr := wtg.Merge(ctx, beeRef); merr != nil {
			if errors.Is(merr, git.ErrConflict) {
				// Case A: leave the conflict IN THE AGENT'S WORKTREE (do NOT abort) so
				// it resolves in-session.
				return conflictReviewHint(sel, wtAbs, beeBranch, "the implementer branch conflicts with the tracked branch"), revertTo, nil
			}
			return "", "", fmt.Errorf("review-merge: merging implementer branch: %w", merr)
		}
	}

	mergeSHA, err := wtg.RevParse(ctx, "HEAD")
	if err != nil {
		return "", "", fmt.Errorf("review-merge: resolving merge sha: %w", err)
	}

	// (3) DoD check on the MERGED tree, before any push.
	if check := cur.Check(); check != "" && !cur.CheckNone {
		o, cerr := r.runCheckIn(ctx, sel, check, wtAbs)
		if cerr != nil {
			var pv policyViolationError
			if errors.As(cerr, &pv) {
				_ = wtg.HardReset(ctx, preHead) // discard the merge
				return checkPolicyFailPrompt(sel.Task.ID, check, cerr), revertTo, nil
			}
			return "", "", fmt.Errorf("review-merge: running DoD check for %s: %w", sel.Task.ID, cerr)
		}
		if o.exitErr {
			// Discard the pending merge — it never reaches the tracked origin (on ANY
			// path: reset to the captured pre-merge HEAD).
			_ = wtg.HardReset(ctx, preHead)
			return checkFailPrompt(sel.Task.ID, check, o.out), revertTo, nil
		}
	}

	// (4) Push the merge to the tracked branch. HEAD contains the tracked tip we
	// fetched, so this is a fast-forward — UNLESS a peer advanced the tracked branch
	// since our fetch. On a rejected push, re-fold the new tip and retry ONCE; a
	// conflict on that re-fold hands back to the agent.
	if rem != "" {
		if perr := wtg.Push(ctx, rem, "HEAD:refs/heads/"+tracked); perr != nil {
			if ferr := wtg.Fetch(ctx, rem, tracked); ferr != nil {
				return "", "", fmt.Errorf("review-merge: re-fetching tracked branch after rejected push: %w", ferr)
			}
			if a, e := wtg.IsAncestor(ctx, trackedRef, "HEAD"); e == nil && !a {
				if merr := wtg.Merge(ctx, trackedRef); merr != nil {
					if errors.Is(merr, git.ErrConflict) {
						return conflictReviewHint(sel, wtAbs, tracked, "the tracked branch advanced concurrently and conflicts"), revertTo, nil
					}
					return "", "", fmt.Errorf("review-merge: re-merging advanced tracked branch: %w", merr)
				}
				if mergeSHA, err = wtg.RevParse(ctx, "HEAD"); err != nil {
					return "", "", fmt.Errorf("review-merge: resolving re-merged sha: %w", err)
				}
			}
			if perr2 := wtg.Push(ctx, rem, "HEAD:refs/heads/"+tracked); perr2 != nil {
				return "", "", fmt.Errorf("review-merge: pushing merge to tracked branch %q: %w", tracked, perr2)
			}
		}
	}

	// (5) Stamp the real merge sha into the PLAN commits= tag + doc header on the
	// hive branch, so the gate's commit invariants and finish()'s pin see it.
	if serr := r.stampReviewMerge(ctx, sel, absRoot, mergeSHA); serr != nil {
		return "", "", serr
	}
	return "", "", nil
}

// stampReviewMerge writes the runner-produced merge sha into the PLAN task's
// `commits=` tag and the change doc's first-line `<!-- Beehive-Commits: -->`
// header (single source: internal/plan helpers, shared with the CLI + gate), then
// commits both on the hive branch so finish() carries them to main.
func (r *Runner) stampReviewMerge(ctx context.Context, sel *selectt.Selection, absRoot, sha string) error {
	commits := []string{sha}

	planPath := sel.Submodule.PlanPath()
	pb, err := os.ReadFile(planPath)
	if err != nil {
		return fmt.Errorf("stamp merge: reading %s: %w", planPath, err)
	}
	p, err := plan.Parse(string(pb))
	if err != nil {
		return fmt.Errorf("stamp merge: parsing %s: %w", planPath, err)
	}
	t := p.Find(sel.Task.ID)
	if t == nil {
		return fmt.Errorf("stamp merge: task %q absent from %s", sel.Task.ID, planPath)
	}
	t.Commits = commits
	t.CommitsSet = true
	if err := os.WriteFile(planPath, []byte(p.String()), 0o644); err != nil {
		return fmt.Errorf("stamp merge: writing %s: %w", planPath, err)
	}

	docFSPath, docRel, ok := changeDocPath(sel, absRoot)
	if !ok {
		return fmt.Errorf("stamp merge: change doc for %s not found under submodules/%s/docs", sel.Task.ID, sel.Submodule.Name)
	}
	db, err := os.ReadFile(docFSPath)
	if err != nil {
		return fmt.Errorf("stamp merge: reading %s: %w", docFSPath, err)
	}
	if err := os.WriteFile(docFSPath, []byte(plan.SetDocCommitsHeader(string(db), commits)), 0o644); err != nil {
		return fmt.Errorf("stamp merge: writing %s: %w", docFSPath, err)
	}

	planRel, err := filepath.Rel(absRoot, planPath)
	if err != nil {
		return fmt.Errorf("stamp merge: resolving plan path: %w", err)
	}
	msg := fmt.Sprintf("plan: stamp %s merge %s (runner-owned review merge)\n\nBeehive: %s plan", sel.Task.ID, sha, sel.Task.ID)
	if err := r.Git.CommitPaths(ctx, msg, planRel, docRel); err != nil && !errors.Is(err, git.ErrNothing) {
		return fmt.Errorf("stamp merge: committing %s + %s: %w", planRel, docRel, err)
	}
	return nil
}

// changeDocPath discovers the on-disk change doc for a task (stem
// `<branch>-<taskid>`) under submodules/<sm>/docs, returning its absolute FS path
// and the hive-relative path used for committing.
func changeDocPath(sel *selectt.Selection, absRoot string) (fsPath, rel string, ok bool) {
	docDir := filepath.Join(sel.Submodule.Path, "docs")
	stem := branchFor(sel) + "-" + sel.Task.ID
	ents, err := os.ReadDir(docDir)
	if err != nil {
		return "", "", false
	}
	for _, e := range ents {
		if !e.IsDir() && pathHasPrefix(e.Name(), stem) {
			name := e.Name()
			return filepath.Join(docDir, name), path.Join("submodules", sel.Submodule.Name, "docs", name), true
		}
	}
	return "", "", false
}

// conflictReviewHint is the fix-forward prompt handed to a Review/Arbitrate agent
// after the runner's merge left conflicts in its inspection worktree.
func conflictReviewHint(sel *selectt.Selection, wtAbs, into, why string) string {
	return fmt.Sprintf(
		"Runner-owned merge left CONFLICTS in your inspection worktree ($SUBMODULE_WORKTREE = %[1]s): %[2]s. "+
			"Your DONE flip was reverted. Resolve the conflicts IN THAT WORKTREE with `beehive submodule git` "+
			"(edit the conflicted files, `beehive submodule git add <files>`, `beehive submodule git commit`), then "+
			"re-run the task's `Check:` and re-record the `<!-- Beehive-Check: -->` line, then flip DONE again with "+
			"`beehive task status %[3]s %[4]s DONE`. The runner then validates your resolved merge, runs the check, and "+
			"pushes it to the tracked branch — do NOT push the tracked branch or touch the gitlink yourself.",
		wtAbs, why, sel.Submodule.Name, sel.Task.ID)
}

// unresolvedMergeHint is handed to an agent that re-flipped DONE while a prior
// conflict hand-back is still unresolved (a dirty inspection worktree).
func unresolvedMergeHint(sel *selectt.Selection, wtAbs string) string {
	return fmt.Sprintf(
		"Your inspection worktree ($SUBMODULE_WORKTREE = %[1]s) still has an UNRESOLVED/uncommitted merge. Finish "+
			"resolving it (`beehive submodule git add <files>` + `beehive submodule git commit`) so the tree is clean "+
			"BEFORE flipping DONE. Then flip DONE again with `beehive task status %[2]s %[3]s DONE`.",
		wtAbs, sel.Submodule.Name, sel.Task.ID)
}

// docMissingReviewHint is handed to a Review/Arbitrate agent that approved (flipped
// DONE) without the change doc present — the gate requires it, so the runner
// refuses to merge/push until it exists.
func docMissingReviewHint(sel *selectt.Selection) string {
	return fmt.Sprintf(
		"You approved %[2]s but the change doc submodules/%[1]s/docs/bee-%[2]s-%[2]s.md is absent. The handoff gate "+
			"requires it (with your `<!-- Beehive-Check: -->` result for a task carrying a `Check:`). Write/commit it, "+
			"then flip DONE again with `beehive task status %[1]s %[2]s DONE`.",
		sel.Submodule.Name, sel.Task.ID)
}
