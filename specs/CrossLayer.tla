------------------------------ MODULE CrossLayer ------------------------------
(***************************************************************************)
(* Layer 2 CAPSTONE: the cross-layer composition of TaskStatus.tla's        *)
(* false-DONE gates with DependencyReadiness.tla's dependency-edge          *)
(* readiness.  The two lower specs each prove a property in ISOLATION:       *)
(*                                                                         *)
(*   - TaskStatus.NoFalseDone     -- a task reaches DONE only when its own    *)
(*       work is durable-on-origin AND merged AND its declared DoD Check is   *)
(*       satisfied (the durability gate + the DoD-check gate, 72e2b4a /       *)
(*       92d2ed1).                                                            *)
(*   - DependencyReadiness         -- a dependent unblocks when its blocking   *)
(*       dep is DONE.                                                         *)
(*                                                                         *)
(* Neither alone says anything about what happens WHEN THEY MEET: a task      *)
(* falsely marked DONE (the exact defect TaskStatus forbids) is precisely     *)
(* the STATUS a downstream selector reads to unblock a dependent (exactly     *)
(* the edge DependencyReadiness models).  So a false-DONE does not merely     *)
(* mislabel one task -- it PROPAGATES: the dependent is dispatched and builds *)
(* on phantom / unverified work.  This module composes the two so the         *)
(* downstream damage is itself a checkable property.                          *)
(*                                                                         *)
(* Model: one dependency edge, a PRODUCER task P (the dep) and a CONSUMER     *)
(* task C (the dependent, `deps=<...>:P`).  P runs the false-DONE-capable     *)
(* lifecycle (a distilled TaskStatus: do work -> handoff -> review-approve,   *)
(* each DONE-ward edge guarded by the same two CONSTANT toggles as            *)
(* TaskStatus).  C is a DependencyReadiness dependent: it unblocks the        *)
(* instant it OBSERVES P at DONE (`pStatus = "DONE"`) -- the realistic        *)
(* selector behaviour, which keys off the published status, NOT off P's       *)
(* private durability/merge/DoD facts.  The composed safety property is that  *)
(* the gates on P are exactly what keep C's observed-DONE unlock HONEST.      *)
(*                                                                         *)
(* CONSTANT toggles (compose the two lower gates across the edge):           *)
(*   Gated     = TRUE  -> P's handoff/approve refuse work not durable-on-      *)
(*                        origin+merged (TaskStatus durability gate);          *)
(*               FALSE -> P can reach DONE with non-durable/unmerged work.     *)
(*   CheckGated= TRUE  -> entering DONE requires P's declared DoD Check to     *)
(*                        pass (TaskStatus verifyGate invariant 5);            *)
(*               FALSE -> P can reach DONE with its acceptance bar unmet.      *)
(*                                                                         *)
(* Safety invariant (the capstone):                                          *)
(*   NoPrematureUnlock -- C never becomes READY or DONE on the strength of P  *)
(*     being DONE unless P's work is TRULY durable + merged + DoD-satisfied.   *)
(*     This is the composition proof: it holds on the fixed cfg (both gates    *)
(*     on) and FAILS on each buggy cfg (a gate off produces a false-DONE P     *)
(*     that prematurely unlocks C), showing the durability gate and the DoD-   *)
(*     check gate must BOTH hold, and must hold ACROSS the dependency edge --  *)
(*     not merely on P in isolation.                                          *)
(*   ConsumerNeverPhantom -- C never reaches DONE while P's work is phantom     *)
(*     (not durable): the downstream task never builds on vanished work.       *)
(* Liveness:                                                                  *)
(*   EventuallyBothDone -- with the gates on, P does the real work, reaches    *)
(*     an earned DONE, and C then unblocks and completes; the edge never       *)
(*     wedges.                                                                 *)
(***************************************************************************)
EXTENDS TLC

CONSTANTS
    Gated,      \* TRUE: P's DONE-ward edges require durable-on-origin (+merged) work
    CheckGated  \* TRUE: entering P's DONE requires its declared DoD Check to pass

VARIABLES
    pStatus,        \* producer (dep) status: TODO -> REVIEW -> DONE
    pWorkDurable,   \* P's bee work committed AND durable on the submodule origin
    pMerged,        \* P's bee work merged into the tracked branch
    pCheckDeclared, \* P declares a machine-checkable `Check:` DoD (vs check=none)
    pCheckPassed,   \* P's declared Check currently exits 0 (acceptance bar met)
    cStatus         \* consumer (dependent) status: BLOCKED -> READY -> DONE

vars == <<pStatus, pWorkDurable, pMerged, pCheckDeclared, pCheckPassed, cStatus>>

PStatuses == {"TODO", "REVIEW", "DONE"}
CStatuses == {"BLOCKED", "READY", "DONE"}

\* P's definition-of-done is satisfied iff no check is declared (check=none: the
\* absence is honest, review-scrutinized) or the declared check actually passes.
DodSatisfied == (~pCheckDeclared) \/ pCheckPassed

\* P's DONE is TRULY earned: durable on origin AND merged into the tracked branch
\* AND its declared DoD is satisfied. This is TaskStatus.NoFalseDone's antecedent,
\* lifted here as the honest-unlock predicate the consumer's readiness must respect.
PTrulyDone == pWorkDurable /\ pMerged /\ DodSatisfied

TypeOK ==
    /\ pStatus \in PStatuses
    /\ pWorkDurable \in BOOLEAN
    /\ pMerged \in BOOLEAN
    /\ pCheckDeclared \in BOOLEAN
    /\ pCheckPassed \in BOOLEAN
    /\ cStatus \in CStatuses

(***************************************************************************)
(* The capstone safety invariants.                                          *)
(***************************************************************************)

\* THE composition property: the consumer is never unlocked (READY) or worked
\* to completion (DONE) on the strength of its dep being DONE unless that dep's
\* work is truly durable + merged + DoD-satisfied. A false-DONE P (any gate off)
\* that unlocks C is exactly the counterexample this forbids.
NoPrematureUnlock == (cStatus \in {"READY", "DONE"}) => PTrulyDone

\* Stronger downstream-damage shape: the dependent never actually FINISHES built
\* on phantom (non-durable) upstream work.
ConsumerNeverPhantom == (cStatus = "DONE") => pWorkDurable

(***************************************************************************)
(* Init: P is TODO with nothing done; C is BLOCKED on P. Explore both a       *)
(* check-declared dep and a check=none dep.                                   *)
(***************************************************************************)
Init ==
    /\ pStatus = "TODO"
    /\ pWorkDurable = FALSE
    /\ pMerged = FALSE
    /\ pCheckDeclared \in BOOLEAN
    /\ pCheckPassed = FALSE
    /\ cStatus = "BLOCKED"

(***************************************************************************)
(* Producer actions -- a distilled TaskStatus lifecycle, DONE-ward edges     *)
(* carrying the same two gates.                                              *)
(***************************************************************************)

\* P does its work FIRST: commits bee-<taskid> and pushes it to origin (durable).
PDoWork ==
    /\ pStatus = "TODO"
    /\ ~pWorkDurable
    /\ pWorkDurable' = TRUE
    /\ UNCHANGED <<pStatus, pMerged, pCheckDeclared, pCheckPassed, cStatus>>

\* P meets its acceptance bar so the declared DoD Check now exits 0. Optional and
\* unforced -- P CAN hand off without meeting it (the jellyfin defect); the check
\* gate is what refuses the resulting DONE.
PPassCheck ==
    /\ pStatus \in {"TODO", "REVIEW"}
    /\ pCheckDeclared
    /\ ~pCheckPassed
    /\ pCheckPassed' = TRUE
    /\ UNCHANGED <<pStatus, pWorkDurable, pMerged, pCheckDeclared, cStatus>>

\* P hands off TODO -> NEEDS-REVIEW. Gated: refused unless work is durable-on-origin.
PHandoff ==
    /\ pStatus = "TODO"
    /\ (Gated => pWorkDurable)
    /\ pStatus' = "REVIEW"
    /\ UNCHANGED <<pWorkDurable, pMerged, pCheckDeclared, pCheckPassed, cStatus>>

\* Review approves P: merge the bee tip, NEEDS-REVIEW -> DONE. Gated on durability
\* (no ambient/phantom false-DONE) and, when CheckGated, on the DoD acceptance bar.
PApprove ==
    /\ pStatus = "REVIEW"
    /\ (Gated => pWorkDurable)
    /\ ((CheckGated /\ pCheckDeclared) => pCheckPassed)
    /\ pMerged' = TRUE
    /\ pStatus' = "DONE"
    /\ UNCHANGED <<pWorkDurable, pCheckDeclared, pCheckPassed, cStatus>>

(***************************************************************************)
(* Consumer actions -- a DependencyReadiness dependent keyed on the OBSERVED  *)
(* producer status (what a selector reads), NOT on P's private durability.    *)
(***************************************************************************)

\* C unblocks the instant it observes P at DONE. This is the realistic selector
\* behaviour: `crossDepSatisfied` gates on the dep task's STATUS being DONE. If
\* that DONE is false (a gate off let P reach it unearned), C unlocks prematurely
\* -- the exact downstream damage NoPrematureUnlock catches.
CUnblock ==
    /\ cStatus = "BLOCKED"
    /\ pStatus = "DONE"
    /\ cStatus' = "READY"
    /\ UNCHANGED <<pStatus, pWorkDurable, pMerged, pCheckDeclared, pCheckPassed>>

\* The now-ready dependent is worked to completion (building on P's work).
CProgress ==
    /\ cStatus = "READY"
    /\ cStatus' = "DONE"
    /\ UNCHANGED <<pStatus, pWorkDurable, pMerged, pCheckDeclared, pCheckPassed>>

\* Terminal idle so a fully-settled edge is not read as a deadlock.
Terminal ==
    /\ pStatus = "DONE"
    /\ cStatus = "DONE"
    /\ UNCHANGED vars

Next ==
    \/ PDoWork
    \/ PPassCheck
    \/ PHandoff
    \/ PApprove
    \/ CUnblock
    \/ CProgress
    \/ Terminal

Fairness ==
    /\ WF_vars(PDoWork)
    /\ WF_vars(PPassCheck)
    /\ WF_vars(PHandoff)
    /\ WF_vars(PApprove)
    /\ WF_vars(CUnblock)
    /\ WF_vars(CProgress)

Spec == Init /\ [][Next]_vars /\ Fairness

(***************************************************************************)
(* Liveness (checked in the fixed cfg): with the gates on, P does the real   *)
(* work, reaches an EARNED DONE, and C then unblocks and completes -- the      *)
(* dependency edge never wedges.                                             *)
(***************************************************************************)
EventuallyBothDone == <>(pStatus = "DONE" /\ cStatus = "DONE")

=============================================================================
