# hive-remote-force-rewind-fix

## Observation
An external push to the hive remote/main was force-rewound back to beehived's
local main (repair commit `cafd6ec6ad` rewound to ancestor `fc0cfb140f`) — a
non-fast-forward update no readable non-force publish path explains.

## Code-side audit (all push call sites)
Every main-write path is a PLAIN, non-force push that merges the advanced remote
main in BEFORE pushing; none passes `--force`, `--force-with-lease`, or a `+`
refspec:

- `internal/git/git.go` `Push` — `git push <remote> <refspec>`, no force.
- `PublishToMain` — fetch+merge remote main, then `push HEAD:refs/heads/main`;
  non-fast-forward is re-merged and retried, never forced.
- `PublishPrimaryMain` — `push HEAD:refs/heads/main`; non-ff → fetch+merge+retry.
- `UpdateLocalMain` — `push . HEAD:refs/heads/main`; non-ff → merge+retry.
- `PushBranchReconciled` / `reconcileOrphan` — explicitly non-destructive (merge
  `-s ours` via plumbing so the retried push is a genuine fast-forward); doc-
  documented "NEVER force-pushes and never deletes the remote ref".
- `internal/web/web.go` `publishMain` — `Push`; non-ff → fetch+merge+retry.

Conclusion: **no code-side call site force-pushes.** Per the task, the cause is
therefore server-side config within this install's control — the receiving repo
silently ACCEPTING a non-fast-forward update to main.

## Fix (server-config, approach b)
The pre-receive hook (`internal/config/hook.go`) is the beehive-installed server-
side enforcement point this install controls. Extended it with a main-force-rewind
guard, applied to EVERY identity (not just the honeybee ROI rule):

- Refuse any non-fast-forward update to `refs/heads/main` — `old` not an ancestor
  of `new` (`git merge-base --is-ancestor old new`).
- Refuse a `refs/heads/main` deletion (delete-then-recreate would bypass the
  ff-check to the same rewind effect).
- Genuine fast-forward advances of main are unaffected; non-main refs (the
  bee-<taskid> branches on submodule origins, a different repo) are untouched so
  the runner's `PushBranchReconciled` orphan handling still works.

## Verification (regression test)
`internal/config/hook_test.go` `TestPreReceiveRejectsMainForceRewind` drives the
INSTALLED hook through real pushes into a convergence-target repo:
- a genuine fast-forward advance of main is ACCEPTED;
- `git push --force origin <ancestor>:refs/heads/main` (the exact observed shape)
  is REJECTED with `non-fast-forward (force) update to main`, dest main UNMOVED;
- deleting main is REJECTED.

Proven to bite (fails without the hook change):

```
$ git stash push internal/config/hook.go
$ go test ./internal/config/ -run TestPreReceiveRejectsMainForceRewind
--- FAIL: TestPreReceiveRejectsMainForceRewind
    force-rewind of main to an ancestor must be REJECTED, but it succeeded:
     + fda8f49...6f341a9 ... -> main (forced update)
FAIL
```

With the fix:

```
$ go test ./internal/config/
ok  	github.com/spencerharmon/beehive/internal/config	0.402s
```

`TestInstallHooks` was also extended to assert the canonical hook carries the new
guard strings (`refs/heads/main`, `merge-base --is-ancestor`,
`non-fast-forward (force) update to main`, `refusing to delete main`).

## Install / live application
The guard ships in the canonical `preReceiveHook` laid down by
`config.InstallHooks` (`beehive init` / `beehive hook install`). Existing installs
pick it up on the next `beehive hook install` (idempotent, upgrades the hook in
place). No out-of-band workaround was needed: the corrective config is fully within
this install's control.
