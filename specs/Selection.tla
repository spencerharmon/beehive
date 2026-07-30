------------------------------ MODULE Selection ------------------------------
(***************************************************************************)
(* Layer 2 (part c): the deterministic task-SELECTION layer                    *)
(* (internal/select/select.go `Select` + `weightedOrder`, and the wall-clock    *)
(* `not_before` gate `plan.Task.NotBeforeReached` / `plan.Plan.Candidates`).    *)
(* It extends the selection dimension that `ClaimRace.tla` only touches from     *)
(* the claim side: ClaimRace proves two sessions never both dispatch on one      *)
(* live task; THIS module proves the selector's two OTHER obligations, each a    *)
(* distinct historical/near-miss failure class:                                  *)
(*                                                                         *)
(*   STARVATION (a LIVENESS failure) -- a task that is continuously READY (its   *)
(*     deps satisfied and its `not_before` gate reached) is passed over forever   *)
(*     and never selected.  The selector picks weighted-random over the ready     *)
(*     pool (`weightedOrder` shuffles each submodule repeated by its weight;      *)
(*     `pickTask` weighted-random over `Candidates`); the design correctness      *)
(*     that makes this safe is that EVERY ready task carries positive weight, so   *)
(*     over an unbounded run each ready task is selected with probability 1 --     *)
(*     modeled here as per-task weak fairness on `Select`.  The buggy variant      *)
(*     drops that guarantee (a task effectively excluded / zero-probability /      *)
(*     an ordering that always defers it), so a ready task starves.                *)
(*                                                                         *)
(*   PREMATURE DISPATCH (a SAFETY failure) -- a task whose `not_before` is in     *)
(*     the FUTURE is selected before its wall clock arrives.  `Candidates` gates   *)
(*     a TODO task on `t.NotBeforeReached(now)` (plan/compat.go:42,                *)
(*     plan/state.go:75) exactly as it gates on an unmet dep; the buggy variant    *)
(*     ignores the gate and dispatches early.                                      *)
(*                                                                         *)
(* Fidelity notes:                                                               *)
(*   - `clock` is a logical wall clock (0..MaxClock), the same abstraction        *)
(*     ClaimRace uses; `not_before` is a per-task threshold on it.  A task is      *)
(*     READY once `clock >= NotBefore[t]` (NotBefore = 0 means no gate).          *)
(*   - SELECTION TTL: a selected task is CLAIMED and thus not re-selected while    *)
(*     its claim is live; `Release` models the claim's TTL expiring / the task     *)
(*     returning to the pool -- the continuous stream of work over which the       *)
(*     starvation guarantee must hold (a task selected once and requeued must not   *)
(*     crowd out a peer that has never been selected).  This is the raw claim TTL   *)
(*     that `plan.Task.Active` / `Candidates` use (the same threshold ClaimRace's   *)
(*     `SelStale` abstracts).                                                       *)
(*   - Weighting itself is abstracted to per-task fairness: what matters for the   *)
(*     failure class is not the exact weight but whether a ready task has a         *)
(*     GUARANTEED eventual turn.  `FairSelect = TRUE` grants it (positive weight     *)
(*     for every candidate); `FALSE` withholds it (the starvation bug).             *)
(*                                                                         *)
(* CONSTANT toggles select buggy vs fixed independently (see the .cfg files):     *)
(*   FairSelect  = TRUE  -> every continuously-ready task is eventually selected   *)
(*                          (weighted-random over positive weights; NO starvation);*)
(*                 FALSE -> a ready task may be starved forever (buggy).           *)
(*   GateHonored = TRUE  -> a task is selected only at/after its `not_before`      *)
(*                          (fixed: no premature dispatch);                        *)
(*                 FALSE -> a future-gated task may be dispatched early (buggy).    *)
(*                                                                         *)
(* Properties:                                                                   *)
(*   NoPrematureDispatch (safety invariant) -- no task is ever selected before    *)
(*     its `not_before` gate is reached.                                          *)
(*   EventuallySelected  (liveness / NoStarvation) -- every task that is          *)
(*     eventually ready is eventually selected; the ready pool never starves one.  *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS
    Tasks,       \* the set of task ids competing for selection
    NotBefore,   \* [Tasks -> Nat] wall-clock gate per task; 0 = no gate
    MaxClock,    \* logical clock bound (keeps the state space finite)
    FairSelect,  \* TRUE: every ready task is eventually selected (positive weight); FALSE: starvation possible
    GateHonored  \* TRUE: selection honors not_before; FALSE: a future-gated task may dispatch early

VARIABLES
    clock,     \* logical wall clock
    claimed,   \* tasks currently holding a live selection claim (dispatched, not yet released)
    picked,    \* tasks that have EVER been selected at least once (monotonic; liveness witness)
    badPick    \* TRUE once any task was selected before its not_before (premature-dispatch witness)

vars == <<clock, claimed, picked, badPick>>

(***************************************************************************)
(* Scenario constants. TLC .cfg files cannot express a function literal in a  *)
(* CONSTANT assignment, so each configuration overrides Tasks/NotBefore via     *)
(* `Tasks <- ...` / `NotBefore <- ...` against one of these operators.          *)
(***************************************************************************)

Tasks3 == {"t1", "t2", "t3"}

\* No gates: every task is ready from clock 0 (isolates the starvation dimension).
NBNone == [t \in Tasks3 |-> 0]

\* One future gate: t3 must not be selected before clock 2 (the not_before case).
NBGate == [t \in Tasks3 |-> IF t = "t3" THEN 2 ELSE 0]

(***************************************************************************)
(* Predicates                                                              *)
(***************************************************************************)

\* A task's not_before gate has arrived (mirrors plan.Task.NotBeforeReached).
Ready(t) == clock >= NotBefore[t]

TypeOK ==
    /\ clock \in 0..MaxClock
    /\ claimed \subseteq Tasks
    /\ picked \subseteq Tasks
    /\ badPick \in BOOLEAN

(***************************************************************************)
(* Invariant (safety): no task is dispatched before its not_before.          *)
(***************************************************************************)
NoPrematureDispatch == badPick = FALSE

(***************************************************************************)
(* Init                                                                     *)
(***************************************************************************)
Init ==
    /\ clock = 0
    /\ claimed = {}
    /\ picked = {}
    /\ badPick = FALSE

(***************************************************************************)
(* Actions                                                                 *)
(***************************************************************************)

\* Time advances (the wall clock the not_before gate is read against).
Tick ==
    /\ clock < MaxClock
    /\ clock' = clock + 1
    /\ UNCHANGED <<claimed, picked, badPick>>

\* The selector dispatches an unclaimed task. In the fixed protocol it fires only
\* once the task's not_before gate is reached (GateHonored); the buggy protocol may
\* fire early, which is recorded in badPick as a premature dispatch. A task's turn
\* is granted per-task-fairly under FairSelect (positive weight) -- see Fairness.
Select(t) ==
    /\ t \notin claimed
    /\ (GateHonored => Ready(t))
    /\ claimed' = claimed \cup {t}
    /\ picked' = picked \cup {t}
    /\ badPick' = (badPick \/ ~Ready(t))
    /\ UNCHANGED clock

\* A selected task's claim TTL expires (or it completes and re-enters the stream):
\* it returns to the selectable pool. This is what makes starvation observable --
\* a requeued peer must not perpetually crowd out a task never yet selected.
Release(t) ==
    /\ t \in claimed
    /\ claimed' = claimed \ {t}
    /\ UNCHANGED <<clock, picked, badPick>>

Next ==
    \/ Tick
    \/ \E t \in Tasks : Select(t)
    \/ \E t \in Tasks : Release(t)

(***************************************************************************)
(* Fairness. Tick is weakly fair so gated tasks eventually become ready.      *)
(* Per-task selection fairness is granted ONLY under FairSelect: that is the    *)
(* weighted-random-with-positive-weight guarantee. Without it (buggy), a ready   *)
(* task may be starved forever while its peers are selected and requeued.        *)
(***************************************************************************)
Fairness ==
    /\ WF_vars(Tick)
    /\ \A t \in Tasks : (FairSelect => WF_vars(Select(t)))

Spec == Init /\ [][Next]_vars /\ Fairness

(***************************************************************************)
(* Liveness (NoStarvation): every task that is eventually ready is eventually  *)
(* selected. With MaxClock >= every gate, all tasks become ready, so this        *)
(* reduces to: every task is eventually selected -- the pool never starves one.  *)
(***************************************************************************)
EventuallySelected == \A t \in Tasks : <>(t \in picked)
NoStarvation == EventuallySelected
=============================================================================
