# root-cause the session-transcript finalize regression + recover the backlog

## Problem

729 of 760 session files on `main` are 185–191 B **stubs** (`repo.SessionStub`), not
transcripts: the real transcript never got promoted to `main`, so the audit engine —
which reads `main` — is blind to 96% of the corpus. It regressed right after pass 1
(last finalized epoch 1782841513; every session since is a stub) and, being
fire-and-forget, produced no retry and no signal. 97 of the 729 stubs already have no
surviving stream branch (transcript gone); 632 are still recoverable.

`Runner.finish` → `finalizeSession` (`internal/swarm/swarm.go`) squashes the stream
branch down onto the start stub and merges the durable transcript to `main` once. The
promotion is deliberately **best-effort**: on failure it logs a stderr WARNING and
leaves the stub, so a cosmetic transcript conflict can never block real work (the
decouple at `finish` that publishes the WORK first and gates completion on *its*
result, not the transcript's).

### Root cause

`startSession` plants the stub as the stream branch's first commit and publishes it to
`main` **before** the agent runs. When a peer advances `main` in that window, the stub
publish's `PublishToMain` merges the advanced `main` back onto the stream branch, so
the branch HEAD becomes a **merge commit above the stub** whose tree carries the peer's
files. `finalizeSession` then did:

    reset --soft <stub>      # rewind past the merge, KEEPING its tree staged
    commit -- <rel>          # commit ONLY the transcript path

The `--soft` reset left the merge's peer files staged; the pathspec commit recorded
only `rel` and **stranded the rest as uncommitted residue** in the worktree. The
subsequent publish's `merge main` was then refused — `Your local changes to the
following files would be overwritten by merge` (exit 2, zero unmerged paths) — which
`PublishToMain` misreports as `git: merge conflict (conflicted: none)`. Net: the
transcript stayed on the branch and `main` kept the stub. The work-publishes-first
decouple made `main` reliably non-FF at finalize, which is what turned this latent
stranding into the steady-state regression.

## Fix

Rebuild the tree explicitly instead of rewinding through the merge
(`finalizeSession`):

    tip := RevParse(HEAD)         # the streamed transcript tip
    reset --hard <stub>          # pristine projection of main-at-session-start
    checkout <tip> -- <rel>      # restore ONLY the transcript onto the clean stub
    CommitPaths(<rel>)           # stub-tree + transcript, nothing else

The result is a clean single commit of stub-tree + transcript, so the publish's
`merge main` re-merges the advanced `main` with no working-tree clash. This is a real
fix to the stranding mechanism, **not** a retry wrapper. Finalize stays **non-fatal**
to WORK completion: `finish` still returns the WORK publish result and only logs the
transcript WARNING, and `SessionPublished` is set true **only** when finalize succeeds.

### Reclaim gating (already correct — verified, no change)

The stream branch is the last copy of a stranded transcript, so it must not be
reclaimed until the transcript is on `main`. That gate already exists and is complete:

- `finish` sets `res.SessionPublished = true` **only** in the success branch of
  `finalizeSession` (`swarm.go`); a failed finalize leaves it false.
- `cmd/honeybee/main.go`: `sessionBranchDisposable = res.SessionPublished ||
  (res.Completed && res.SessionID == "")`; the deferred cleanup deletes the local and
  remote stream branch **only** when `sessionBranchDisposable`. The second disjunct is
  a reconcile-dedup pass that started no session (no stub planted, nothing to strand);
  `res.SessionID` is set for every real session, so a failed finalize there can never
  satisfy it.

So a failed finalize keeps `SessionPublished=false` and the stream branch intact —
"stranded", never "lost". The only stream-branch reclaim path in the tree is that
gated block; `reclaimSourceBranch` handles the *code* `bee-<taskid>` branch under its
own merged-guard, and `internal/editor` reclaim is a different subsystem. No code
change was needed here; the value this task adds is the finalize fix (so the gate's
happy path actually fires) plus the sweep below. The regression guard
`TestRunTranscriptPublishFailureDoesNotBlockWork` already asserts the false-on-failure
half of the gate.

## Sweep — recover the stranded backlog

`SweepSessionTranscripts` (`internal/swarm/sweep.go`) is the deferred, idempotent
completion of finalize. For every `submodules/<sm>/sessions/*.md` that is a stub on
`main` (`repo.ParseSessionStub`), it resolves the named stream branch
(`refs/heads/<b>`, then `refs/remotes/<remote>/<b>`) and, when that branch exists AND
its tip carries a real (non-stub) transcript, rewrites `main`'s copy to it and
publishes once — via a throwaway detached worktree cut from `main` + `PublishToMain`,
never the live checkout. The transcript is copied with `checkout <ref> -- <path>` (the
exact blob; `git show` trims). It reports and never fabricates:

- `Recovered`  — stub promoted from a surviving branch's real transcript.
- `GoneBranch` — stub whose stream branch no longer exists (unrecoverable: the ~97).
- `NoTranscript` — branch exists but its tip is itself a stub / lacks the path.

Branches currently checked out in a live worktree (an in-flight session) are skipped,
so the sweep never races a running honeybee's own finalize. It is idempotent: once a
transcript reaches `main` it is no longer a stub, so a second run is a no-op.

### Entrypoint — beehived startup

`sweepSessions` runs the sweep at `beehived` startup, right after `RecoverEditors`,
once per served registry repo, best-effort and non-fatal (a failure logs and serving
continues). Rationale:

- **Self-healing, no operator action.** The backlog (the reachable 632) is recovered
  on the next `beehived` start, and any future stranded stub is swept on the following
  start — the same shape as editor-worktree recovery already there.
- **Correct convergence.** It publishes via the honeybee converge-to-main path (temp
  worktree off `main` + `PublishToMain`), deriving remote-vs-local sharing from the
  repo's own remotes, so it is safe on shared local checkouts and multi-host remotes
  alike and never authors in the live tree.
- **Multi-repo aware.** beehived now serves a registry of repos; the sweep runs for
  every `reg.Repos` entry, matching `RecoverEditors`' per-served-repo scope.
- **Alternatives rejected.** A per-pass runner hook would re-scan every session every
  pass (wasteful, and races in-flight finalizes); a one-shot manual command would not
  be self-healing. Startup is idempotent and hands-off.

## Tests

- `internal/swarm/finalize_test.go::TestFinalizeSessionRecoversWhenMainAdvancedBeforeStub`
  — regression guard: advances `main` BEFORE the stub publish so the stub publish
  produces a merge commit above the stub (asserted via `head != stubCommit`), streams
  a transcript, advances `main` again so finalize's publish is non-FF, and asserts the
  transcript reaches `main` (not a stub) with peer files intact. Fails with the old
  `reset --soft` (`git: merge conflict (conflicted: none)`), passes with the fix.
- `internal/swarm/sweep_test.go::TestSweepSessionTranscripts` — all four cases at once
  (recoverable / gone-branch / stub-tip / already-final): only the recoverable one is
  promoted (byte-faithful), the rest reported or ignored, never fabricated; a second
  run proves idempotency.
- `internal/swarm/sweep_test.go::TestSweepSkipsLiveSessionBranch` — a stub whose branch
  still has a checked-out worktree is left for that session's own finalize.

`CGO_ENABLED=0 go build ./... && go test ./...` green.

## Recovery run

The one-time recovery of the reachable backlog is performed by running the sweep once
against the hive (it also self-heals on every subsequent `beehived` start). Real
promoted/gone counts are recorded in the beehive-layer change doc.
