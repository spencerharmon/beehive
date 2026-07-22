# The submodule-pointer invariant

> **A submodule gitlink MUST ALWAYS equal the tip of that submodule's configured
> tracked branch** (`.gitmodules` `submodule.<path>.branch`, default `main`) as
> published on the submodule's origin. **Never** a `bee-<taskid>` tip. **Never**
> an intermediate merge parent. **Never** any other commit. The pointer advances
> **only** when the tracked branch tip itself advances — i.e. after work has been
> merged into it on the origin.

This is not a style preference. It is a hard correctness invariant: violating it
silently corrupts the superproject and eventually **halts the entire swarm**.

## Why (the failure mode this prevents)

The superproject records exactly one commit per submodule — its gitlink. Git
materializes that commit with `git submodule update`; if it cannot resolve the
recorded sha, the primary working tree is **dirty and unhealable**, and every
honeybee preflight aborts:

```
preflight: remote-sharing checkout ... is dirty and cannot be reset to a clean
projection of HEAD (submodule resync/clean failed for submodules/<sm>/repo)
```

Once that happens, the timer keeps firing passes but **none run** — no commits,
no progress, indefinitely — until an operator heals the pointer by hand.

A gitlink that names anything other than the tracked-branch tip names a commit
whose lifetime the superproject does **not** control:

- A **`bee-<taskid>` tip** is a disposable per-attempt branch. It is pushed to
  origin, merged into the tracked branch at review approval, then **reclaimed
  (deleted)**. If the gitlink still names that bee-tip when the branch is
  reclaimed — and the bee-tip is not an ancestor of the tracked branch (squash /
  re-merge / a dropped pointer-advance) — the sha is **garbage-collected off
  origin** and the gitlink dangles.
- Any **non-tip commit** can likewise become unreachable, or be stranded when
  the superproject `main` is force-rewound / forked and the pointer-advance
  commit is dropped (see `docs/main-convergence-protocol.md`).

The tracked-branch tip, by contrast, is **by definition** present on origin, so
`git submodule update` always resolves it and the tree stays healable. Pinning
the pointer to the tracked tip makes the whole class of "dangling gitlink →
unhealable tree → swarm halt" impossible.

Observed live: gitlinks `62243addcc`, `672eabd857`, `ae42d1b3f5` — all
bee-branch tips, merged-then-reclaimed, each stranded by a lost pointer-advance;
each wedged the swarm until healed by hand.

## The protocol

The **runner owns the gitlink. The agent never writes it.**

- **Agents** (work / review / arbitrate) do their normal job: commit code on
  `bee-<taskid>`, push it to the submodule origin, and — at review/arbitration
  approval — **merge `bee-<taskid>` into the submodule's tracked branch on its
  origin**. They flip `PLAN.md` and write the change doc. They **do not** touch
  `submodules/<sm>/repo` and **never** bump or stage the gitlink.
- **The runner** re-derives the gitlink from `origin/<tracked-branch>` and pins
  it there via `pinPointerToTrackedTip` (`internal/swarm/swarm.go`):
  - **at work-pass start**, so the code worktree branches off the live tracked
    tip; and
  - **at every task-bearing completion**, just before publishing, so the
    PUBLISHED superproject records the tracked tip and nothing else — correcting
    any stray pointer an agent staged.

`pinPointerToTrackedTip` fetches `origin/<branch>`, hard-resets the shared
submodule checkout to that tip, and commits the gitlink. It is idempotent
(a no-op when already pinned) and a no-op on a no-remote / single-host install
(there is no origin to pin against, so the recorded pointer stands).

### How work still lands on the tracked branch

The pointer only advances when the tracked branch advances. Work reaches the
tracked branch the normal way: the implementer pushes `bee-<taskid>`; the
reviewer (approve) or arbitrator (side-with-implementer) **merges `bee-<taskid>`
into the tracked branch and pushes origin**. The very next pin — at that same
completion — then re-derives the gitlink from the now-advanced tracked tip. The
bee-tip is never itself recorded as the pointer; it only ever contributes its
commits to the tracked branch via the merge.

## Enforcement points

| Where | What |
|-------|------|
| `pinPointerToTrackedTip` | the ONLY gitlink writer for a submodule under the swarm; always `origin/<branch>` tip |
| Work-pass start (`syncWorktreeBase` → pin) | worktree branches off the live tracked tip; pointer pinned |
| `finish` (before publish) | re-pins to the tracked tip for every task-bearing pass, overriding any agent bump |
| `RemoteContainsCommit` (`internal/git/git.go`) | a pointer bump must target a commit reachable on origin (the CLI `beehive submodule` guard) |

## Recovery (if a dangling pointer is already recorded)

Heal the gitlink to the reachable tracked-branch tip and let convergence follow:

1. Confirm the recorded gitlink is unreachable and identify the tracked tip:
   `git -C submodules/<sm>/repo cat-file -t $(git rev-parse main:submodules/<sm>/repo)`
   (fails) vs `git -C submodules/<sm>/repo rev-parse origin/<branch>`.
2. In a hive-layer worktree rebased on the remote tip, set the gitlink to
   `origin/<branch>` tip (`git update-index --cacheinfo 160000,<tip>,submodules/<sm>/repo`),
   commit, and publish fast-forward to the hive remote.
3. Materialize the checkout to the tracked tip so the tree goes clean; the
   runner's `pinPointerToTrackedTip` keeps it there thereafter.

See also `docs/main-convergence-protocol.md` (why the superproject `main` must be
fork- and force-rewind-safe so a correct pointer-advance is never dropped).
