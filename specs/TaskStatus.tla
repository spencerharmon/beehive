----------------------------- MODULE TaskStatus -----------------------------
(***************************************************************************)
(* Layer 2 (part a): the task-lifecycle status state machine + the DONE     *)
(* gates.  Faithful to internal/plan/state.go: the legal edges, the         *)
(* attempts/limit escalation to NEEDS-HUMAN, the runner recovery edges       *)
(* (RecoverLostWork, FinalizeAlreadyMerged), and the honeybee/operator       *)
(* escalation edges (RequestHuman, Resolve).                                 *)
(*                                                                         *)
(* The single most important safety property here is NoFalseDone: a task    *)
(* reaches DONE only when its work is REAL -- committed and durable on the   *)
(* submodule origin AND merged into the tracked branch.  The handoff gate    *)
(* (internal/swarm/verify.go) is what enforces this; turning it off          *)
(* reproduces the family of silent false-DONE bugs:                          *)
(*   - fe6da39  uncommitted work landed on an empty bee-branch               *)
(*   - 2573066  NEEDS-REVIEW handoff gated on a COMMITTED change doc          *)
(*   - 72e2b4a  handoff gate verifies commits are durable on ORIGIN          *)
(*   - 743b1c6  reviewed commit read from the task's own bee tip, not the     *)
(*              ambient sibling gitlink (ambient false-DONE)                  *)
(*   - 92d2ed1  DONE entered on a reviewed commit while the task's declared    *)
(*              definition-of-done Check command was NOT satisfied            *)
(*              (jellyfin:zuul-image-build-publish false-DONE)                *)
(*                                                                         *)
(* DoD-check gate (verifyGate invariant 5, 92d2ed1): entering DONE by ANY     *)
(* path -- review approve, arbitration, interrupted-review finalize -- runs   *)
(* the task's declared `Check:` command and REFUSES the DONE unless it        *)
(* passes.  A `check=none` task declared a justified absence and is not gated. *)
(* So NoFalseDone strengthens: durable + merged is necessary but NOT          *)
(* sufficient; a declared check must also be satisfied.                       *)
(*                                                                         *)
(* Liveness: a NEEDS-REVIEW/NEEDS-ARBITRATION task whose work is lost         *)
(* everywhere eventually returns to TODO or escalates NEEDS-HUMAN -- it       *)
(* never strands forever at a phantom commit (4fdd953, 743c46f).  And the     *)
(* attempts/limit escalation guarantees the task always terminates           *)
(* (reaches DONE or NEEDS-HUMAN) rather than looping rework forever.          *)
(*                                                                         *)
(* Concurrency (the claim race between two sessions) is the companion         *)
(* module ClaimRace.tla; this module models a single worker at a time and     *)
(* focuses on edge legality + the gates + recovery.                          *)
(*                                                                         *)
(* CONSTANT Gated selects the handoff-gate-enforced protocol (TRUE) vs the    *)
(* ungated pre-fix runner (FALSE).  See the .cfg files.                      *)
(***************************************************************************)
EXTENDS Naturals, TLC

CONSTANTS
    Limit,      \* reject/recover attempts limit; past it a task escalates NEEDS-HUMAN
    Gated,      \* TRUE: the handoff gate requires work durable-on-origin before DONE-ward edges
    CheckGated  \* TRUE: entering DONE requires the task's declared DoD Check to pass (verifyGate inv 5)

VARIABLES
    status,       \* one of Statuses
    prevStatus,   \* status before the last step (to check edge legality as an invariant)
    attempts,     \* rework attempts; Reject/Strand/RecoverLostWork bump it (state.go)
    workDurable,  \* the task's own bee work is committed AND durable on the submodule origin
    merged,       \* the task's own bee work has been merged into the tracked branch
    workLost,     \* adversary flag: the durable work was subsequently lost (branch GC'd, publish never landed)
    checkDeclared,\* the task declares a machine-checkable `Check:` DoD command (vs check=none/undeclared)
    checkPassed   \* the declared Check currently exits 0 (the acceptance bar is actually met)

vars == <<status, prevStatus, attempts, workDurable, merged, workLost,
          checkDeclared, checkPassed>>

Statuses == {"TODO", "REVIEW", "ARB", "DONE", "HUMAN"}

\* The legal status edges. Agent edges (Transition, state.go:14-16):
\*   TODO->REVIEW, REVIEW->{DONE,ARB}, ARB->{TODO,DONE}.
\* Runner/operator edges (Reject/Strand/RecoverLostWork/BounceUnreachable/
\* FinalizeAlreadyMerged/RequestHuman/Resolve):
\*   REVIEW->{TODO,HUMAN,ARB}, ARB->{TODO,HUMAN,DONE}, TODO->HUMAN, HUMAN->TODO.
LegalEdges ==
    { <<"TODO","REVIEW">>, <<"REVIEW","DONE">>, <<"REVIEW","ARB">>,
      <<"ARB","TODO">>, <<"ARB","DONE">>,
      <<"REVIEW","TODO">>, <<"REVIEW","HUMAN">>,
      <<"ARB","HUMAN">>, <<"TODO","HUMAN">>,
      <<"HUMAN","TODO">> }

TypeOK ==
    /\ status \in Statuses
    /\ prevStatus \in Statuses
    /\ attempts \in 0..(Limit + 1)
    /\ workDurable \in BOOLEAN
    /\ merged \in BOOLEAN
    /\ workLost \in BOOLEAN
    /\ checkDeclared \in BOOLEAN
    /\ checkPassed \in BOOLEAN

\* The definition-of-done is satisfied iff no check is declared (check=none: the
\* absence is honest and review-scrutinized) or the declared check actually passes.
DodSatisfied == (~checkDeclared) \/ checkPassed

(***************************************************************************)
(* Safety invariants                                                       *)
(***************************************************************************)

\* Every status change follows a sanctioned edge. (Steps that leave status
\* unchanged -- doing work, losing work -- are exempt.)
LegalTransitionsOnly ==
    (status /= prevStatus) => (<<prevStatus, status>> \in LegalEdges)

\* A task is DONE only when its own work is real: durable on origin AND merged
\* into the tracked branch AND (92d2ed1) its declared definition-of-done Check is
\* satisfied. This is the anti-false-DONE invariant.
NoFalseDone ==
    (status = "DONE") => (workDurable /\ merged /\ DodSatisfied)

\* The attempts counter never runs away past the escalation point.
AttemptsBounded == attempts <= Limit + 1

(***************************************************************************)
(* Init                                                                    *)
(***************************************************************************)
Init ==
    /\ status = "TODO"
    /\ prevStatus = "TODO"
    /\ attempts = 0
    /\ workDurable = FALSE
    /\ merged = FALSE
    /\ workLost = FALSE
    /\ checkDeclared \in BOOLEAN   \* explore both a check-declared task and a check=none task
    /\ checkPassed = FALSE         \* the acceptance bar starts unmet

(***************************************************************************)
(* Actions. Every action sets prevStatus' = status so the edge just taken   *)
(* is checkable by LegalTransitionsOnly in the post-state.                  *)
(***************************************************************************)

\* The Work agent does the work FIRST: commits bee-<taskid> and pushes it to the
\* submodule origin (durable). Status stays TODO until the handoff.
DoWork ==
    /\ status = "TODO"
    /\ ~workDurable
    /\ workDurable' = TRUE
    /\ workLost' = FALSE
    /\ prevStatus' = status
    /\ UNCHANGED <<status, attempts, merged, checkDeclared, checkPassed>>

\* The agent meets the acceptance bar: it does the real work so the declared DoD
\* Check command now exits 0. Optional and unforced -- an agent CAN hand off
\* without meeting it (the jellyfin defect); the check gate is what refuses the
\* resulting DONE. Enabled while the task is still being worked/reviewed.
PassCheck ==
    /\ status \in {"TODO", "REVIEW", "ARB"}
    /\ checkDeclared
    /\ ~checkPassed
    /\ checkPassed' = TRUE
    /\ prevStatus' = status
    /\ UNCHANGED <<status, attempts, workDurable, merged, workLost, checkDeclared>>

\* TODO -> NEEDS-REVIEW. The handoff gate: in the fixed protocol the terminal
\* flip is refused unless the work is durable on origin (verify.go +
\* RemoteContainsCommit). Ungated (buggy) it flips regardless.
HandoffToReview ==
    /\ status = "TODO"
    /\ (Gated => workDurable)
    /\ status' = "REVIEW"
    /\ prevStatus' = status
    /\ UNCHANGED <<attempts, workDurable, merged, workLost, checkDeclared, checkPassed>>

\* Review approves: merge the bee tip into the tracked branch, NEEDS-REVIEW -> DONE.
\* Gated: refuse to approve work that is not durable on origin (no ambient/phantom
\* false-DONE). CheckGated (verifyGate inv 5): refuse to approve a declared-check
\* task whose acceptance bar is not met.
ReviewApprove ==
    /\ status = "REVIEW"
    /\ (Gated => workDurable)
    /\ ((CheckGated /\ checkDeclared) => checkPassed)
    /\ merged' = TRUE
    /\ status' = "DONE"
    /\ prevStatus' = status
    /\ UNCHANGED <<attempts, workDurable, workLost, checkDeclared, checkPassed>>

\* Review rejects: NEEDS-REVIEW -> NEEDS-ARBITRATION (the agent reject edge).
ReviewReject ==
    /\ status = "REVIEW"
    /\ status' = "ARB"
    /\ prevStatus' = status
    /\ UNCHANGED <<attempts, workDurable, merged, workLost, checkDeclared, checkPassed>>

\* Arbiter sides with the implementer: merge, NEEDS-ARBITRATION -> DONE. Also
\* gated on the DoD check (the gate covers DONE entered via arbitration too).
ArbSideImpl ==
    /\ status = "ARB"
    /\ (Gated => workDurable)
    /\ ((CheckGated /\ checkDeclared) => checkPassed)
    /\ merged' = TRUE
    /\ status' = "DONE"
    /\ prevStatus' = status
    /\ UNCHANGED <<attempts, workDurable, workLost, checkDeclared, checkPassed>>

\* Arbiter sides with the reviewer: rework. NEEDS-ARBITRATION -> TODO, attempts++;
\* once attempts exceed the limit the task escalates NEEDS-HUMAN instead of
\* auto-recycling (state.go Reject semantics).
ArbSideReviewer ==
    /\ status = "ARB"
    /\ attempts' = attempts + 1
    /\ status' = IF attempts + 1 > Limit THEN "HUMAN" ELSE "TODO"
    /\ prevStatus' = status
    /\ UNCHANGED <<workDurable, merged, workLost, checkDeclared, checkPassed>>

\* Adversary: the durable work is subsequently lost -- the bee branch was reclaimed
\* / GC'd and the publish never landed, so what looked reviewable now points at a
\* phantom commit. Enabled only while under review/arbitration and not yet merged.
LoseWork ==
    /\ status \in {"REVIEW", "ARB"}
    /\ workDurable
    /\ ~merged
    /\ workDurable' = FALSE
    /\ workLost' = TRUE
    /\ prevStatus' = status
    /\ UNCHANGED <<status, attempts, merged, checkDeclared, checkPassed>>

\* Runner recovers a task whose work is unrecoverable everywhere: reset to TODO
\* (attempts++, past limit -> NEEDS-HUMAN). Valid from REVIEW or ARB (state.go
\* RecoverLostWork). This is the self-heal that stops a phantom-commit strand.
RecoverLostWork ==
    /\ status \in {"REVIEW", "ARB"}
    /\ ~workDurable
    /\ ~merged
    /\ attempts' = attempts + 1
    /\ status' = IF attempts + 1 > Limit THEN "HUMAN" ELSE "TODO"
    /\ workLost' = FALSE
    /\ prevStatus' = status
    /\ UNCHANGED <<workDurable, merged, checkDeclared, checkPassed>>

\* Runner completes interrupted review bookkeeping: the bee work is already merged
\* into tracked main, so finalize NEEDS-REVIEW/ARB -> DONE without a new session.
\* Requires merged, so the work is real; and (verifyGate inv 5) the DoD check must
\* be satisfied -- the interrupted-review finalize is exactly the path the jellyfin
\* false-DONE walked through, so it is gated on the check too.
FinalizeAlreadyMerged ==
    /\ status \in {"REVIEW", "ARB"}
    /\ merged
    /\ workDurable
    /\ ((CheckGated /\ checkDeclared) => checkPassed)
    /\ status' = "DONE"
    /\ prevStatus' = status
    /\ UNCHANGED <<attempts, workDurable, merged, workLost, checkDeclared, checkPassed>>

\* Honeybee escalates a concrete blocker: any non-DONE working status -> NEEDS-HUMAN.
RequestHuman ==
    /\ status \in {"TODO", "REVIEW", "ARB"}
    /\ status' = "HUMAN"
    /\ prevStatus' = status
    /\ UNCHANGED <<attempts, workDurable, merged, workLost, checkDeclared, checkPassed>>

\* NOTE: the operator Resolve edge NEEDS-HUMAN -> TODO (state.go Resolve) is a
\* legal edge (kept in LegalEdges) but is deliberately NOT an action here: it is
\* an out-of-band operator action, outside the autonomous protocol's liveness
\* scope. Modeling it would make NEEDS-HUMAN non-terminal and let a resolve/retry
\* loop grow attempts without bound (Resolve does not reset attempts in the code) --
\* realistic, but not a property of the autonomous machine. Within this module
\* NEEDS-HUMAN is terminal, which is exactly the selector's own view (a NEEDS-HUMAN
\* task is excluded from selection until an operator reopens it).

\* Terminal idle so a completed task does not read as a deadlock.
Done ==
    /\ status \in {"DONE", "HUMAN"}
    /\ UNCHANGED vars

Next ==
    \/ DoWork
    \/ PassCheck
    \/ HandoffToReview
    \/ ReviewApprove
    \/ ReviewReject
    \/ ArbSideImpl
    \/ ArbSideReviewer
    \/ LoseWork
    \/ RecoverLostWork
    \/ FinalizeAlreadyMerged
    \/ RequestHuman
    \/ Done

(***************************************************************************)
(* Liveness (checked in the fixed cfg):                                     *)
(*  - the task always terminates (DONE or NEEDS-HUMAN);                      *)
(*  - lost work never strands -- it leads back to TODO or NEEDS-HUMAN.       *)
(* Fairness on the progress + recovery edges; the LoseWork adversary and the *)
(* operator Resolve edge are deliberately NOT forced.                        *)
(***************************************************************************)
Terminates == <>(status \in {"DONE", "HUMAN"})

LostWorkRecovers ==
    (status \in {"REVIEW", "ARB"} /\ ~workDurable /\ ~merged)
        ~> (status \in {"TODO", "HUMAN"})

Fairness ==
    /\ WF_vars(DoWork)
    /\ WF_vars(PassCheck)
    /\ WF_vars(HandoffToReview)
    /\ WF_vars(ReviewApprove)
    /\ WF_vars(ArbSideImpl)
    /\ WF_vars(ArbSideReviewer)
    /\ WF_vars(RecoverLostWork)
    /\ WF_vars(FinalizeAlreadyMerged)

Spec == Init /\ [][Next]_vars /\ Fairness
=============================================================================
