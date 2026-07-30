--------------------------- MODULE TaskRemovalRace ---------------------------
(***************************************************************************)
(* Layer 2: a work pass holding a task races a concurrent removal of that   *)
(* task (or of the whole PLAN.md) from `main` — by a reconcile pass or an    *)
(* operator. Faithful to internal/swarm/swarm.go's guards:                   *)
(*                                                                         *)
(*   - `taskRemoved` (swarm.go:1657, called at swarm.go:1159) re-reads       *)
(*     main's PLAN each turn; if the plan was deleted (":1686 …no longer     *)
(*     exists. Exiting.") or the task vanished (":1695 …was removed… "),     *)
(*     it ends the pass rather than working / publishing a task nobody       *)
(*     wants. This is the presence RE-CHECK before any status write lands.   *)
(*                                                                         *)
(*   - the durable-record already-merged guards (`finalizeIfMergedByRecord`  *)
(*     ~:1682, `finalizeIfAlreadyMerged` ~:2680) exist because the naive     *)
(*     recoverIfLost heuristic MISREADS a vanished bee-<taskid> branch as    *)
(*     "lost work" and RE-LOOPS the task forever. The fixed protocol exits   *)
(*     cleanly on a vanished task instead of looping.                        *)
(*                                                                         *)
(* Two historical failure classes are proven absent:                        *)
(*                                                                         *)
(*   NoOrphanWrite (safety) — no status write / DONE commit for a task that  *)
(*     is absent from main's current PLAN ever lands. The removal and the    *)
(*     in-flight publish race; the fixed Publish re-checks presence (models  *)
(*     `taskRemoved`) so it is disabled once the task is gone.               *)
(*                                                                         *)
(*   VanishedPassTerminates (liveness) — a pass whose task vanished reaches  *)
(*     a terminal state (clean exit) rather than looping the task forever.   *)
(*                                                                         *)
(* CONSTANT Fixed selects the guarded runner (TRUE: re-check presence        *)
(* before publish; exit cleanly on a vanished task) vs the buggy runner      *)
(* (FALSE: publish without re-checking → orphan write; treat the vanished    *)
(* branch as lost work → re-loop). See the .cfg files.                       *)
(***************************************************************************)
EXTENDS Naturals, TLC

CONSTANTS
    MaxLoops,   \* bound on the lost-work re-loop count (keeps the state space finite)
    Fixed       \* TRUE: presence re-check + clean vanished-exit; FALSE: pre-fix runner

VARIABLES
    taskPresent,   \* TRUE while the task is still in main's current PLAN snapshot
    passState,     \* "working" | "landed" | "terminated"
    orphanWrite,   \* TRUE once a status write for an ABSENT task has landed on main
    loopCount      \* number of times a vanished pass re-looped (lost-work misread)

vars == <<taskPresent, passState, orphanWrite, loopCount>>

TypeOK ==
    /\ taskPresent \in BOOLEAN
    /\ passState \in {"working", "landed", "terminated"}
    /\ orphanWrite \in BOOLEAN
    /\ loopCount \in 0..MaxLoops

(***************************************************************************)
(* Safety: a status write never lands for a task absent from main's PLAN.   *)
(***************************************************************************)
NoOrphanWrite == ~orphanWrite

(***************************************************************************)
(* Init                                                                    *)
(***************************************************************************)
Init ==
    /\ taskPresent = TRUE
    /\ passState = "working"
    /\ orphanWrite = FALSE
    /\ loopCount = 0

(***************************************************************************)
(* Actions                                                                 *)
(***************************************************************************)

\* A concurrent reconcile pass or operator removes this task (or the whole
\* PLAN.md) from main while the work pass is still executing.
Remove ==
    /\ passState = "working"
    /\ taskPresent = TRUE
    /\ taskPresent' = FALSE
    /\ UNCHANGED <<passState, orphanWrite, loopCount>>

\* FIXED publish: models the `taskRemoved` re-check at the top of the turn —
\* the pass re-reads main's PLAN and only lands its status write if the task is
\* still present. Disabled once the task has vanished, so no orphan write.
PublishSafe ==
    /\ Fixed
    /\ passState = "working"
    /\ taskPresent = TRUE
    /\ passState' = "landed"
    /\ UNCHANGED <<taskPresent, orphanWrite, loopCount>>

\* BUGGY publish: the pre-fix runner writes its status without re-checking
\* presence. If the task has already vanished, the write is an ORPHAN.
PublishUnsafe ==
    /\ ~Fixed
    /\ passState = "working"
    /\ passState' = "landed"
    /\ orphanWrite' = (taskPresent = FALSE)
    /\ UNCHANGED <<taskPresent, loopCount>>

\* FIXED vanished-exit: the guard finds the task gone and ends the pass cleanly
\* (models the ":1686/:1695 … Exiting." branches, and the durable-record
\* finalize guards that stop recoverIfLost from misreading the vanished branch).
TerminateVanished ==
    /\ Fixed
    /\ passState = "working"
    /\ taskPresent = FALSE
    /\ passState' = "terminated"
    /\ UNCHANGED <<taskPresent, orphanWrite, loopCount>>

\* BUGGY re-loop: the naive recoverIfLost heuristic misreads the vanished
\* branch/claim as "lost work" and re-dispatches the task instead of exiting —
\* the pass never terminates.
ReLoopVanished ==
    /\ ~Fixed
    /\ passState = "working"
    /\ taskPresent = FALSE
    /\ loopCount' = IF loopCount < MaxLoops THEN loopCount + 1 ELSE loopCount
    /\ UNCHANGED <<taskPresent, passState, orphanWrite>>

\* Terminal idle once the pass has settled.
Done ==
    /\ passState \in {"landed", "terminated"}
    /\ UNCHANGED vars

Next ==
    \/ Remove
    \/ PublishSafe
    \/ PublishUnsafe
    \/ TerminateVanished
    \/ ReLoopVanished
    \/ Done

(***************************************************************************)
(* Liveness: a pass whose task vanished eventually terminates (never loops  *)
(* forever). Fairness is attached ONLY to the fixed actions, so the buggy    *)
(* ReLoopVanished behavior — spinning forever without ever terminating —     *)
(* is an admitted behavior that violates the property.                       *)
(***************************************************************************)
VanishedPassTerminates == (taskPresent = FALSE) ~> (passState = "terminated")

Fairness ==
    /\ WF_vars(PublishSafe)
    /\ WF_vars(TerminateVanished)

Spec == Init /\ [][Next]_vars /\ Fairness
=============================================================================
