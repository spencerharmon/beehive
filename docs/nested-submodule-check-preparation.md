# Pinned Nested Check Dependencies

The runner prepares nested source gitlinks before handing a private code worktree
to Work, Review, or Arbitrate agents, and again immediately before task checks.
The second boundary also covers post-merge pin changes and already-merged recovery
checks, including checkouts reused across attempts.

`git.Repo.PrepareNestedSubmodules` enumerates gitlinks and synchronizes configured
submodule URLs, then runs `submodule update --init --checkout` with explicit paths
at each level before recursing. This bypasses active-path filtering and picks up
changed child URLs only after their parent pin is checked out. Nested dependencies use recorded
gitlink commits, never `--remote` branch tips. The parent source HEAD and the hive's
tracked-branch-tip invariant are unchanged. The runner calls it only on its private
checkouts, not the shared live source checkout. Setup errors propagate as
infrastructure errors before execution, not as failed assertions that reopen work.
No forced checkout or cleaning of authored nested files is performed.

Regression tests use local temporary origins with two levels of gitlinks and a
real Go test importing nested source. They cover fresh handoffs, reuse, changed
pins, teardown/recreation, actual review merges, both already-merged recovery
routes, and unavailable origins/commits without publishing or incrementing attempts.

An optional `TestNestedCheckoutLiveCheck` clones a repository afresh and runs an
unchanged check through the runner's real preparation and default sandbox policy.
Set `BEEHIVE_NESTED_CHECK_REPO` and `BEEHIVE_NESTED_CHECK_COMMAND`, optionally
`BEEHIVE_NESTED_CHECK_REF`, then run it with `go test ./internal/swarm -run
TestNestedCheckoutLiveCheck -count=1 -v -timeout=20m` through the owning worktree's
`beehive submodule worktree exec` command. Deployment-specific values belong in
the hive change doc, never in this source tree.

For a self-hosted deployment, merge the reviewed fix first, use the install's
documented rebuild command from `LOCALS.md`, and rerun the live probe from the
landed revision before unblocking downstream tasks. A passing branch probe is not
evidence that already-running binaries contain the fix.
