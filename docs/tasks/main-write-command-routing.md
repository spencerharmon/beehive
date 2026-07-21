# main-write-command-routing

Route the remaining direct-on-primary-main CLI verbs through the convergence
protocol (`docs/main-convergence-protocol.md`) so none can manufacture the fork
that `main-convergence-user-docs` documents.

## Problem

`submodule sync` (`syncSubmodule`) already wraps its primary-main gitlink commit
with `SyncMainFromRemote` (before authoring) and `PublishPrimaryMain` (after). The
other verbs that also author a commit DIRECTLY on the primary main did not:

- `submodule remote` (`submoduleRemoteCmd`) — commits `.gitmodules`.
- `plan archive` (`planArchiveCmd`) — commits the leaned PLAN.md + archive docs.
- `task human` (`taskHumanCmd`) — commits the NEEDS-HUMAN PLAN.md flip.
- `instruction update` (`internal/instruct.Update`) — commits refreshed managed
  files.

Each authored on whatever local main happened to be, so a commit landing while
local main was behind the hive remote diverged into a fork that ff-only `pullMain`
cannot heal — the silent-loss bug.

## Fix

Each call site now mirrors `syncSubmodule` exactly:

    rootGit := git.New(root)
    remote, _ := rootGit.Remote(ctx)
    if err := rootGit.SyncMainFromRemote(ctx, remote); err != nil { return err }
    // ... author + CommitPaths ...
    if err := rootGit.PublishPrimaryMain(ctx, remote); err != nil { return err }

`SyncMainFromRemote`/`PublishPrimaryMain` are no-ops when the repo has no remote
(local-only hive), so single-host installs are unaffected.

## Files

- `cmd/beehive/cmd_submodule.go` — `submoduleRemoteCmd`
- `cmd/beehive/cmd_plan.go` — `planArchiveCmd`
- `cmd/beehive/cmd_task.go` — `taskHumanCmd`
- `internal/instruct/instruct.go` — `Update`

## Tests

`cmd/beehive/cmd_mainwrite_test.go` and
`internal/instruct/instruct_mainwrite_test.go`: per verb a fork-seeded fixture (a
peer pushes main ahead on the origin between setup and the verb run) asserts the
fork is healed (peer commit survives in local history) and the origin receives the
push (local and origin main converge on the verb's commit); a negative control
proves a stubbed no-op sync manufactures a fork ff-only merge cannot heal.
