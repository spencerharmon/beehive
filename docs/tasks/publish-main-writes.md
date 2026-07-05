# publish every beehived write to origin main

## Problem

`beehived` (`internal/web/web.go`) is the frontend that turns operator actions into
git-backed writes on the beehive root's primary checkout. Its mutating handlers each
committed **locally only** via `s.commit` (`git add -A` + commit under `gitMu`) and
stopped there: `roiPost`, `secretsPost`, `submoduleAdd`, `submoduleLink`, `envDeploy`.
On a multi-host swarm the commit never reached `origin/main`, so peers and honeybees on
other hosts (which branch off `origin/main`) never saw the edit — a silently local write.

Two handlers already published (`planDelete`, `mergePost`): they called `s.commit`
followed by a separate push-only `publishMain(ctx)`. That was two `gitMu` acquisitions
(a race window between the commit and the push) and left five write paths local-only.

## Fix

Unify commit+push into a single 2-arg `publishMain(ctx, msg)` and route **every**
mutating handler through it. One `gitMu`-held critical section now:

1. `git add -A` + commit under `msg` (`s.git.Commit`). An empty commit is **not** an
   error — `git.ErrNothing` is tolerated, so an idempotent write (an already-merged
   merge, an unchanged file) still succeeds. This absorbs the per-handler
   `errors.Is(err, git.ErrNothing)` tolerance that `mergePost`/`submoduleAdd`/`Link`/
   `envDeploy` used to carry.
2. Resolve the checkout's branch (`CurrentBranch`, not a hardcoded `"main"`) and push
   it. **No remote => single-host:** the local commit is the whole publish (honeybees
   branch off local main) and the push is skipped — `Remote()==""` returns early.
3. On a **non-fast-forward** (a peer advanced the remote under us) fetch, merge the
   advanced branch in (`merge --no-edit FETCH_HEAD`) — never clobbering the peer's
   commit — and retry the push once. A real merge conflict is `merge --abort`ed (the
   checkout is left clean) and the error is surfaced.

The seven handlers drop their standalone `s.commit(...)`/`publishMain(ctx)` pairs for a
single `s.publishMain(ctx, msg)`; the old push-only `publishMain` and the now-unused
`commit` method are removed. `mergePost` keeps its full merge-button-wire body
(checkout tracked branch, `git.Merge`, push the tracked branch to the submodule origin,
advance the pointer) and only swaps its `commit`+push tail for the 2-arg call.

Anchored to the current submodule tip `76512043` (`optional-file-links`), after
merge-button-wire (the DONE task that introduced the 1-arg push-only `publishMain` and
the `planDelete`/`mergePost` publish path) and `web/stats` landed — the prior attempt on
a stale base (`59a64e5`) was re-cut here per arbitration.

## Tests (`internal/web/web_test.go`)

- `TestFrontendWritesReachOrigin` — ROI edit and env deploy (two distinct handlers, one
  shared path) each land on a temp bare origin's main (origin main == local HEAD, content
  present).
- `TestFrontendWriteRetriesOnConcurrentAdvance` — a peer advances origin main first; our
  write is a non-ff, and fetch+merge+retry lands it with **no lost write** (both the
  peer's file and our edit on origin main; peer commit is an ancestor, not clobbered).
- `TestFrontendWriteNoOriginCommitsLocally` — no remote: the write still commits locally
  and does not error.

`CGO_ENABLED=0 go build/vet/test ./internal/web/` green.
