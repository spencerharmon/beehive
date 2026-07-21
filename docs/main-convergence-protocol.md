# Main-convergence protocol (and the silent-loss fork bug it prevents)

**Audience: ANY writer to a beehive hive's `main` — honeybee passes, beehived
(editor/chat/resolve), the `beehive` CLI verbs, and any human- or agent-driven
edit that lands a commit on the hive repo or drives the equivalent submodule
process.** Read this before you author anything on `main` by a path other than a
worktree + `PublishToMain`.

## The invariant

A hive has (at most) two `main` anchors that must stay reconcilable:

- **local primary `main`** — the checked-out working tree beehived owns; the
  target of `receive.denyCurrentBranch=updateInstead` pushes.
- **`remote/main`** (`gitea/main`, `origin/main`, …) — the shared ref other
  hosts and external pushers converge on. Absent in a local-only hive.

**The whole convergence model assumes `main` only ever *fast-forwards*.** Passes
author in isolated worktrees and converge by `PublishToMain`, which fetch+merges
before pushing; beehived's background `pullMain` does a `git pull --ff-only`.
Fast-forward-only is what lets many uncoordinated writers share one ref without a
lock.

## The bug: a fork causes silent, permanent loss

`pullMain`'s `git pull --ff-only` **records a divergence and proceeds — it never
merges** (`internal/web/sessions.go`, `internal/git/git.go` `Pull`). That is
correct *only while the invariant holds*. The moment two commits share a base but
neither descends from the other — a **fork** — ff-only can no longer cross it,
and the only thing that heals a fork is a `PublishToMain`-style fetch+**merge**.
If nothing merges the two lines before one is republished forward, the other line
is silently dropped. Every artifact authored on the losing line vanishes with no
error surfaced.

### How a fork used to get manufactured

The `beehive` CLI verbs (`submodule sync`, `submodule remote`, `plan`, `task`,
`instruction update`) author a commit **directly on the primary `main`** via
`git.New(root).CommitPaths(...)` — no worktree, and (before this fix) **no merge
of `remote/main` first**. If local `main` lagged the remote (e.g. an external
push landed on `gitea/main` that local `main` had not yet fast-forwarded), the
commit forked history:

```
                     BASE  (last commit both anchors share)
                       │
  external push ──────►├───────────────► X   [only on remote/main]
                       │
  submodule sync ─────►└───────────────► S   [only on local main, STALE base]
  (CommitPaths on primary main,
   no fetch/merge first)

  beehived pullMain:  git pull --ff-only remote main
      local has S (not on remote), remote has X (not on local)
      → DIVERGED → recorded, NEVER merged → X never ingested
                       │
  the local S-line keeps growing and getting republished;
  X is orphaned on the remote and eventually lost.
```

This is exactly how a batch of operator-directed ROI corrections, pushed to
`gitea/main` while the swarm ran, was silently dropped: a `submodule sync` (the
swarm runs it every pass) committed on a stale local `main` and forked; ff-only
`pullMain` could not reabsorb the pushed corrections.

## The rule every writer must follow

Any write that lands on `main` MUST, in order:

1. **pull-merge** the remote into the base it authors on (heals/prevents a
   fork; NOT ff-only),
2. **write** the change,
3. **publish to remote** (fetch+merge on non-ff, retry — never force-push),
4. **advance local `main`** so it never lags the remote it just published to,
5. only then **clean up** the worktree/branch.

Two code shapes implement this; use one, never hand-roll `git push` to `main`:

| Write path | Steps 1+2 | Steps 3+4 |
|---|---|---|
| **Worktree publish** (honeybee, editor, resolve) | author in a worktree cut off the freshest `main`; `PublishToMain` fetch+merges the remote in step 3 | `PublishToMain(remote)` pushes; caller then `UpdateLocalMain(ctx)` advances local main (soft: a dirty primary tree only warns — the remote already has it) |
| **Direct-on-primary** (CLI verbs) | `SyncMainFromRemote(ctx, remote)` **before** `CommitPaths` | `PublishPrimaryMain(ctx, remote)` pushes the bump; local main is already advanced (the commit is on the primary) |

`SyncMainFromRemote` and `PublishPrimaryMain` (`internal/git/git.go`) are the
deterministic primitives for the direct-on-primary path; both are no-ops when
`remote == ""` (local-only hive, local main already authoritative). All three
publish sites (honeybee `publish` closure, `editor.publish`, `resolveagent`) now
advance local main after a remote push.

## For agents editing a hive (operator-directed)

- **Never** `git push <remote> HEAD:main` from a worktree and let local `main`
  lag. That is the fork seam. Publish through a beehive path that advances local
  main, or use the editor API (`POST /roi/{name}`, `beehive` editor) which
  authors through beehived's own `PublishToMain` + `UpdateLocalMain`.
- **Never** interleave a direct external push with `beehive submodule sync` on
  the same convergence window — split authorship across the two anchors is the
  antipattern, not "external edits" (coexistence via push/pull is first-class
  *when the invariant holds*).
- Prefer the deterministic `beehive` verbs over ad-hoc `git`; they now carry the
  protocol so you cannot get the ordering wrong.

## Status of the fix

- `submodule sync` — **fixed** (sync-before-author + publish-after).
- `PublishToMain` callers (honeybee/editor/resolve) — **advance local main**.
- `submodule remote`, `plan`, `task`, `instruction update` — still commit on the
  primary without `SyncMainFromRemote`/`PublishPrimaryMain`. Tracked by the
  follow-up task to route every write step through a protocol-carrying `beehive`
  command. Until then, do not run them while local `main` may lag a remote push.

Locked by `internal/git/git_test.go`:
`TestSyncMainFromRemoteHealsFork`, `TestSyncMainFromRemoteNoRemoteIsNoop`,
`TestPublishPrimaryMainPushesAndMergesRace`, `TestPublishPrimaryMainNoRemoteIsNoop`
(plus the pre-existing `TestPullFFOnlyDivergence` proving ff-only cannot cross a
fork).
