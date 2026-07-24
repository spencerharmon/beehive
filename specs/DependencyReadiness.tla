-------------------------- MODULE DependencyReadiness -------------------------
(***************************************************************************)
(* Layer 2 (part c): dependency readiness and the dangling-dependency         *)
(* refusal (92d2ed1, docs/dod-verification-spec.md).  A work pass that leaves  *)
(* its task TODO because a blocking dependency is not yet DONE is a            *)
(* legitimate, complete yield -- the selector holds the task until the dep is  *)
(* DONE (or a not_before elapses).  But a dependency that names NO real task   *)
(* can never become DONE, so treating it as "not DONE -> blocked" wedges the   *)
(* task forever while masquerading as a clean dep-yield.  That is the defect   *)
(* that stranded flux:phantom-library-bluegreen-repin-gitea-images on the      *)
(* nonexistent jellyfin:jellyfin-image-build.                                  *)
(*                                                                         *)
(* Fix (internal/swarm/swarm.go taskYieldedBlocked): a yield is accepted ONLY  *)
(* if every blocking dep is a real, existing task (local plan / cross link     *)
(* graph).  A phantom dep fails the pass LOUD (an error, never a silent        *)
(* complete), so the task escalates instead of wedging.                        *)
(*                                                                         *)
(* CONSTANT DepGuard selects the phantom-dep-refusing protocol (TRUE) vs the   *)
(* pre-fix runner that silently accepts any blocked yield (FALSE).            *)
(*                                                                         *)
(* Properties:                                                               *)
(*   HeldImpliesRealDep (safety)  -- a held (accepted-yield) task always has a  *)
(*     real dependency; a phantom dep is never silently held.                  *)
(*   EventuallyResolved (liveness) -- the task never wedges forever: it always  *)
(*     reaches DONE (its real dep completed and it finished) or NEEDS-HUMAN     *)
(*     (a phantom dep failed the pass loud and escalated).                     *)
(***************************************************************************)
EXTENDS TLC

CONSTANT DepGuard  \* TRUE: a blocked yield is accepted only when the blocking dep is real

VARIABLES
    status,   \* TODO | HELD (yielded, awaiting dep) | READY (dep satisfied) | DONE | HUMAN
    depReal,  \* the blocking dependency names a real, existing task
    depDone   \* the real dependency has reached DONE

vars == <<status, depReal, depDone>>

Statuses == {"TODO", "HELD", "READY", "DONE", "HUMAN"}

TypeOK ==
    /\ status \in Statuses
    /\ depReal \in BOOLEAN
    /\ depDone \in BOOLEAN

(***************************************************************************)
(* Invariants                                                              *)
(***************************************************************************)

\* A task held out of selection on a dep-yield always has a REAL dependency --
\* a phantom dep is never accepted as a legitimate blocked yield.
HeldImpliesRealDep == (status = "HELD") => depReal

(***************************************************************************)
(* Init: the task is TODO with a single blocking dependency that is either a  *)
(* real task or a phantom (nonexistent) id -- both explored.                  *)
(***************************************************************************)
Init ==
    /\ status = "TODO"
    /\ depReal \in BOOLEAN
    /\ depDone = FALSE

(***************************************************************************)
(* Actions                                                                 *)
(***************************************************************************)

\* The work pass runs and finds the task not ready (its blocking dep is not DONE),
\* so it yields. taskYieldedBlocked decides how:
\*   fixed  (DepGuard): a REAL dep -> a legitimate yield, task HELD; a PHANTOM dep
\*     -> fail LOUD, the pass escalates the task to NEEDS-HUMAN rather than holding
\*     it on a dependency that can never appear.
\*   buggy: any blocked yield is accepted -> task HELD regardless (a phantom dep is
\*     silently held forever).
Yield ==
    /\ status = "TODO"
    /\ ~depDone
    /\ status' = IF (DepGuard /\ ~depReal) THEN "HUMAN" ELSE "HELD"
    /\ UNCHANGED <<depReal, depDone>>

\* A real dependency is completed by its own pass.
DepCompletes ==
    /\ depReal
    /\ ~depDone
    /\ depDone' = TRUE
    /\ UNCHANGED <<status, depReal>>

\* The dependency was already DONE when the task was selected (it completed before
\* this task's pass ran), so the task is worked straight through -- no yield needed.
ReadyFromTodo ==
    /\ status = "TODO"
    /\ depDone
    /\ status' = "DONE"
    /\ UNCHANGED <<depReal, depDone>>

\* The held task's dependency is now DONE, so the selector releases it back into
\* the ready pool.
Unblock ==
    /\ status = "HELD"
    /\ depDone
    /\ status' = "READY"
    /\ UNCHANGED <<depReal, depDone>>

\* The now-ready task is worked to completion.
Progress ==
    /\ status = "READY"
    /\ status' = "DONE"
    /\ UNCHANGED <<depReal, depDone>>

\* Terminal idle so a completed/escalated task is not read as a deadlock.
Done ==
    /\ status \in {"DONE", "HUMAN"}
    /\ UNCHANGED vars

Next ==
    \/ Yield
    \/ DepCompletes
    \/ ReadyFromTodo
    \/ Unblock
    \/ Progress
    \/ Done

(***************************************************************************)
(* Liveness: the task never wedges forever.                                 *)
(***************************************************************************)
EventuallyResolved == <>(status \in {"DONE", "HUMAN"})

Fairness ==
    /\ WF_vars(Yield)
    /\ WF_vars(DepCompletes)
    /\ WF_vars(ReadyFromTodo)
    /\ WF_vars(Unblock)
    /\ WF_vars(Progress)

Spec == Init /\ [][Next]_vars /\ Fairness
=============================================================================
