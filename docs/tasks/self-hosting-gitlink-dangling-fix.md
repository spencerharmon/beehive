# self-hosting-gitlink-dangling-fix

## Problem
The beehive self-hosting submodule's superproject gitlink was chronically bumped
to worktree commits that never reached the submodule's origin (e.g. dangling
`62243addcc`, `672eabd857`; only `origin/main`'s tip is real). A pointer recorded
at a local-only commit dangles on every OTHER host: the submodule sync / clone
there cannot resolve the gitlink, because the object lives nowhere but the one
local checkout that authored it.

The gap: the pointer bump (`git update-index --cacheinfo 160000,<sha>,…`) records
whatever sha it is handed with NO check that the sha is durably on origin. If the
push was skipped or failed, the bump still "succeeds" locally and the dangling
pointer is committed to `main`.

## Fix
Add a reachability gate in front of the pointer bump:

- `internal/git/git.go` — `RemoteContainsCommit(remote, branch, sha)`: confirms
  the sha is DURABLY on origin — reachable from the tip the remote advertises for
  `branch` (fetch + `merge-base --is-ancestor sha remote/branch`), strictly
  stronger than `CommitReachable` (which accepts a local-only object). Local
  sharing (`remote == ""`) falls back to `CommitExists`. A "remote lacks branch"
  fetch reports `(false, nil)`; other fetch failures surface as real errors.
  `BumpGitlink(path, sha)` wraps `update-index --add --cacheinfo` for the bump.
- `cmd/beehive/cmd_submodule.go` — `beehive submodule pointer-bump <sm> <commit>`:
  resolves the submodule's tracked branch + origin, and REFUSES the bump with an
  error NAMING the unreachable sha when `RemoteContainsCommit` is false; otherwise
  bumps the gitlink index entry in the beehive layer.

## Verification
`go test ./cmd/beehive/... ./internal/git/...`:
- `TestRemoteContainsCommit` — pushed commit reachable; local-only unpushed commit
  refused (the defect shape); no-remote local-sharing fallback; missing remote
  branch reports false.
- `TestPointerBumpRefusesUnpushedCommit` — an unpushed submodule commit is refused
  with the sha named and NOT staged.
- `TestPointerBumpAcceptsPushedCommit` — the pushed-commit success path stages the
  gitlink unregressed.

