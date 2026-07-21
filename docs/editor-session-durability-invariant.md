# Editor session durability invariant

**A live or pending chat-diff editor session's durable state — its worktree, its
branch (local AND on a trusted remote), and its persisted store record — MUST
NEVER be destroyed by any automatic beehive process while the session is still
in use.**

Losing it mid-chat surfaces to the operator as a `404 ("no such session")` on
the very next turn and throws away their in-flight edit, their conversation, and
the tokens spent producing them. This is a correctness invariant, not a
nice-to-have: one silent reclaim eats real operator work.

## The shared `.worktrees/` namespace

Three subsystems cut per-edit worktrees into the SAME beehive-root `.worktrees/`
directory, and their branch names all begin with `edit`:

| Subsystem | Source | Branch prefix | Persistence |
|-----------|--------|---------------|-------------|
| Coordination-file editor | `internal/editor` | `hive-edit-` | store + Reload recovery + optional remote push |
| Blocker-resolution agent | `internal/web/resolveagent.go` | `edit-resolve-` | in-memory only (proposal committed per turn) |
| Bootstrap chat editor | `internal/web/chatedit.go` | `edit-` | in-memory only (proposal held in memory) |

`internal/editor`'s `Manager` is the only one with a startup/gc reclaim (it
enumerates worktrees + a trusted remote's branches and removes the stale, clean
ones). Before the editor-session-wipe fix it enumerated the **bare `edit-`
prefix**, so it treated the other two subsystems' worktrees as its own. That was
catastrophic in two ways:

1. **Mis-adoption.** A record-less foreign worktree carrying a committed change
   was "recovered" into the editor store as a bogus editor session (observed in
   the wild: `editor-sessions.json` full of `edit-resolve-*` records pointing at
   ROI files those resolve sessions never edited).
2. **Live deletion.** A foreign worktree that momentarily looked *clean* — a
   Q&A-only turn, or a proposal still held in memory and not yet committed — was
   judged "abandoned" and had its worktree, local branch, and (with a trusted
   remote) its pushed remote branch deleted, wiping another subsystem's live
   in-flight edit.

## The two rules that enforce the invariant

Both live in `internal/editor/editor.go` (see the `INVARIANT` comment block and
`editBranchPrefix`).

1. **Namespace ownership.** The `Manager` owns EXACTLY the branches it creates —
   those under `editBranchPrefix` (`hive-edit-`) — and every enumeration
   (`isEditBranch`, the local worktree scan, the trusted-remote branch glob) is
   scoped to that prefix. It never adopts, reclaims, or remote-deletes a foreign
   `edit-*` / `edit-resolve-*` branch.

2. **Never reclaim a live session.** A branch currently registered in
   `Manager.byID` is a live, in-memory session (an operator has it open) and is
   NEVER reclaimed, whatever its record age or working-tree cleanliness.
   Startup `Reload` runs on an empty `byID` so this is a no-op there; the guard
   matters for the `gc` dance, which runs `Reclaimable` + `Reload` on a LIVE
   daemon and would otherwise delete an operator's open-but-idle session once
   its record aged past the TTL.

## Consequence for the other subsystems

Because the editor `Manager` no longer sweeps foreign `edit-*` worktrees, the
resolve agent and bootstrap chat editor own their own teardown (they already
remove their worktree+branch on Discard/Publish). A worktree one of them leaks
via a crash is swept by the manual `cleanup-stale` dance
(`web.staleWorktrees`, which is registration-based and only removes worktree
dirs with no live git worktree), never by an automatic process that could race a
live session. Trading a rare, cheap leaked worktree for never deleting live work
is the correct exchange.
