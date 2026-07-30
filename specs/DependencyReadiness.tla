-------------------------- MODULE DependencyReadiness -------------------------
(***************************************************************************)
(* Layer 2: the N-task dependency graph with cross-submodule links            *)
(* (internal/links/links.go, internal/select/graph.go) and the two silent-     *)
(* wedge failure classes it must never permit.                                 *)
(*                                                                         *)
(* This module GENERALIZES the original single-dependency readiness spec to a  *)
(* graph of N tasks whose dependency edges (DepEdges) may:                      *)
(*                                                                         *)
(*   - name a REAL task (an id in Tasks)      -> a legitimate blocking dep;     *)
(*   - name a PHANTOM/dangling id (NOT in     -> can never become DONE          *)
(*     Tasks, e.g. a cross-link dep whose        (plan.Plan.DanglingDeps); and  *)
(*     target task does not exist);                                            *)
(*   - form a CYCLE among real tasks          -> a mutual, permanent wedge      *)
(*     (t2 -> t3 -> t2), which the selector's    (links.CyclicNodes /           *)
(*     combined graph must detect.               selectt.Graph.InCycle).        *)
(*                                                                         *)
(* Two independent historical failure classes, each a SILENT deadlock:         *)
(*                                                                         *)
(*   PHANTOM: a work pass yields its task blocked on a dep that names no real   *)
(*     task; pre-fix the yield is accepted and the task is HELD forever         *)
(*     (masquerading as a clean dep-yield). Stranded                            *)
(*     flux:phantom-library-bluegreen-repin-gitea-images on the nonexistent    *)
(*     jellyfin:jellyfin-image-build. Fix (92d2ed1, swarm.go taskYieldedBlocked*)
(*     ): accept a blocked yield ONLY when every dep is a real task; a phantom  *)
(*     dep fails the pass LOUD and the task escalates to NEEDS-HUMAN.           *)
(*                                                                         *)
(*   CYCLE: two (or more) tasks each depend on the other, so neither can ever   *)
(*     reach DONE. Pre-fix the selector merely EXCLUDES a cyclic task from      *)
(*     candidates (silently held out forever); the fix detects the cycle and    *)
(*     escalates it to NEEDS-HUMAN rather than deadlocking silently.            *)
(*                                                                         *)
(* CONSTANT toggles select the broken vs fixed protocol independently:         *)
(*   DepGuard   = TRUE  -> a phantom-dep yield escalates NEEDS-HUMAN (fixed);   *)
(*                FALSE -> a phantom-dep yield is silently HELD (buggy).        *)
(*   CycleGuard = TRUE  -> a cycle escalates NEEDS-HUMAN (fixed);              *)
(*                FALSE -> a cyclic task is silently HELD (buggy).             *)
(*                                                                         *)
(* Invariants (safety):                                                      *)
(*   PhantomNeverHeld -- a HELD (accepted-yield) task never has a phantom dep;  *)
(*     a dangling/cross-link phantom dep is refused exactly like a local one.   *)
(*   CycleNeverHeld   -- a HELD task never lies on a dependency cycle; a cycle  *)
(*     escalates instead of masquerading as a legitimate blocked yield.         *)
(* Liveness:                                                                  *)
(*   EventuallyResolved -- every task always reaches a terminal (DONE for a     *)
(*     task whose real deps complete; NEEDS-HUMAN for a phantom or a cycle);    *)
(*     the graph never wedges forever.                                         *)
(***************************************************************************)
EXTENDS TLC

CONSTANTS
    Tasks,      \* the set of REAL task ids (a dep naming anything outside is phantom)
    DepEdges,   \* set of <<from, to>> edges; `to` may be a phantom id not in Tasks
    DepGuard,   \* TRUE: a phantom-dep yield escalates NEEDS-HUMAN instead of silent HELD
    CycleGuard  \* TRUE: a cyclic task escalates NEEDS-HUMAN instead of silent HELD

VARIABLE status   \* [Tasks -> Statuses]
vars == <<status>>

(***************************************************************************)
(* Scenario graphs. TLC .cfg files cannot express << >> tuple literals in a  *)
(* CONSTANT assignment, so each configuration overrides Tasks/DepEdges with   *)
(* one of these operators via `Tasks <- ...` / `DepEdges <- ...`.             *)
(***************************************************************************)

Tasks3 == {"t1", "t2", "t3"}
Tasks5 == {"t1", "t2", "t3", "t4", "t5"}

\* Fixed: a normal chain (t2->t1), a phantom dep (t3->ph, ph not a task), and a
\* real cycle (t4<->t5).
EdgesFixed == {<<"t2", "t1">>, <<"t3", "ph">>, <<"t4", "t5">>, <<"t5", "t4">>}

\* Phantom-class bug: t3 depends on the phantom (dangling) id ph.
EdgesPhantom == {<<"t2", "t1">>, <<"t3", "ph">>}

\* Cycle-class bug: t2 and t3 depend on each other (a mutual wait cycle).
EdgesCycle == {<<"t2", "t3">>, <<"t3", "t2">>}

Statuses == {"TODO", "HELD", "READY", "DONE", "HUMAN"}

(***************************************************************************)
(* Graph helpers over the CONSTANT edge set (state-independent).            *)
(***************************************************************************)

