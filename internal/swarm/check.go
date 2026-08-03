package swarm

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/links"
	"github.com/spencerharmon/beehive/internal/repo"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

// policyViolationError marks a check that failed the command denylist (as opposed
// to an infra failure to run it). Callers unwrap it to hand the author a
// fix-forward prompt instead of failing closed on an un-runnable command.
type policyViolationError struct{ err error }

func (e policyViolationError) Error() string { return e.err.Error() }
func (e policyViolationError) Unwrap() error { return e.err }

// runCheck executes a task's `Check:` DoD command under the check policy: the
// command denylist is enforced (a violation is returned as an error → the gate
// fails CLOSED), and the command runs inside filesystem confinement scoped to the
// task's OWN submodule checkout (writable) plus its LINKED submodule checkouts and
// the operator-declared read paths (read-only). It returns the same verifyOutcome
// the gate/injection consume. legacyDir is the working directory used ONLY on the
// no-policy path (nil CheckPolicy — tests and un-wired callers), preserving the
// historical bare `sh -c` behavior byte-for-byte.
func (r *Runner) runCheck(ctx context.Context, sel *selectt.Selection, check, legacyDir string) (verifyOutcome, error) {
	if r.CheckPolicy == nil {
		// Legacy path: no policy configured (tests / not wired). Byte-identical to the
		// historical gate call — a bare shell in legacyDir, no validation, no sandbox.
		return r.runVerify(ctx, legacyDir, "sh", "-c", check)
	}
	cwd := sel.Submodule.RepoDir()
	if !filepath.IsAbs(cwd) {
		cwd = filepath.Join(legacyDir, cwd)
	}
	return r.runCheckAt(ctx, sel, check, cwd, legacyDir)
}

// runCheckIn runs a task's `Check:` DoD in an explicit ABSOLUTE cwd (the
// Review/Arbitrate inspection worktree, which holds the MERGED tree the runner is
// about to push) instead of the submodule's tracked checkout. Same policy/sandbox
// semantics as runCheck; the legacy (no-policy) path runs a bare shell in cwd.
func (r *Runner) runCheckIn(ctx context.Context, sel *selectt.Selection, check, cwd string) (verifyOutcome, error) {
	if r.CheckPolicy == nil {
		return r.runVerify(ctx, cwd, "sh", "-c", check)
	}
	return r.runCheckAt(ctx, sel, check, cwd, cwd)
}

// runCheckAt is the shared policy/sandbox body: validate the command against the
// denylist (a violation fails CLOSED), resolve the sandbox binds for cwd, and run
// under confinement. base is the directory used to resolve relative read paths.
func (r *Runner) runCheckAt(ctx context.Context, sel *selectt.Selection, check, cwd, base string) (verifyOutcome, error) {
	if err := r.CheckPolicy.Validate(check); err != nil {
		return verifyOutcome{}, policyViolationError{err: err}
	}
	rw, ro := CheckBinds(ctx, r.Repo, r.Links, sel.Submodule, cwd, base, r.CheckPolicy.ReadPaths)
	pl, err := r.CheckPolicy.Argv(check, cwd, rw, ro)
	if err != nil {
		return verifyOutcome{}, err
	}
	if pl.Note != "" {
		r.logConcise("[honeybee] check sandbox for %s: %s\n", sel.Task.ID, pl.Note)
	}
	return r.runVerify(ctx, cwd, pl.Name, pl.Args...)
}

// CheckBinds resolves the sandbox bind sets for a check running in cwd (a
// submodule's checkout): the writable set is the checkout plus its git-common-dir
// (so `git` checks resolve their object store, which for a submodule lives under
// the superproject's .git/modules/…, outside the checkout); the read-only set is
// the LINKED submodule checkouts (derived from SUBMODULE-LINKS.yaml — never
// hardcoded), the operator-declared readPaths, and the default kubeconfig dir when
// present. Exported so `beehive task check` confines identically to the gate.
func CheckBinds(ctx context.Context, rp *repo.Repo, lk *links.Links, sub repo.Submodule, cwd, base string, readPaths []string) (rw, ro []string) {
	rw = []string{cwd}
	if gc := gitCommonDir(ctx, cwd); gc != "" {
		rw = append(rw, gc)
	}
	for _, d := range linkedRepoDirsFor(rp, lk, sub.Name) {
		if !filepath.IsAbs(d) {
			d = filepath.Join(base, d)
		}
		ro = append(ro, d)
	}
	ro = append(ro, readPaths...)
	// Default kubeconfig location so a `kubectl` check works out of the box without
	// the operator restating it; a kubeconfig elsewhere goes in CheckReadPaths.
	if home := os.Getenv("HOME"); home != "" {
		ro = append(ro, filepath.Join(home, ".kube"))
	}
	if kc := os.Getenv("KUBECONFIG"); kc != "" {
		for _, p := range filepath.SplitList(kc) {
			if p != "" {
				ro = append(ro, p)
			}
		}
	}
	return rw, ro
}

// gitCommonDir resolves the absolute git-common-dir for dir, or "" if dir is not a
// git checkout. Used to bind a submodule's real object store into the sandbox.
func gitCommonDir(ctx context.Context, dir string) string {
	out, err := git.New(dir).Run(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// linkedRepoDirsFor returns the repo/ checkout dirs of the submodules LINKED to
// `name`, derived from lk (SUBMODULE-LINKS.yaml). The flat `submodules:` list
// models one mutually-linked set: if `name` is in it, every OTHER listed submodule
// is linked. Directional `deps` edges touching `name` (either endpoint) also link
// it, so a check may read what it depends on.
func linkedRepoDirsFor(rp *repo.Repo, lk *links.Links, name string) []string {
	if lk == nil || rp == nil {
		return nil
	}
	linked := map[string]bool{}
	inFlat := false
	for _, n := range lk.Submodules {
		if n == name {
			inFlat = true
			break
		}
	}
	if inFlat {
		for _, n := range lk.Submodules {
			if n != name {
				linked[n] = true
			}
		}
	}
	for _, e := range lk.Deps {
		if e.From == name {
			linked[e.To] = true
		}
		if e.To == name {
			linked[e.From] = true
		}
	}
	if len(linked) == 0 {
		return nil
	}
	subs, err := rp.Submodules()
	if err != nil {
		return nil
	}
	var out []string
	for _, s := range subs {
		if linked[s.Name] {
			out = append(out, s.RepoDir())
		}
	}
	return out
}
