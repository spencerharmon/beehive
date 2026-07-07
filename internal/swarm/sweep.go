package swarm

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/repo"
)

// SweepResult reports what SweepSessionTranscripts did. Recovered lists the
// session files (repo-relative paths) whose real transcript was promoted to main.
// GoneBranch and NoTranscript list stubs left UNTOUCHED because their transcript
// is unrecoverable — the stream branch is gone, or the branch tip is itself still
// a stub (no transcript was ever recorded). The sweep NEVER fabricates a
// transcript, so those two are reported, not synthesized.
type SweepResult struct {
	Recovered    []string
	GoneBranch   []string
	NoTranscript []string
}

// Empty reports whether the sweep found nothing to act on or report.
func (s SweepResult) Empty() bool {
	return len(s.Recovered)+len(s.GoneBranch)+len(s.NoTranscript) == 0
}

// Summary is a one-line, log-friendly tally.
func (s SweepResult) Summary() string {
	return fmt.Sprintf("recovered=%d gone-branch=%d no-transcript=%d",
		len(s.Recovered), len(s.GoneBranch), len(s.NoTranscript))
}

// SweepSessionTranscripts finishes the job a failed finalizeSession left undone:
// it promotes to main every session transcript that is still a STUB there but
// exists in full on its (surviving) stream branch. This is the deferred, idempotent
// completion of finalize — the recovery half of the transcript pipeline whose
// forward half (finalizeSession) the regression broke, stranding finished sessions
// as stubs while their real transcripts sat on kept stream branches.
//
// For every submodules/<sm>/sessions/*.md that is a stub on main
// (repo.ParseSessionStub), it resolves the named stream branch and, when that
// branch still exists AND its tip carries a real (non-stub) transcript for the same
// path, rewrites main's copy to that transcript and publishes once via the same
// converge-to-main path honeybees use (a throwaway worktree off main + PublishToMain
// — never the live checkout). A stub whose branch is GONE, or whose branch tip is
// ITSELF a stub, is reported and left in place: the sweep never invents a transcript
// it cannot source. Branches currently checked out in a live worktree (an in-flight
// session) are skipped so the sweep never races a running honeybee's own finalize.
//
// Idempotent: once a transcript reaches main it is no longer a stub, so a second run
// is a no-op. Best-effort at the batch level — a single unreadable branch is dropped
// (still recoverable on a later run) rather than sinking the whole sweep.
func SweepSessionTranscripts(ctx context.Context, primary *git.Repo, subs []repo.Submodule, remote string) (SweepResult, error) {
	var res SweepResult

	// Branches with a live worktree belong to an in-flight session; skip them so a
	// concurrent honeybee's own finalize owns the promotion, not this sweep.
	live := map[string]bool{}
	if wts, err := primary.Worktrees(ctx); err == nil {
		for _, w := range wts {
			if w.Branch != "" {
				live[w.Branch] = true
			}
		}
	}

	resolveRef := func(branch string) string {
		if _, err := primary.RevParse(ctx, "refs/heads/"+branch); err == nil {
			return "refs/heads/" + branch
		}
		if remote != "" {
			if _, err := primary.RevParse(ctx, "refs/remotes/"+remote+"/"+branch); err == nil {
				return "refs/remotes/" + remote + "/" + branch
			}
		}
		return ""
	}

	type recoverable struct{ rel, ref string }
	var todo []recoverable
	for _, sub := range subs {
		dir := path.Join("submodules", sub.Name, "sessions")
		out, err := primary.Run(ctx, "ls-tree", "-r", "--name-only", "main", "--", dir)
		if err != nil {
			return res, fmt.Errorf("list session files for %s: %w", sub.Name, err)
		}
		for _, rel := range strings.Split(out, "\n") {
			rel = strings.TrimSpace(rel)
			if rel == "" || !strings.HasSuffix(rel, ".md") {
				continue
			}
			content, err := primary.Show(ctx, "main", rel)
			if err != nil {
				return res, fmt.Errorf("read %s on main: %w", rel, err)
			}
			branch, isStub := repo.ParseSessionStub(content)
			if !isStub {
				continue // already a real transcript on main — nothing to do
			}
			if live[branch] {
				continue // in-flight session; its own finalize will promote it
			}
			ref := resolveRef(branch)
			if ref == "" {
				res.GoneBranch = append(res.GoneBranch, rel)
				continue
			}
			tip, err := primary.Show(ctx, ref, rel)
			if err != nil {
				// Branch exists but the transcript path is absent at its tip: nothing
				// to source, so report rather than fabricate.
				res.NoTranscript = append(res.NoTranscript, rel)
				continue
			}
			if _, tipIsStub := repo.ParseSessionStub(tip); tipIsStub {
				res.NoTranscript = append(res.NoTranscript, rel)
				continue
			}
			todo = append(todo, recoverable{rel: rel, ref: ref})
		}
	}
	if len(todo) == 0 {
		return res, nil
	}

	// Rebuild the stranded transcripts on a throwaway worktree cut from main and
	// publish once — never author in the live checkout. Copy each transcript with
	// `checkout <branch> -- <path>` (the exact blob, unlike a trimmed `git show`),
	// so main gets a byte-faithful copy of the branch's final transcript.
	tmp := filepath.Join(primary.Dir, ".worktrees", "session-finalize-sweep")
	_, _ = primary.Run(ctx, "worktree", "remove", "--force", tmp)
	_, _ = primary.Run(ctx, "worktree", "prune")
	if _, err := primary.Run(ctx, "worktree", "add", "--detach", tmp, "main"); err != nil {
		return res, fmt.Errorf("create sweep worktree: %w", err)
	}
	defer func() {
		_, _ = primary.Run(context.Background(), "worktree", "remove", "--force", tmp)
		_, _ = primary.Run(context.Background(), "worktree", "prune")
	}()

	tg := git.New(tmp)
	var rels []string
	for _, rc := range todo {
		if _, err := tg.Run(ctx, "checkout", rc.ref, "--", rc.rel); err != nil {
			// One unreadable branch must not sink the batch; leave its stub in place
			// (still recoverable on a later run) and keep going.
			res.NoTranscript = append(res.NoTranscript, rc.rel)
			continue
		}
		rels = append(rels, rc.rel)
	}
	if len(rels) == 0 {
		return res, nil
	}
	msg := fmt.Sprintf("session: finalize sweep recovered %d transcript(s)\n\nBeehive: session finalize sweep", len(rels))
	if err := tg.CommitPaths(ctx, msg, rels...); err != nil {
		if errors.Is(err, git.ErrNothing) {
			return res, nil
		}
		return res, fmt.Errorf("commit recovered transcripts: %w", err)
	}
	if err := tg.PublishToMain(ctx, remote); err != nil {
		// The recovery commit lives only on the throwaway worktree (removed on defer),
		// so a failed publish leaves main untouched and every transcript still on its
		// branch — a later sweep retries cleanly. Surface the error; recover nothing.
		return res, fmt.Errorf("publish recovered transcripts to main: %w", err)
	}
	res.Recovered = rels
	return res, nil
}
