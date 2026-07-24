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
    Limit,   \* reject/recover attempts limit; past it a task escalates NEEDS-HUMAN
    Gated    \* TRUE: the handoff gate requires work durable-on-origin before DONE-ward edges

VARIABLES
    status,       \* one of Statuses
    prevStatus,   \* status before the last step (to check edge legality as an invariant)
    attempts,     \* rework attempts; Reject/Strand/RecoverLostWork bump it (state.go)
    workDurable,  \* the task's own bee work is committed AND durable on the submodule origin
    merged,       \* the task's own bee work has been merged into the tracked branch
    workLost      \* adversary flag: the durable work was subsequently lost (branch GC'd, publish never landed)

vars == <<status, prevStatus, attempts, workDurable, merged, workLost>>

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

(***************************************************************************)
(* Safety invariants                                                       *)
(***************************************************************************)

\* Every status change follows a sanctioned edge. (Steps that leave status
\* unchanged -- doing work, losing work -- are exempt.)
LegalTransitionsOnly ==
    (status /= prevStatus) => (<<prevStatus, status>> \in LegalEdges)

\* A task is DONE only when its own work is real: durable on origin AND merged
\* into the tracked branch. This is the anti-false-DONE invariant.
NoFalseDone ==
    (status = "DONE") => (workDurable /\ merged)

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
    /\ UNCHANGED <<status, attempts, merged>>

\* TODO -> NEEDS-REVIEW. The handoff gate: in the fixed protocol the terminal
\* flip is refused unless the work is durable on origin (verify.go +
\* RemoteContainsCommit). Ungated (buggy) it flips regardless.
HandoffToReview ==
    /\ status = "TODO"
    /\ (Gated => workDurable)
    /\ status' = "REVIEW"
    /\ prevStatus' = status
    /\ UNCHANGED <<attempts, workDurable, merged, workLost>>

\* Review approves: merge the bee tip into the tracked branch, NEEDS-REVIEW -> DONE.
\* Gated: refuse to approve work that is not durable on origin (no ambient/phantom
\* false-DONE).
ReviewApprove ==
    /\ status = "REVIEW"
    /\ (Gated => workDurable)
    /\ merged' = TRUE
    /\ status' = "DONE"
    /\ prevStatus' = status
    /\ UNCHANGED <<attempts, workDurable, workLost>>

\* Review rejects: NEEDS-REVIEW -> NEEDS-ARBITRATION (the agent reject edge).
ReviewReject ==
    /\ status = "REVIEW"
    /\ status' = "ARB"
    /\ prevStatus' = status
    /\ UNCHANGED <<attempts, workDurable, merged, workLost>>

\* Arbiter sides with the implementer: merge, NEEDS-ARBITRATION -> DONE.
ArbSideImpl ==
    /\ status = "ARB"
    /\ (Gated => workDurable)
    /\ merged' = TRUE
    /\ status' = "DONE"
    /\ prevStatus' = status
    /\ UNCHANGED <<attempts, workDurable, workLost>>

\* Arbiter sides with the reviewer: rework. NEEDS-ARBITRATION -> TODO, attempts++;
\* once attempts exceed the limit the task escalates NEEDS-HUMAN instead of
\* auto-recycling (state.go Reject semantics).
ArbSideReviewer ==
    /\ status = "ARB"
    /\ attempts' = attempts + 1
    /\ status' = IF attempts + 1 > Limit THEN "HUMAN" ELSE "TODO"
    /\ prevStatus' = status
    /\ UNCHANGED <<workDurable, merged, workLost>>

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
    /\ UNCHANGED <<status, attempts, merged>>

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
    /\ UNCHANGED <<workDurable, merged>>

\* Runner completes interrupted review bookkeeping: the bee work is already merged
\* into tracked main, so finalize NEEDS-REVIEW/ARB -> DONE without a new session.
\* Requires merged, so it never manufactures a false DONE.
FinalizeAlreadyMerged ==
    /\ status \in {"REVIEW", "ARB"}
    /\ merged
    /\ workDurable
    /\ status' = "DONE"
    /\ prevStatus' = status
    /\ UNCHANGED <<attempts, workDurable, merged, workLost>>

\* Honeybee escalates a concrete blocker: any non-DONE working status -> NEEDS-HUMAN.
RequestHuman ==
    /\ status \in {"TODO", "REVIEW", "ARB"}
    /\ status' = "HUMAN"
    /\ prevStatus' = status
    /\ UNCHANGED <<attempts, workDurable, merged, workLost>>

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
    /\ WF_vars(HandoffToReview)
    /\ WF_vars(ReviewApprove)
    /\ WF_vars(ArbSideImpl)
    /\ WF_vars(ArbSideReviewer)
    /\ WF_vars(RecoverLostWork)
    /\ WF_vars(FinalizeAlreadyMerged)

Spec == Init /\ [][Next]_vars /\ Fairness
=============================================================================
