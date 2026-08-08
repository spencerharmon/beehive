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
	"github.com/spencerharmon/beehive/internal/guards"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

// guardGate enforces the submodule's diff-scoped MUTATION GUARDS (internal/guards)
// at a gated handoff. Where the DoD check gates whether the work is done, a guard
// gates whether the CHANGE is allowed to land at all — judged against live release
// state the runner itself never models (which blue/green color is active, whether a
// canary step is mid-flight, ...). The runner stays strategy-agnostic: it computes
// the committed diff of the pass branch vs the merge-base it forked from, and for
// every guard whose Protects glob intersects a changed file, runs that guard's
// command and enforces its exit code (0 = allow, non-zero = refuse + fix-forward).
//
// TAMPER ANCHOR: both GUARDS.md and the guard command are materialized from the
// MERGE-BASE (the reviewed baseline the pass forked from), never the bee branch. A
// bee's edit to guard code is therefore inert for its OWN pass — a changed/weakened
// guard governs only later passes, after it lands and is reviewed. You never grade
// your own exam with a key you edited mid-exam.
//
// Returns "" when every triggered guard allows (or no guard is triggered, or the
// baseline declares none — zero overhead); a fix-forward prompt when a guard
// refuses or the registry is malformed; a non-nil error on an infra failure the
// caller fails closed on.
func (r *Runner) guardGate(ctx context.Context, sel *selectt.Selection, checkoutDir, hiveAbs, branch string) (string, error) {
	// Resolve the baseline the pass forked from. Prefer the remote tracking branch
	// (origin/<branch>) so the anchor is the last PUBLISHED tip, not a local ref a
	// bee could move; fall back to the local branch name.
	rem := ""
	if g := git.New(checkoutDir); g != nil {
		if rr, err := g.Remote(ctx); err == nil {
			rem = strings.TrimSpace(rr)
		}
	}
	baseRef := branch
	if rem != "" {
		baseRef = rem + "/" + branch
	}
	base := baseRef
	if mb, err := r.runVerify(ctx, checkoutDir, "git", "merge-base", "HEAD", baseRef); err == nil && !mb.exitErr {
		if s := strings.TrimSpace(mb.out); s != "" {
			base = s
		}
	}

	// Baseline GUARDS.md. Absent at baseline => no guards => allow (zero overhead).
	gs, err := r.runVerify(ctx, checkoutDir, "git", "show", base+":"+guards.GuardsFile)
	if err != nil {
		return "", fmt.Errorf("guard gate: reading baseline %s in %s: %w", guards.GuardsFile, checkoutDir, err)
	}
	if gs.exitErr {
		return "", nil
	}
	reg, perr := guards.Parse(gs.out)
	if perr != nil {
		return guardRegistryParseFailPrompt(sel.Submodule.Name, perr), nil
	}
	if len(reg.Stubs) == 0 {
		return "", nil
	}

	// Committed diff base..HEAD (changed file paths, repo-relative).
	df, err := r.runVerify(ctx, checkoutDir, "git", "diff", "--name-only", base, "HEAD")
	if err != nil {
		return "", fmt.Errorf("guard gate: diffing %s..HEAD in %s: %w", base, checkoutDir, err)
	}
	if df.exitErr {
		return "", fmt.Errorf("guard gate: `git diff --name-only %s HEAD` failed in %s: %s", base, checkoutDir, strings.TrimSpace(df.out))
	}
	changed := splitNonEmptyLines(df.out)
	triggered := reg.Triggered(changed)
	if len(triggered) == 0 {
		return "", nil
	}

	// Materialize the BASELINE guard code (guards/ + GUARDS.md) into a throwaway
	// workspace so a bee's in-pass edit to guard code cannot influence the verdict.
	ws, err := os.MkdirTemp("", "beehive-guard-")
	if err != nil {
		return "", fmt.Errorf("guard gate: workspace: %w", err)
	}
	defer os.RemoveAll(ws)
	if err := r.materializeBaselineGuards(ctx, checkoutDir, base, ws); err != nil {
		return "", fmt.Errorf("guard gate: materializing baseline guard code: %w", err)
	}
	// Full patch, for guards that inspect hunks (written into the workspace).
	patchPath := filepath.Join(ws, ".beehive-guard-patch")
	if pd, err := r.runVerify(ctx, checkoutDir, "git", "diff", base, "HEAD"); err == nil && !pd.exitErr {
		_ = os.WriteFile(patchPath, []byte(pd.out), 0o644)
	}

	for i := range triggered {
		st := triggered[i]
		hint, err := r.runGuard(ctx, sel, &st, changed, ws, patchPath, hiveAbs)
		if err != nil {
			return "", err
		}
		if hint != "" {
			return hint, nil
		}
	}
	return "", nil
}