\* The dependency ids of task t (may include phantom ids not in Tasks).
DepsOf(t) == { e[2] : e \in { d \in DepEdges : d[1] = t } }

\* t depends on at least one id that names no real task -> a phantom/dangling dep.
HasPhantom(t) == \E d \in DepsOf(t) : d \notin Tasks

\* The real (in-graph) dependencies of t.
RealDeps(t) == DepsOf(t) \cap Tasks

\* Edges restricted to real tasks (a phantom id is a sink, never on a cycle).
RealEdges == { e \in DepEdges : e[2] \in Tasks /\ e[1] \in Tasks }

\* Reaches(a, b): a can reach b following RealEdges without revisiting (bounded
\* DFS over the finite Tasks set -- deterministic, terminates).
RECURSIVE Reaches(_, _, _)
Reaches(a, b, seen) ==
    \/ <<a, b>> \in RealEdges
    \/ \E m \in Tasks :
        /\ <<a, m>> \in RealEdges
        /\ m \notin seen
        /\ Reaches(m, b, seen \cup {a})

\* t lies on a dependency cycle: it can reach itself through real edges.
OnCycle(t) == Reaches(t, t, {})

\* All of t's real dependencies have reached DONE (in state s).
DepsAllDone(s, t) == \A d \in RealDeps(t) : s[d] = "DONE"

TypeOK == status \in [Tasks -> Statuses]

(***************************************************************************)
(* Invariants                                                              *)
(***************************************************************************)

\* A task accepted as a legitimate blocked yield (HELD) never carries a phantom
\* dep -- a dangling/cross-link dep is refused (escalated), never silently held.
PhantomNeverHeld == \A t \in Tasks : (status[t] = "HELD") => ~HasPhantom(t)

\* A HELD task never lies on a dependency cycle -- a cycle escalates to
\* NEEDS-HUMAN rather than silently deadlocking as a masqueraded blocked yield.
CycleNeverHeld == \A t \in Tasks : (status[t] = "HELD") => ~OnCycle(t)

(***************************************************************************)
(* Init: every task starts TODO.                                            *)
(***************************************************************************)
Init == status = [t \in Tasks |-> "TODO"]

(***************************************************************************)
(* Actions                                                                 *)
(***************************************************************************)

\* A work pass runs a TODO task that is NOT immediately completable (on a cycle,
\* has a phantom dep, or a real dep is not yet DONE) and yields. The runner
\* decides how, per guard:
\*   on a cycle    -> NEEDS-HUMAN if CycleGuard else silently HELD (buggy wedge);
\*   phantom dep   -> NEEDS-HUMAN if DepGuard   else silently HELD (buggy wedge);
\*   real dep pend -> a legitimate HELD (blocked yield), released on Unblock.
Yield(t) ==
    /\ status[t] = "TODO"
    /\ (OnCycle(t) \/ HasPhantom(t) \/ ~DepsAllDone(status, t))
    /\ status' = [status EXCEPT ![t] =
            IF OnCycle(t)    THEN (IF CycleGuard THEN "HUMAN" ELSE "HELD")
            ELSE IF HasPhantom(t) THEN (IF DepGuard THEN "HUMAN" ELSE "HELD")
            ELSE "HELD"]

\* A TODO task whose real deps are already all DONE, with no cycle and no phantom,
\* is worked straight through to DONE (no yield needed).
DirectDone(t) ==
    /\ status[t] = "TODO"
    /\ ~OnCycle(t)
    /\ ~HasPhantom(t)
    /\ DepsAllDone(status, t)
    /\ status' = [status EXCEPT ![t] = "DONE"]

\* A legitimately HELD task (no cycle, no phantom) whose real deps are now all
\* DONE is released back into the ready pool.
Unblock(t) ==
    /\ status[t] = "HELD"
    /\ ~OnCycle(t)
    /\ ~HasPhantom(t)
    /\ DepsAllDone(status, t)
    /\ status' = [status EXCEPT ![t] = "READY"]

\* The now-ready task is worked to completion.
Progress(t) ==
    /\ status[t] = "READY"
    /\ status' = [status EXCEPT ![t] = "DONE"]

\* Terminal idle so an all-settled graph is not read as a deadlock.
Terminal ==
    /\ \A t \in Tasks : status[t] \in {"DONE", "HUMAN"}
    /\ UNCHANGED status

Next ==
    \/ \E t \in Tasks : Yield(t)
    \/ \E t \in Tasks : DirectDone(t)
    \/ \E t \in Tasks : Unblock(t)
    \/ \E t \in Tasks : Progress(t)
    \/ Terminal

Fairness ==
    /\ \A t \in Tasks : WF_vars(Yield(t))
    /\ \A t \in Tasks : WF_vars(DirectDone(t))
    /\ \A t \in Tasks : WF_vars(Unblock(t))
    /\ \A t \in Tasks : WF_vars(Progress(t))

Spec == Init /\ [][Next]_vars /\ Fairness

(***************************************************************************)
(* Liveness: the graph never wedges -- every task reaches a terminal.        *)
(***************************************************************************)
EventuallyResolved == \A t \in Tasks : <>(status[t] \in {"DONE", "HUMAN"})
=============================================================================
