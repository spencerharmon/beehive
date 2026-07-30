--------------------------- MODULE BootstrapRace ---------------------------
(***************************************************************************)
(* Layer 1 (sibling of MainConvergence.tla): two concurrent BOOTSTRAP        *)
(* passes, each observing "no PLAN.md exists yet" for the same submodule,    *)
(* each attempting to create + publish the plan's first version. This is    *)
(* the spec-first model of a race the Go level ALREADY defends today --     *)
(* TestRunBootstrapGatesOnUnpublishedPlan (internal/swarm/swarm_test.go)     *)
(* proves a bootstrap pass whose PLAN.md write never lands a commit is       *)
(* gated (not reported Completed, and marked for GC / re-drive) -- so this   *)
(* module is a documented, already-gated candidate, not a fix for an         *)
(* observed bug (see PLAN.md task specs-bootstrap-race).                    *)
(*                                                                         *)
(* Failure class = a lost/overwritten PLAN.md: without a gate on the         *)
(* "unpublished plan" check, two bootstrap sessions can each observe no      *)
(* PLAN.md, each build their own version, and both publish -- the second     *)
(* publish either overwrites the first session's plan (if publish is a       *)
(* blind write) or, more subtly, the second session's local completion       *)
(* check reports success against ITS OWN unpublished worktree copy while     *)
(* main only ever gains one of the two. The fixed protocol gates: a          *)
(* bootstrap session's local "PLAN.md exists" check is refreshed against     *)
(* the FRESH main tip immediately before it commits/publishes, and a         *)
(* publish is a genuine merge (first commit wins) rather than a blind        *)
(* overwrite -- so a second, now-stale session's publish attempt observes    *)
(* the already-landed plan and no-ops instead of clobbering it.              *)
(*                                                                         *)
(* CONSTANT Gated selects the fixed vs buggy protocol: TRUE reproduces the   *)
(* defended (Go-level tested) behavior -- exactly one PLAN.md ever lands,    *)
(* and a second, losing session no-ops against the now-published plan.      *)
(* FALSE is the counterfactual an ungated bootstrap check would allow: both  *)
(* sessions publish, and the second either overwrites the first or main      *)
(* ends up with neither publish faithfully recorded as "the" plan.          *)
(***************************************************************************)
EXTENDS Naturals, TLC

CONSTANTS
    BSessions,  \* the set of concurrent bootstrap sessions racing the same submodule
    Gated       \* TRUE: a session re-checks "PLAN.md exists on the fresh main tip"
                \*   immediately before publishing and no-ops if a peer already
                \*   published (first-publish-wins). FALSE: no such gate -- a
                \*   session that built its own plan from a stale "no PLAN.md yet"
                \*   observation blindly publishes regardless of what landed since.

VARIABLES
    planExists,     \* whether a PLAN.md has landed on main for this submodule
    planOwner,      \* which session's plan is the one CURRENTLY recorded on main (0 = none)
    firstOwner,     \* sticky: the session whose publish FIRST created the plan (0 = none yet)
    bPhase,         \* [BSessions -> {"idle", "observed", "built"}]
    bBaseExists,    \* [BSessions -> BOOLEAN] each session's snapshot of planExists
                    \*   at the moment it observed "no PLAN.md" and decided to bootstrap
    publishCount    \* total number of bootstrap publishes that actually recorded
                     \* a plan (as opposed to a gated no-op) -- the redundancy counter

vars == <<planExists, planOwner, firstOwner, bPhase, bBaseExists, publishCount>>

(***************************************************************************)
(* Type invariant                                                          *)
(***************************************************************************)
TypeOK ==
    /\ planExists \in BOOLEAN
    /\ planOwner \in (BSessions \union {0})
    /\ firstOwner \in (BSessions \union {0})
    /\ bPhase \in [BSessions -> {"idle", "observed", "built"}]
    /\ bBaseExists \in [BSessions -> BOOLEAN]
    /\ publishCount \in 0..8

(***************************************************************************)
(* Safety invariants                                                       *)
(***************************************************************************)

\* Exactly one PLAN.md ever lands on main for this submodule: at most one
\* bootstrap publish ever actually records a plan. Without the gate, two
\* sessions racing the same "no PLAN.md yet" observation can each publish,
\* violating this.
SinglePlanCreated == publishCount <= 1

\* A second bootstrap attempt that loses the race observes the now-published
\* plan and no-ops rather than overwriting it: the plan's recorded owner
\* never changes away from whichever session's publish FIRST created it --
\* no later, losing session's stale-built publish ever clobbers the winner.
\* (firstOwner is sticky: set once on the very first publish, never reset.)
SecondBootstrapNoOps == (firstOwner # 0) => (planOwner = firstOwner)

(***************************************************************************)
(* Init                                                                    *)
(***************************************************************************)
Init ==
    /\ planExists = FALSE
    /\ planOwner = 0
    /\ firstOwner = 0
    /\ bPhase = [s \in BSessions |-> "idle"]
    /\ bBaseExists = [s \in BSessions |-> FALSE]
    /\ publishCount = 0

(***************************************************************************)
(* Actions                                                                 *)
(***************************************************************************)

\* A bootstrap session observes the submodule's PLAN.md state (the selector's
\* "no PLAN.md -> pick bootstrap kind" check) and snapshots it. Only
\* meaningful while the session is idle and no plan has landed yet at the
\* time of THIS observation -- a session observing an already-published plan
\* would never even select bootstrap (see swarm.go task selection).
Observe(s) ==
    /\ bPhase[s] = "idle"
    /\ ~planExists
    /\ bPhase' = [bPhase EXCEPT ![s] = "observed"]
    /\ bBaseExists' = [bBaseExists EXCEPT ![s] = planExists]
    /\ UNCHANGED <<planExists, planOwner, firstOwner, publishCount>>

\* The session builds its own PLAN.md in its private worktree (agent turn
\* loop writing the file), independent of what any peer has done since --
\* this is the local, uncommitted work a Byzantine-agent-effects model must
\* allow to proceed regardless of concurrent state.
Build(s) ==
    /\ bPhase[s] = "observed"
    /\ bPhase' = [bPhase EXCEPT ![s] = "built"]
    /\ UNCHANGED <<planExists, planOwner, firstOwner, bBaseExists, publishCount>>

\* The session attempts to commit + publish its built plan to main.
\*   Gated = TRUE  -- re-check the FRESH planExists immediately before
\*     publishing (the Go-level "gates on unpublished plan" check): if a
\*     peer already landed a plan, no-op (first-publish-wins, no clobber).
\*     Otherwise this session's publish is the one that lands.
\*   Gated = FALSE -- publish unconditionally from the stale bBaseExists
\*     snapshot, ignoring whatever landed since: a second session can
\*     blindly publish even though a peer already has, either overwriting
\*     the winner's plan (planOwner flips) or double-counting the publish.
Publish(s) ==
    /\ bPhase[s] = "built"
    /\ bPhase' = [bPhase EXCEPT ![s] = "idle"]
    /\ IF Gated
         THEN IF planExists
                THEN \* a peer already published: no-op, never clobber it
                     UNCHANGED <<planExists, planOwner, firstOwner, publishCount>>
                ELSE /\ planExists' = TRUE
                     /\ planOwner' = s
                     /\ firstOwner' = IF firstOwner = 0 THEN s ELSE firstOwner
                     /\ publishCount' = publishCount + 1
         ELSE /\ planExists' = TRUE
              /\ planOwner' = s
              /\ firstOwner' = IF firstOwner = 0 THEN s ELSE firstOwner
              /\ publishCount' = publishCount + 1
    /\ UNCHANGED bBaseExists

\* Terminal idle: once every session is idle and there is nothing left to
\* observe/build/publish, stutter -- avoids a spurious deadlock report on a
\* fully-converged state.
Done ==
    /\ \A s \in BSessions : bPhase[s] = "idle"
    /\ UNCHANGED vars

Next ==
    \/ \E s \in BSessions : Observe(s)
    \/ \E s \in BSessions : Build(s)
    \/ \E s \in BSessions : Publish(s)
    \/ Done

Spec == Init /\ [][Next]_vars

=============================================================================