// materializeBaselineGuards extracts every file under guards/ plus GUARDS.md AS OF
// the baseline commit into dst, preserving the executable bit, so the guard runs
// from trusted (reviewed) code regardless of what the bee branch did to those files.
func (r *Runner) materializeBaselineGuards(ctx context.Context, checkoutDir, base, dst string) error {
	lt, err := r.runVerify(ctx, checkoutDir, "git", "ls-tree", "-r", base, "--", "guards", guards.GuardsFile)
	if err != nil {
		return err
	}
	if lt.exitErr {
		// Nothing to materialize (a guard whose command lives elsewhere would fail to
		// run below and be reported); not an error here.
		return nil
	}
	for _, line := range splitNonEmptyLines(lt.out) {
		// Format: "<mode> <type> <sha>\t<path>"
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			continue
		}
		meta := strings.Fields(line[:tab])
		relPath := line[tab+1:]
		if len(meta) < 3 || meta[1] != "blob" {
			continue
		}
		mode := meta[0]
		content, err := r.runVerify(ctx, checkoutDir, "git", "show", base+":"+relPath)
		if err != nil {
			return err
		}
		if content.exitErr {
			continue
		}
		out := filepath.Join(dst, filepath.FromSlash(relPath))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		perm := os.FileMode(0o644)
		if strings.HasSuffix(mode, "755") {
			perm = 0o755
		}
		if err := os.WriteFile(out, []byte(content.out), perm); err != nil {
			return err
		}
	}
	return nil
}

// runGuard runs one triggered guard's command from the baseline workspace under the
// check sandbox (denylist + bwrap + ~/.kube), passing the diff/identity ABI as
// environment. Exit 0 => allow (""); non-zero => refuse (fix-forward prompt naming
// the guard, its protected files, and its message). A denylisted guard command or a
// run failure is handed back as a fix-forward prompt (author defect), not an infra
// abort — except a genuine inability to execute, which fails closed.
func (r *Runner) runGuard(ctx context.Context, sel *selectt.Selection, st *guards.Stub, changed []string, ws, patchPath, hiveAbs string) (string, error) {
	matched := st.MatchedPaths(changed)
	env := append(os.Environ(),
		"BEEHIVE_HONEYBEE=1", // a gate evaluation is by definition a honeybee pass
		"BEEHIVE_GUARD_SUBMODULE="+sel.Submodule.Name,
		"BEEHIVE_GUARD_ID="+st.ID,
		"BEEHIVE_GUARD_PROTECTS="+strings.Join(st.ProtectsRaw, "\n"),
		"BEEHIVE_GUARD_DIFF_FILES="+strings.Join(changed, "\n"),
		"BEEHIVE_GUARD_MATCHED_FILES="+strings.Join(matched, "\n"),
		"BEEHIVE_GUARD_DIFF_PATCH="+patchPath,
	)

	// Denylist validation (fail-forward on a violating guard command).
	if r.CheckPolicy != nil {
		if err := r.CheckPolicy.Validate(st.Command); err != nil {
			return guardPolicyFailPrompt(sel.Submodule.Name, st, err), nil
		}
	}

	name := "sh"
	args := []string{"-c", st.Command}
	if r.CheckPolicy != nil {
		rw, ro := CheckBinds(ctx, r.Repo, r.Links, sel.Submodule, ws, ws, r.CheckPolicy.ReadPaths)
		pl, err := r.CheckPolicy.Argv(st.Command, ws, rw, ro)
		if err != nil {
			return "", fmt.Errorf("guard gate: building sandbox for guard %s: %w", st.ID, err)
		}
		if pl.Note != "" {
			r.logConcise("[honeybee] guard sandbox for %s: %s\n", st.ID, pl.Note)
		}
		name, args = pl.Name, pl.Args
	}

	o, err := r.runGuardCmd(ctx, ws, env, name, args...)
	if err != nil {
		return "", fmt.Errorf("guard gate: running guard %s in %s: %w", st.ID, sel.Submodule.Name, err)
	}
	if o.exitErr {
		return guardRefusedPrompt(sel.Submodule.Name, st, matched, o.out), nil
	}
	return "", nil
}

