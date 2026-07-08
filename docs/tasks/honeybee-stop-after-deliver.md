# honeybee-stop-after-deliver: stop once the completion predicate is met

## Problem

A honeybee that has already met its completion predicate — deliverable written and
committed, terminal status set, change doc present — sometimes keeps going with
trailing "housekeeping" (cleanup of scratch files, re-verification, "one last check",
out-of-repo reads) until it stalls on the per-turn idle timeout. The transcript then
ends on a genuine `## ⚠️ warning turn N made no progress` and the engine flags the
session aborted / completion_miss **even though the work was fully delivered**.

Confirmed live (session-audit-012, Finding #1, ranked #1):

- session-audit-011's own work session committed its audit, self-declared done, then
  ran trailing `find` / `rm -f` / `rm -rf` cleanup of its scratch binary and `/tmp`
  dir, stalled, and ended the file on the idle-timeout warning → flagged aborted.
- The rebuild-recipe review 1783492267 stalled re-reading the out-of-repo
  `~/.local/bin/beehive-rebuild` and produced no verdict → recycled.

This poisons the very ledger the session-audit series mines: a delivered task is
scored as a failure, and a downstream reconcile pass burned a whole session doing
nothing but un-poisoning the false flags.

Root cause: `HONEYBEE.md` had no kind-general "STOP once the completion predicate is
met" instruction. The deterministic runner already owns cleanup/teardown (it tears
the worktree down, streams the transcript, reclaims the branch), so agent-side
trailing work is pure downside.

## Change

`prompts/HONEYBEE.md`, `## Turn loop` — added a kind-general clause: STOP the instant
your role section's completion predicate is met; end the turn and emit nothing
further. It applies to every kind (reconcile, work, review, arbitration). It does NOT
relax any completion predicate — the agent still delivers in full first (deliverable
committed; for a work task the code pushed on `bee-<taskid>`, the pointer bumped, and
the change doc present at its exact path, with the terminal status set). After that:
no cleanup of scratch files / `$TMPDIR` (the runner tears the worktree down), no
re-verification, no "one last check", no out-of-repo reads. The cost is named
explicitly: post-delivery the turn has no productive move left, so trailing work only
stalls on the per-turn idle timeout, the transcript ends on a "made no progress"
warning, and the engine misflags the already-finished session aborted/completion_miss
— poisoning the ledger later audits mine.

The `## Turn loop` section is the natural home: it is the kind-general section that
already describes completion ("Met → you exit"), so the STOP clause reads as its
direct continuation and is seen by every kind.

## Why only a prompt edit

The lever is injection/preamble — this is a `prompts/HONEYBEE.md` token-cost fix, one
of the named tokens-per-honeybee levers. No runner/engine code changes: the runner
already owns cleanup and the deterministic completion check, so the fix is purely to
stop the agent adding trailing turns. No completion predicate is touched, so no code
path that reads terminal status / doc presence changes.

## Acceptance mapping

- *kind-general instruction to stop as soon as the completion predicate is met* →
  the `## Turn loop` clause, explicitly listing reconcile/work/review/arbitration.
- *do not perform trailing cleanup / re-verification / out-of-repo reads after
  delivery* → the "do NOT tack on trailing housekeeping" sentence enumerating scratch
  cleanup, re-verification, "one last check", out-of-repo reads.
- *name the idle-timeout misflag as the cost* → the closing sentence naming the
  per-turn idle timeout, the "made no progress" warning, and the
  aborted/completion_miss misflag against already-finished work.
- *no completion predicate is weakened* → the clause states it "relaxes NO predicate
  above: deliver in FULL first" and changes no kind's role/completion section.
- *future audit pass can confirm delivered sessions no longer end on a post-delivery
  idle-timeout warning* → behavioral: with the clause, a bee ends its turn at
  delivery rather than adding stalling trailing turns.
