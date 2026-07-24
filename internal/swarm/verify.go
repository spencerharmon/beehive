package swarm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

// The handoff gate is the runner-owned check every task-bearing pass (Work,
// Review, Arbitrate) must pass BEFORE its terminal flip is accepted as complete.
// It is the uniform, kind-agnostic protocol gate — the same committed-artifact
// invariants apply to every terminal handoff — and it is a PROTOCOL check, NOT a
// code-correctness judgment (see docs/runner-protocol-vs-correctness.md): the
// RUNNER verifies adherence to the honeybee protocol (the submodule commits the
// agent made actually exist, the status flip and the change doc are COMMITTED so
// the runner can MERGE them to main, and the flip references only real commits)
// and NEVER judges whether the code is correct. CORRECTNESS is owned by the
// honeybees themselves (a work agent writes and runs its own regression test, a
// review agent re-verifies the evidence, an arbiter breaks ties) using the
// target's own tests/pipelines. The runner cannot know a submodule's toolchain and
// must not assume one; whether a worktree is clean, whether a ref is committed,
// and whether a commit exists are facts the runner CAN and MUST check in any
// language.
//
// Division of labor for the beehive superrepo (PLAN.md + docs): the AGENT commits
// its status flip and change doc to the hive branch; the RUNNER merges that branch
// to main. So the gate's job is to confirm the agent actually committed the
// artifacts the runner is about to merge — an uncommitted flip or doc would be
// silently dropped by publish (which carries only committed history), which is the
// exact bug that stranded gostream-image-build-verify's arbitration on the wall-
// deadline/GC exit path (2026-07-22) and the doc-less NEEDS-REVIEW thrash before
// it. Red => the caller does NOT complete: it keeps the claim and feeds the ONE
// failing requirement to the agent as the next prompt (commit forward, same
// session). Clean (or inapplicable) => the flip stands.

// verifyOutcome is the gate command's result as the gate consumes it: its combined
// output plus whether the command RAN and exited non-zero. A separate non-nil
// error from the runner means the command could not be run at all — an infra
// failure the caller handles fail-closed (block completion).
type verifyOutcome struct {
	out     string
	exitErr bool
}

// runVerify dispatches the gate command through the injectable seam. A nil
// RunVerify uses realRunVerify (real exec); tests set RunVerify to force
// deterministic outcomes and to assert the exact invocations.
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

// repoPlanFile is the PLAN.md filename (mirrors repo.PlanFile; kept local to this
// file's small surface).
const repoPlanFile = "PLAN.md"

