---------------------------- MODULE DeferBound -----------------------------
(***************************************************************************)
(* The self-defer convergence-wait termination modeled by                    *)
(* internal/plan/mutate.go `Defer` (bumps `not_before` + increments          *)
(* `Defers`, gated by `plan.MaxDefers`) and `Reopen` (`Defers = 0`, the       *)
(* reset behind a fresh operator reopen). This is the mechanism behind       *)
(* `beehive task defer` and the runner's own re-check scheduling            *)
(* (HONEYBEE.md "TODO -> TODO (self-defer)"): "did the work, the world has   *)
(* not converged yet, re-check after `until`" -- used for GitOps reconcile,  *)
(* CI runs, and the merge-gated `Verify-After-Merge` successor-check         *)
(* flagged as specified-but-unmodeled in docs/formal-spec-mapping.md's       *)
(* Roadmap section.                                                          *)
(*                                                                         *)
(* Failure class = LIVELOCK: a task defers forever on a convergence that     *)
(* never arrives (never escalates to a human -- swarm.go:2214-2215's         *)
(* "work pass self-deferred task past the defer cap" refusal is exactly the *)
(* fix), or gives up (escalates) before the external effect actually lands.  *)
(*                                                                         *)
(* The external "converged" boolean is modeled as an UNFAIR (adversarial)   *)
(* environment action: real convergence (a GitOps reconcile, a CI run, a     *)
(* cache TTL) is NOT something the swarm may assume fairness over -- it may *)
(* genuinely never happen (a permanently broken pipeline, a dependency that *)
(* will never land). That is precisely why the bound-and-escalate contract  *)
(* is required: termination must hold REGARDLESS of whether the awaited     *)
(* effect ever converges, not merely "eventually, assuming fairness".        *)
(*                                                                         *)
(* CONSTANT Bounded selects the fixed protocol (TRUE: `Defer` refuses once    *)
(* `Defers >= MaxDefers`, `plan.MaxDefers` in plan.go, and the task           *)
(* escalates NEEDS-HUMAN instead of deferring again -- swarm.go's defer-cap  *)
(* refusal) vs the buggy pre-fix behavior (FALSE: `Defer` stays enabled no   *)
(* matter how large the counter grows, and there is no escalation edge) --   *)
(* see the *_buggy.cfg / *_fixed.cfg files.                                  *)
(***************************************************************************)
EXTENDS Naturals, TLC

CONSTANTS
    MaxDefers, \* the bound on repeated self-defers (plan.MaxDefers = 12 in code)
    Bounded    \* TRUE: Defer refuses past MaxDefers and the task escalates (the fix).
               \* FALSE: Defer is unconditionally enabled and there is no escalation
               \* edge -- an unconverging effect defers the task forever (the bug).

VARIABLES
    status,    \* one of Statuses: "WAIT" (deferred, awaiting convergence),
               \* "DONE" (converged and progressed), "HUMAN" (escalated)
    defers,    \* the Defers counter mutate.go's Defer increments each call
    converged  \* the external effect's real-world convergence flag (environment-owned)

vars == <<status, defers, converged>>

Statuses == {"WAIT", "DONE", "HUMAN"}

TypeOK ==
    /\ status \in Statuses
    /\ defers \in Nat
    /\ converged \in BOOLEAN

(***************************************************************************)
(* Safety invariants                                                       *)
(***************************************************************************)

\* The task is never marked progressed (DONE) while the awaited external
\* effect has not actually converged -- the anti-false-progress invariant a
\* self-defer loop must never violate: a deferring task's DONE-ward edge
\* (Progress below) is gated on `converged`, mirroring the runner's own rule
\* that a work pass may only leave the defer loop once the effect is real.
NoDoneWhileUnconverged ==
    (status = "DONE") => converged

\* Under the fixed (Bounded) protocol the Defers counter never runs away past
\* the configured cap -- mutate.go's Defer refusal plus swarm.go's defer-cap
\* check together enforce this; under the buggy (unbounded) cfg this is NOT
\* asserted as an invariant (it is expected to grow without bound, which is
\* exactly the reproduced defect -- see the liveness violation below instead).
DefersBoundedWhenBounded ==
    Bounded => defers <= MaxDefers

(***************************************************************************)
(* Init                                                                    *)
(***************************************************************************)
Init ==
    /\ status = "WAIT"
    /\ defers = 0
    /\ converged = FALSE

(***************************************************************************)
(* Actions                                                                 *)
(***************************************************************************)

\* mutate.go Defer: bump not_before (abstracted away -- only its counter
\* effect matters here) and increment Defers. Bounded: refuses once the cap
\* is reached (the fix, matching swarm.go's own defer-cap refusal, which
\* means the work pass must NOT call Defer again past the cap -- it is
\* required to escalate instead, modeled by Escalate below). Unbounded: no
\* such guard -- Defer remains ENABLED forever regardless of `defers`. The
\* counter itself is saturated at MaxDefers (`defers' = defers` once there)
\* purely to keep TLC's state space finite -- the real code's `Defers` field
\* grows without bound in the unbounded cfg, but the reproduced defect is
\* Defer's unbounded ENABLEDNESS (it never stops being a legal move), not the
\* raw counter value, so saturating the tracked number loses nothing the
\* invariants/properties below actually check.
Defer ==
    /\ status = "WAIT"
    /\ ~converged
    /\ (Bounded => defers < MaxDefers)
    /\ defers' = IF defers < MaxDefers THEN defers + 1 ELSE defers
    /\ UNCHANGED <<status, converged>>

\* The environment: the awaited external effect (GitOps reconcile, CI run,
\* cache TTL) flips to converged at some nondeterministic point. Deliberately
\* NOT given weak fairness in the Fairness formula below -- real-world
\* convergence is not something the protocol may assume will eventually
\* happen; it may never happen at all (a broken pipeline, a dependency that
\* never lands). The protocol must terminate regardless.
Converge ==
    /\ ~converged
    /\ converged' = TRUE
    /\ UNCHANGED <<status, defers>>

\* Once the effect has converged, the task leaves the defer loop and
\* progresses (mirrors the work pass observing the effect live and completing
\* instead of deferring again).
Progress ==
    /\ status = "WAIT"
    /\ converged
    /\ status' = "DONE"
    /\ UNCHANGED <<defers, converged>>

\* The fix: once the bound is exhausted without convergence, the task
\* escalates NEEDS-HUMAN instead of deferring forever (swarm.go:2214-2215's
\* refusal is exactly what forces this edge in the real runner -- a work pass
\* that keeps trying to self-defer past the cap is rejected, so its only
\* remaining move is to escalate). Only defined -- indeed only reachable --
\* when Bounded; the unbounded (buggy) cfg has no such escape hatch at all.
Escalate ==
    /\ status = "WAIT"
    /\ Bounded
    /\ defers >= MaxDefers
    /\ ~converged
    /\ status' = "HUMAN"
    /\ UNCHANGED <<defers, converged>>

\* Terminal idle so a completed/escalated task does not read as a deadlock.
Done ==
    /\ status \in {"DONE", "HUMAN"}
    /\ UNCHANGED vars

Next ==
    \/ Defer
    \/ Converge
    \/ Progress
    \/ Escalate
    \/ Done

(***************************************************************************)
(* Liveness                                                                *)
(*                                                                         *)
(* Terminates is the property that distinguishes the two cfgs: under the    *)
(* fixed (Bounded) protocol it holds in EVERY behavior, because the         *)
(* Escalate edge fires once the bound is exhausted even if convergence      *)
(* never arrives. Under the buggy (unbounded) cfg there is no Escalate      *)
(* edge and Converge is deliberately unfair, so TLC finds a behavior that    *)
(* takes Defer forever and never takes Converge -- a genuine non-           *)
(* terminating (livelocked) trace, reproducing the ROI's failure class.     *)
(*                                                                         *)
(* Fairness is intentionally narrow: only the task's OWN internal moves     *)
(* (Defer, Progress, Escalate) are weakly fair, never Converge (the         *)
(* environment). WF(Defer) ensures the self-defer loop actually KEEPS        *)
(* re-checking rather than stalling mid-loop (a work pass that holds a       *)
(* deferred task does not simply go idle forever without re-dispatching     *)
(* it -- the selector's own `not_before` re-check drives it); WF(Progress)/  *)
(* WF(Escalate) ensure the task actually TAKES its exit once eligible        *)
(* rather than stalling on it forever while eligible -- without asserting    *)
(* anything about whether the awaited effect ever arrives, which is         *)
(* precisely the guarantee the bound-and-escalate contract must hold up     *)
(* without.                                                                 *)
(***************************************************************************)
Terminates == <>(status \in {"DONE", "HUMAN"})

Fairness ==
    /\ WF_vars(Defer)
    /\ WF_vars(Progress)
    /\ WF_vars(Escalate)

Spec == Init /\ [][Next]_vars /\ Fairness
=============================================================================