// runGuardCmd execs the guard with an explicit environment (the ABI), routed
// through the injectable RunGuard seam (nil => realRunGuard, which sets cmd.Env).
func (r *Runner) runGuardCmd(ctx context.Context, cwd string, env []string, name string, args ...string) (verifyOutcome, error) {
	if r.RunGuard != nil {
		return r.RunGuard(ctx, cwd, env, name, args...)
	}
	return realRunGuard(ctx, cwd, env, name, args...)
}

func splitNonEmptyLines(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// --- fix-forward prompts (mirror the check-gate prompt convention) ---

func guardRefusedPrompt(subName string, st *guards.Stub, matched []string, out string) string {
	tail := strings.TrimSpace(out)
	if len(tail) > 800 {
		tail = tail[len(tail)-800:]
	}
	return fmt.Sprintf(
		"Handoff gate FAILED: mutation guard %[1]q (submodules/%[2]s/GUARDS.md) REFUSED this change. It "+
			"protects %[3]s and your diff touches: %[4]s. This is a deployment-safety guard judged against LIVE "+
			"release state (e.g. the currently-active blue/green color), not a naming convention — a honeybee "+
			"must not make this change. Move your edit to the permitted target (e.g. the INACTIVE color), or, if "+
			"the change genuinely must touch the protected surface, that is an operator-gated action (a flip), not "+
			"a honeybee one: stop and escalate. Do NOT edit the guard to get past it — the gate runs the BASELINE "+
			"guard, so an in-pass edit to guard code is ignored. Guard output:\n%[5]s",
		st.ID, subName, strings.Join(st.ProtectsRaw, ", "), strings.Join(matched, ", "), tail)
}

func guardRegistryParseFailPrompt(subName string, err error) string {
	return fmt.Sprintf(
		"Handoff gate FAILED: submodules/%[1]s/GUARDS.md (the diff-scoped mutation-guard registry) does not "+
			"parse at the baseline (%[2]v). This is an author defect in a previously-landed GUARDS.md — fix the "+
			"registry (each stub needs a `Protects:` glob and a real `Command:` that can exit non-zero), commit "+
			"it, and leave the status; the gate re-runs.",
		subName, err)
}

func guardPolicyFailPrompt(subName string, st *guards.Stub, err error) string {
	return fmt.Sprintf(
		"Handoff gate FAILED: mutation guard %[1]q (submodules/%[2]s/GUARDS.md) command violates the check "+
			"command policy (%[3]v). Rewrite the guard's `Command:` to a real policy script without denied "+
			"commands, commit it, and leave the status; the gate re-runs.",
		st.ID, subName, err)
}

// realRunGuard execs the guard with an explicit environment (so the ABI env vars
// reach the guard even through bwrap, which inherits its parent's env). Mirrors
// realRunVerify's exit-classification.
func realRunGuard(ctx context.Context, cwd string, env []string, name string, args ...string) (verifyOutcome, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = cwd
	cmd.Env = env
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