// verifyGate runs the handoff PROTOCOL gate for a task-bearing pass (Work,
// Review, Arbitrate) that has just driven its task to a terminal handoff status —
// Work->{NEEDS-REVIEW,NEEDS-ARBITRATION,DONE}, Review->{DONE,NEEDS-ARBITRATION},
// Arbitrate->{DONE,TODO}. NEEDS-HUMAN escalations are never gated (a legitimate
// escalation must not be trapped). Every invariant is a language-agnostic PROTOCOL
// fact, never a correctness judgment (docs/runner-protocol-vs-correctness.md):
//
//  1. the submodule CHECKOUT the pass touched (the code worktree for Work; the
//     submodules/<sm>/repo checkout for Review/Arbitrate) carries NO uncommitted
//     work — an uncommitted edit would be silently dropped by the merge/publish;
//  2. the task's STATUS FLIP is COMMITTED in the hive HEAD (not merely on-disk) —
//     an on-disk-only flip is lost on any exit path that publishes committed
//     history without a runner claim/release commit (the wall-deadline/GC exit
//     that stranded gostream-image-build-verify's arbitration, 2026-07-22);
//  3. the change DOC (submodules/<sm>/docs/bee-<taskid>-<taskid>.md) is COMMITTED
//     in HEAD;
//  4. the task carries a `commits=` tag (the session's submodule commits, or
//     `commits=none`) in the COMMITTED plan, the doc's `<!-- Beehive-Commits: -->`
//     header names the SAME set, and every referenced commit is REACHABLE ON THE
//     SUBMODULE ORIGIN (pushed, not merely present in this pass's ephemeral local
//     object store) — so a flip can never reference a phantom/bad-object commit (the
//     be7e394/d4fdf97 stamps, 2026-07-22) NOR a local-only commit that dies on
//     worktree teardown (chat-editor-fullwidth-panel-layout, 2026-07-22). A
//     remote-less local-sharing hive has no origin to push to and falls back to
//     local existence, which is durable there.
//
// It returns "" when every invariant holds or the gate is INAPPLICABLE (a non-task
// kind, or a status that is not a gated terminal handoff); a non-empty commit-
// forward prompt naming the ONE unmet requirement to hand the agent; or a non-nil
// error when a check could not be run (fail-closed: the caller blocks completion).
func (r *Runner) verifyGate(ctx context.Context, sel *selectt.Selection, wtAbs, hiveAbs, branch string) (string, error) {
	if sel.Kind != selectt.Work && sel.Kind != selectt.Review && sel.Kind != selectt.Arbitrate {
		return "", nil
	}
	// complete() re-reads the plan; re-read here to learn the specific terminal
	// status it just accepted and confirm this is a gated handoff.
	b, err := os.ReadFile(sel.Submodule.PlanPath())
	if err != nil {
		return "", err
	}
	p, err := plan.Parse(string(b))
	if err != nil {
		return "", err
	}
	t := p.Find(sel.Task.ID)
	if t == nil || !gatedHandoff(sel.Kind, t.Status) {
		return "", nil
	}

	// The submodule checkout this pass may have committed into: the code worktree
	// for Work, the submodules/<sm>/repo checkout for Review/Arbitrate (which merge
	// a bee-branch into the tracked branch in place). Both share the module object
	// store, so a commit made in either is visible to `cat-file` below. Normalize to
	// an absolute path (Submodule.Path may be relative or absolute across callers).
	checkoutDir := wtAbs
	if sel.Kind != selectt.Work || checkoutDir == "" {
		checkoutDir = sel.Submodule.RepoDir()
	}
	if !filepath.IsAbs(checkoutDir) {
		checkoutDir = filepath.Join(hiveAbs, checkoutDir)
	}
	checkoutExists := true
	if fi, statErr := os.Stat(checkoutDir); statErr != nil || !fi.IsDir() {
		checkoutExists = false
	}

	// (1) Uncommitted-work gate. Skipped only when the checkout does not exist at
	// all (a pass that touched no submodule code — reachability below still holds
	// any referenced commit to account, so a `commits=none` handoff is consistent).
	if checkoutExists {
		if hint, err := r.gateCleanCheckout(ctx, checkoutDir); err != nil || hint != "" {
			return hint, err
		}
	}

	// (2) Committed-flip gate. The status flip must be in the hive HEAD, not just
	// on disk: the runner merges only committed history, and an abnormal exit
	// (wall-deadline/GC) never runs the claim release that would otherwise carry an
	// on-disk-only flip to main. Parse the COMMITTED PLAN.md.
	planHEAD := "HEAD:" + path.Join("submodules", sel.Submodule.Name, repoPlanFile)
	show, err := r.runVerify(ctx, hiveAbs, "git", "show", planHEAD)
	if err != nil {
		return "", fmt.Errorf("verify gate: reading committed PLAN.md in %s: %w", hiveAbs, err)
	}
	if show.exitErr {
		return planFlipUncommittedFailPrompt(sel.Task.ID, t.Status), nil
	}
	cp, err := plan.Parse(show.out)
	if err != nil {
		return "", fmt.Errorf("verify gate: parsing committed PLAN.md: %w", err)
	}
	ct := cp.Find(sel.Task.ID)
	if ct == nil || ct.Status != t.Status {
		return planFlipUncommittedFailPrompt(sel.Task.ID, t.Status), nil
	}

	// (3) Committed-doc gate — the change doc must be COMMITTED in the hive HEAD.
	docDir := path.Join("submodules", sel.Submodule.Name, "docs")
	stem := branch + "-" + sel.Task.ID
	docPath := path.Join(docDir, stem+".md")
	lt, err := r.runVerify(ctx, hiveAbs, "git", "ls-tree", "-r", "--name-only", "HEAD", "--", docDir)
	if err != nil {
		return "", fmt.Errorf("verify gate: listing committed docs in %s: %w", hiveAbs, err)
	}
	if lt.exitErr {
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

	// (4) commits= tag: present in the committed plan, echoed by the doc header,
	// agreeing, and every referenced commit reachable in the submodule.
	if !ct.CommitsSet {
		return commitsTagMissingFailPrompt(sel.Task.ID, docPath), nil
	}
	docShow, err := r.runVerify(ctx, hiveAbs, "git", "show", "HEAD:"+docPath)
	if err != nil {
		return "", fmt.Errorf("verify gate: reading committed doc %s: %w", docPath, err)
	}
	if docShow.exitErr {
		return docUncommittedFailPrompt(docPath), nil
	}
	docShas, docSet := parseDocCommits(docShow.out)
	if !docSet {
		return docCommitsHeaderFailPrompt(docPath, ct.Commits), nil
	}
	if !sameCommitSet(ct.Commits, docShas) {
		return commitsMismatchFailPrompt(docPath, ct.Commits, docShas), nil
	}
	// Durability: every referenced sha must be reachable on the submodule ORIGIN,
	// not merely in this pass's ephemeral local object store. The agent commits on
	// bee-<taskid> and PUSHES it to origin; at gate time the local code worktree
	// still holds the object, so a bare local `cat-file -e` PASSES even for a commit
	// that was never pushed — which then vanishes on worktree teardown + gc, leaving
	// a bad-object pointer that strands every later review/arbitration (observed
	// live: chat-editor-fullwidth-panel-layout flipped NEEDS-REVIEW with a local-only
	// commit, went bad-object, then burned review→arbitration→NEEDS-HUMAN, 2026-07-22).
	// git.Repo.RemoteContainsCommit fetches remote/<branch> and asks
	// `merge-base --is-ancestor sha remote/<branch>`, so an unpushed local commit is
	// REFUSED with the fix-forward "push bee-<taskid> to origin" prompt. A remote-less
	// local-sharing hive (rem=="") falls back to local existence, which IS durable
	// there (one shared object database). `commits=none` (len 0) trivially passes; the
	// clean-checkout gate above already guards an undeclared uncommitted change.
	if len(ct.Commits) > 0 {
		if !checkoutExists {
			return commitsUnreachableFailPrompt(sel.Submodule.Name, ct.Commits[0], ct.Commits), nil
		}
		sg := git.New(checkoutDir)
		rem, err := sg.Remote(ctx)
		if err != nil {
			return "", fmt.Errorf("verify gate: resolving submodule %s remote in %s: %w", sel.Submodule.Name, checkoutDir, err)
		}
		for _, sha := range ct.Commits {
			ok, err := sg.RemoteContainsCommit(ctx, rem, branch, sha)
			if err != nil {
				return "", fmt.Errorf("verify gate: checking commit %s durability on %s/%s: %w", sha, rem, branch, err)
			}
			if !ok {
				return commitsUnreachableFailPrompt(sel.Submodule.Name, sha, ct.Commits), nil
			}
		}
	}

	// (5) Definition-of-done check. When this handoff ENTERS DONE and the task
	// declares a `Check:` command, that command IS the machine definition of done:
	// run it and REFUSE the DONE unless it passes (exit 0). This gates the DONE
	// *state* regardless of writer (review approve, arbitration, an interrupted-
	// review finalize) — the enforcement the jellyfin false-DONE lacked
	// (docs/dod-verification-spec.md). A `check=none` task declared no machine-
	// checkable DoD (the absence is honest and review-scrutinized) and is not gated
	// here; an UNDECLARED check is left to `beehive plan lint` (migration-safe: we
	// gate on a check IF PRESENT, never retro-block a legacy DONE). An infra failure
	// to RUN the check is fail-closed (block completion).
	if t.Status == plan.Done && t.Check() != "" {
		if hint, err := r.checkGate(ctx, sel, t.ID, t.Check(), hiveAbs); err != nil || hint != "" {
			return hint, err
		}
	}

	// (6) Review-ran-check. A REVIEW that approves a task carrying a `Check:` must
	// have actually RUN that check and RECORDED its live result in the change doc
	// (a `<!-- Beehive-Check: … -->` line) — the reviewer's INDEPENDENT confirmation
	// that the definition of done holds, distinct from the runner's own gate above.
	// This closes the "approved a check they never executed" gap. Scoped to Review
	// (arbitration and successor-check finalizes are different roles) and only when a
	// real check is present (a `check=none` task declared no machine check to run).
	if sel.Kind == selectt.Review && t.Status == plan.Done && t.Check() != "" && !t.CheckNone {
		if !docRecordsCheck(docShow.out) {
			return reviewCheckUnrecordedFailPrompt(docPath, t.ID), nil
		}
	}
	return "", nil
}

// docRecordsCheck reports whether a change doc carries a non-empty `Beehive-Check:`
// marker — the reviewer's recorded live definition-of-done result (mirrors the
// `Beehive-Commits:` header convention).
func docRecordsCheck(doc string) bool {
	for _, line := range strings.Split(doc, "\n") {
		_, rest, ok := strings.Cut(line, "Beehive-Check:")
		if !ok {
			continue
		}
		rest, _, _ = strings.Cut(rest, "-->")
		if strings.TrimSpace(rest) != "" {
			return true
		}
	}
	return false
}

// reviewCheckUnrecordedFailPrompt tells a reviewer to actually run the task's check
// and record its live result before approving DONE.
func reviewCheckUnrecordedFailPrompt(docPath, taskID string) string {
	return fmt.Sprintf(
		"Handoff gate FAILED: this review approved %[1]s to DONE, but the change doc %[2]s records NO live "+
			"definition-of-done result. A reviewer must EXECUTE the task's `Check:` (run `beehive task check "+
			"<submodule> %[1]s`) and RECORD what it returned in the doc as a `<!-- Beehive-Check: pass — "+
			"<one-line evidence, e.g. curl … 200 / rollout complete> -->` line — do NOT approve a check you "+
			"never ran. Add that line, commit the doc, and leave the status DONE; the gate re-runs.",
		taskID, docPath)
}

// checkGate runs a task's definition-of-done command (its `Check:` body field) as
// the precondition for entering DONE. The command is a shell string (possibly
// multi-line), run via `sh -c` in the hive root through the same injectable
// runVerify seam the rest of the gate uses (tests force outcomes). Exit 0 => the
// DoD is met and DONE stands (""). Non-zero => the DoD is NOT met: refuse the
// handoff with a commit-forward prompt carrying the check's output tail (same
// mechanism as the other gate invariants — the caller keeps the claim and re-runs
// the gate). A failure to RUN the command at all is an infra error (fail-closed).
func (r *Runner) checkGate(ctx context.Context, sel *selectt.Selection, taskID, check, hiveAbs string) (string, error) {
	o, err := r.runCheck(ctx, sel, check, hiveAbs)
	if err != nil {
		var pv policyViolationError
		if errors.As(err, &pv) {
			// The check itself violates the command allowlist (an author defect, not an
			// infra failure): hand it back as a fix-forward prompt so the agent rewrites
			// the check, rather than fail-closed looping the task through GC.
			return checkPolicyFailPrompt(taskID, check, err), nil
		}
		return "", fmt.Errorf("verify gate: running DoD check for %s: %w", taskID, err)
	}
	if o.exitErr {
		return checkFailPrompt(taskID, check, o.out), nil
	}
	return "", nil
}

// checkPolicyFailPrompt renders the fix-forward prompt for a check that violates
// the command-allowlist policy (checkpolicy.Validate). The task cannot be DONE
// until the check is rewritten with allowlisted, low-risk tools (or the operator
// widens check_allowed_commands).
func checkPolicyFailPrompt(taskID, check string, err error) string {
	return fmt.Sprintf(
		"Handoff gate FAILED: %[1]s's definition-of-done check is REJECTED by the check-command policy "+
			"before it can run — %[2]v. A `Check:` must use low-risk, read-only/inspection tools scoped to "+
			"this submodule (and its linked submodules); it may not invoke a shell/interpreter to smuggle code "+
			"or reach outside its lane. Rewrite the check with allowlisted commands (or, if a tool is genuinely "+
			"needed, ask the operator to add it to check_allowed_commands). `Check:` was `%[3]s`.",
		taskID, err, oneLineCheck(check))
}

// checkFailPrompt renders the commit-forward prompt for a task whose definition-
// of-done check did not pass at the DONE handoff. The check output is tail-capped
// so a verbose command cannot blow the turn's token budget.
func checkFailPrompt(taskID, check, out string) string {
	out = strings.TrimRight(out, "\n")
	if len(out) > gateVerifyOutputCap {
		out = "…(truncated; showing the tail)\n" + out[len(out)-gateVerifyOutputCap:]
	}
	return fmt.Sprintf(
		"Handoff gate FAILED: %[1]s's definition-of-done check did NOT pass, so the task is NOT "+
			"accepted as done — the `Check:` command IS the machine definition of done and it exited "+
			"non-zero. Do NOT mark this DONE on a plausible-looking diff: either finish the work so the "+
			"check passes, or (if the effect only exists after this change is MERGED) leave the task "+
			"NEEDS-REVIEW and carry the live-effect check on a `Verify-After-Merge:` successor instead of "+
			"forcing DONE now. Then the gate re-runs automatically (leave the status as-is). `Check:` was "+
			"`%[2]s`; its output:\n\n%[3]s",
		taskID, oneLineCheck(check), out)
}

// oneLineCheck flattens a multi-line check command to a single line for prompt
// echo (the full command is already in the task's PLAN.md body).
func oneLineCheck(s string) string { return strings.Join(strings.Fields(s), " ") }

// gatedHandoff reports whether (kind, status) is a terminal handoff the uniform
// gate covers. NEEDS-HUMAN is deliberately excluded for every kind: an escalation
// carries its own reason and must never be trapped by the artifact gate. Work is
// NOT listed for Done: a work pass may never set DONE (workChecklist refuses it),
// so DONE is reached only via Review/Arbitrate, which carry the check gate below.
func gatedHandoff(kind selectt.Kind, st plan.Status) bool {
	switch kind {
	case selectt.Work:
		return st == plan.NeedsReview || st == plan.NeedsArb
	case selectt.Review:
		return st == plan.Done || st == plan.NeedsArb
	case selectt.Arbitrate:
		return st == plan.Done || st == plan.TODO
	}
	return false
}

// gateCleanCheckout runs the uncommitted-work check in dir. Returns a non-empty
// commit-forward prompt if the tree is dirty, "" if clean, or an error if the
// check could not run (fail-closed).
func (r *Runner) gateCleanCheckout(ctx context.Context, dir string) (string, error) {
	o, err := r.runVerify(ctx, dir, "git", "status", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("verify gate: running `git status --porcelain` in %s: %w", dir, err)
	}
	if o.exitErr {
		return "", fmt.Errorf("verify gate: `git status --porcelain` failed in %s: %s", dir, strings.TrimSpace(o.out))
	}
	if strings.TrimSpace(o.out) != "" {
		return dirtyTreeFailPrompt(o.out), nil
	}
	return "", nil
}

// parseDocCommits extracts the `<!-- Beehive-Commits: <sha>,<sha> | none -->`
// header from a change doc. Returns the sha list (empty for `none`) and whether
// a well-formed header was found at all.
func parseDocCommits(doc string) ([]string, bool) {
	for _, line := range strings.Split(doc, "\n") {
		line = strings.TrimSpace(line)
		_, rest, ok := strings.Cut(line, "Beehive-Commits:")
		if !ok {
			continue
		}
		rest, _, ok = strings.Cut(rest, "-->")
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		if rest == "" || rest == "none" {
			return nil, true
		}
		var shas []string
		for _, s := range strings.Split(rest, ",") {
			if s = strings.TrimSpace(s); s != "" {
				shas = append(shas, s)
			}
		}
		return shas, true
	}
	return nil, false
}

// sameCommitSet reports whether a and b hold the same set of commit shas
// (order-insensitive).
func sameCommitSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

// gateVerifyOutputCap bounds how much command output rides back in the
// commit-forward prompt so a large porcelain listing cannot blow the turn's token
// budget. The TAIL is kept.
const gateVerifyOutputCap = 4000

// dirtyTreeFailPrompt renders the commit-forward continue prompt: the submodule
// checkout still has uncommitted changes at the completion handoff, so the task is
// NOT accepted as done (the merge would carry none of the edits). The porcelain
// listing is tail-capped so a large diff cannot blow the turn's token budget.
func dirtyTreeFailPrompt(out string) string {
	out = strings.TrimRight(out, "\n")
	if len(out) > gateVerifyOutputCap {
		out = "…(truncated; showing the tail)\n" + out[len(out)-gateVerifyOutputCap:]
	}
	return fmt.Sprintf(
		"Handoff gate FAILED: your submodule checkout still has UNCOMMITTED changes, so "+
			"the task is NOT accepted as done — the runner only ever merges commits that already "+
			"exist, so an uncommitted edit would be silently discarded and the task would land "+
			"with none of its code. Commit ALL of these to the submodule THIS session (and push "+
			"the bee-<taskid> branch to the submodule origin), then RECORD each resulting commit "+
			"sha in BOTH your PLAN.md `commits=` tag and the doc's `Beehive-Commits` header; if a "+
			"listed file is scratch that must not ship, delete it. Then the gate re-runs "+
			"automatically (leave the task status as-is). `git status --porcelain`:\n\n%s",
		out)
}

// docUncommittedFailPrompt renders the commit-forward prompt for a terminal
// handoff whose change doc is not committed in the hive HEAD. The runner merges
// only committed history, so an uncommitted doc would land the flip on main with
// NO change doc.
func docUncommittedFailPrompt(docPath string) string {
	return fmt.Sprintf(
		"Handoff gate FAILED: your change doc is NOT committed, so the task is NOT accepted "+
			"as done — the runner merges only committed history, so an uncommitted doc would land "+
			"this flip on main with NO change doc and the reviewer would reject it. Write the change "+
			"doc at EXACTLY %[1]s (its FIRST line a `<!-- Beehive-Commits: <sha>,<sha> -->` header, "+
			"or `<!-- Beehive-Commits: none -->` if this session made no submodule commit), then "+
			"`git add %[1]s` and COMMIT it (with your PLAN.md status flip) to the hive branch THIS "+
			"session. Then the gate re-runs automatically (leave the task status as-is).",
		docPath)
}

// planFlipUncommittedFailPrompt renders the commit-forward prompt for a terminal
// flip that is present on disk but NOT committed in the hive HEAD. Without a
// committed flip, an abnormal exit (wall-deadline/GC) merges committed history and
// discards the on-disk-only flip — the decision is silently lost.
func planFlipUncommittedFailPrompt(taskID string, st plan.Status) string {
	return fmt.Sprintf(
		"Handoff gate FAILED: your PLAN.md status flip to %[2]s for %[1]s is on disk but NOT "+
			"committed, so the task is NOT accepted as done — the runner merges only committed "+
			"history, and if this pass ends on a wall-clock/GC boundary an uncommitted flip is "+
			"silently discarded and your decision is lost. `git add` PLAN.md and COMMIT the status "+
			"flip (together with your change doc) to the hive branch THIS session. Then the gate "+
			"re-runs automatically (leave the task status as-is).",
		taskID, st)
}

// commitsTagMissingFailPrompt renders the commit-forward prompt for a committed
// flip that carries no `commits=` tag at all.
func commitsTagMissingFailPrompt(taskID, docPath string) string {
	return fmt.Sprintf(
		"Handoff gate FAILED: %[1]s's committed PLAN.md task carries no `commits=` tag, so the "+
			"task is NOT accepted as done — every terminal flip must record the submodule commits "+
			"this session produced. Add `commits=<sha>[,<sha>...]` to the task's header comment (or "+
			"`commits=none` if this session made no submodule commit), mirror the SAME set in the "+
			"doc's first-line `<!-- Beehive-Commits: ... -->` header at %[2]s, and COMMIT both to the "+
			"hive branch THIS session. Then the gate re-runs automatically (leave the status as-is).",
		taskID, docPath)
}

// docCommitsHeaderFailPrompt renders the commit-forward prompt for a doc missing
// its Beehive-Commits header.
func docCommitsHeaderFailPrompt(docPath string, commits []string) string {
	return fmt.Sprintf(
		"Handoff gate FAILED: the committed change doc %[1]s has no `<!-- Beehive-Commits: ... -->` "+
			"header, so the task is NOT accepted as done — the doc and PLAN.md must BOTH reference "+
			"the session's submodule commits. Add the header as the doc's first line naming exactly "+
			"the same set as the PLAN.md `commits=` tag (%[2]s), then COMMIT it THIS session. Then "+
			"the gate re-runs automatically (leave the status as-is).",
		docPath, commitsTagValue(commits))
}

// commitsMismatchFailPrompt renders the commit-forward prompt for a plan/doc
// commit-set disagreement.
func commitsMismatchFailPrompt(docPath string, plan, doc []string) string {
	return fmt.Sprintf(
		"Handoff gate FAILED: the PLAN.md `commits=` tag (%[2]s) and the doc's Beehive-Commits "+
			"header (%[3]s) at %[1]s name DIFFERENT commit sets, so the task is NOT accepted as done. "+
			"Make them reference exactly the same submodule commits and COMMIT both THIS session. "+
			"Then the gate re-runs automatically (leave the status as-is).",
		docPath, commitsTagValue(plan), commitsTagValue(doc))
}

// commitsUnreachableFailPrompt renders the commit-forward prompt for a referenced
// commit that does not exist in the submodule — the phantom/bad-object stamp bug.
func commitsUnreachableFailPrompt(sm, sha string, commits []string) string {
	return fmt.Sprintf(
		"Handoff gate FAILED: commit %[2]s referenced by this task (commits=%[3]s) does NOT exist "+
			"in submodule %[1]s — a flip may never reference a phantom/bad-object commit. Either "+
			"CREATE the commit (commit your work to the submodule and push the bee-<taskid> branch to "+
			"its origin THIS session) or UPDATE the `commits=` tag and the doc's Beehive-Commits "+
			"header to the REAL commit sha(s), then COMMIT the corrected PLAN.md + doc to the hive "+
			"branch. Then the gate re-runs automatically (leave the status as-is).",
		sm, sha, commitsTagValue(commits))
}

// commitsTagValue renders a sha list the way the `commits=` tag serializes it (or
// "none" for an empty set), for use in gate prompts.
func commitsTagValue(commits []string) string {
	if len(commits) == 0 {
		return "none"
	}
	return strings.Join(commits, ",")
}

// checkGroundTruth runs the selected task's definition-of-done check ONCE at pass
// start and renders the result as a brief section, so a task-bearing agent starts
// from reality (does the effect already hold? is it still broken?) instead of
// re-deriving state by hand. It is the SAME command (`Check:`) and the SAME
// execution surface the handoff gate runs on entry to DONE (checkGate) — running
// it here adds no new environment coupling beyond the gate that already exists.
// Returns "" when the task carries no check (so the injected brief stays
// byte-identical to the historical path for every check-less task) or when the
// check cannot be run (best-effort; the gate, not this hint, is the enforcement).
func (r *Runner) checkGroundTruth(ctx context.Context, sel *selectt.Selection, hiveAbs string) string {
	if !hasTask(sel) {
		return ""
	}
	check := sel.Task.Check()
	if check == "" {
		return ""
	}
	o, err := r.runCheck(ctx, sel, check, hiveAbs)
	if err != nil {
		var pv policyViolationError
		if errors.As(err, &pv) {
			// The check violates the command-allowlist policy: it will be REFUSED at the
			// DONE gate. Say so now so the agent fixes the check early instead of
			// discovering it only at handoff.
			return fmt.Sprintf(
				"## Ground truth (definition-of-done check)\n"+
					"This task's `Check:` is REJECTED by the check-command policy (%v) and will be refused at "+
					"the DONE gate. Rewrite it with allowlisted low-risk tools scoped to this submodule before "+
					"handing off. `Check:` was `%s`.\n\n",
				pv.err, oneLineCheck(check))
		}
		// Could not even run the check (infra). Do not fabricate a result; the gate
		// enforces, this is only a hint. Tell the agent it is unknown.
		return fmt.Sprintf(
			"## Ground truth (definition-of-done check)\n"+
				"The runner tried to run this task's `Check:` at pass start but could not (%v). Treat the "+
				"definition of done as UNKNOWN and establish it yourself. `Check:` was `%s`.\n\n",
			err, oneLineCheck(check))
	}
	out := strings.TrimRight(o.out, "\n")
	if len(out) > gateVerifyOutputCap {
		out = "…(truncated; showing the tail)\n" + out[len(out)-gateVerifyOutputCap:]
	}
	status, guidance := "PASSED (exit 0)", "The definition of done ALREADY holds. Do not redo settled work — "+
		"confirm your change keeps it holding (and does not regress it); this same check gates the task into DONE."
	if o.exitErr {
		status, guidance = "FAILED (non-zero exit)", "The definition of done is NOT yet met — this is your target. "+
			"When your work makes this same check pass, the task is done; the runner re-runs it to gate DONE."
	}
	return fmt.Sprintf(
		"## Ground truth (definition-of-done check, run by the runner at pass start)\n"+
			"%[1]s %[2]s `Check:` was `%[3]s`; output:\n\n%[4]s\n\n",
		status, guidance, oneLineCheck(check), out)
}
