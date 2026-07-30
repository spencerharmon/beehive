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
    CycleGuard, \* TRUE: a cyclic task escalates NEEDS-HUMAN instead of silent HELD
    AtomicWrite \* WRITE-TIME toggle (WriteSpec only, ignored by the runtime Spec):
                \* TRUE  -> AddDep is cycle-checked and LinkSubmodules writes both
                \*          directions atomically (internal/links/links.go, fixed);
                \* FALSE  -> the cycle check is dropped / raced and a link is written
                \*          one direction at a time (a partial, non-reciprocal write).

VARIABLE status   \* [Tasks -> Statuses] (runtime readiness model)
\* Write-time model state (WriteSpec): the persisted dependency + link registry
\* that internal/links/links.go AddDep / LinkSubmodules mutate.
VARIABLE wedges   \* set of persisted directed dep edges <<from, to>>
VARIABLE wlinks   \* set of persisted directed submodule-link edges <<a, b>>
vars == <<status, wedges, wlinks>>

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
(* Init: every task starts TODO; the write-time registry starts empty.       *)
(***************************************************************************)
Init ==
    /\ status = [t \in Tasks |-> "TODO"]
    /\ wedges = {}
    /\ wlinks = {}

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
    /\ UNCHANGED <<wedges, wlinks>>

\* A TODO task whose real deps are already all DONE, with no cycle and no phantom,
\* is worked straight through to DONE (no yield needed).
DirectDone(t) ==
    /\ status[t] = "TODO"
    /\ ~OnCycle(t)
    /\ ~HasPhantom(t)
    /\ DepsAllDone(status, t)
    /\ status' = [status EXCEPT ![t] = "DONE"]
    /\ UNCHANGED <<wedges, wlinks>>

\* A legitimately HELD task (no cycle, no phantom) whose real deps are now all
\* DONE is released back into the ready pool.
Unblock(t) ==
    /\ status[t] = "HELD"
    /\ ~OnCycle(t)
    /\ ~HasPhantom(t)
    /\ DepsAllDone(status, t)
    /\ status' = [status EXCEPT ![t] = "READY"]
    /\ UNCHANGED <<wedges, wlinks>>

\* The now-ready task is worked to completion.
Progress(t) ==
    /\ status[t] = "READY"
    /\ status' = [status EXCEPT ![t] = "DONE"]
    /\ UNCHANGED <<wedges, wlinks>>

\* Terminal idle so an all-settled graph is not read as a deadlock.
Terminal ==
    /\ \A t \in Tasks : status[t] \in {"DONE", "HUMAN"}
    /\ UNCHANGED vars

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

(***************************************************************************)
(* WRITE-TIME MODEL (WriteSpec)                                             *)
(*                                                                         *)
(* The runtime model above proves a cycle/phantom that ALREADY EXISTS in the  *)
(* graph is escalated rather than silently wedged. This half proves the graph  *)
(* can never have a cycle WRITTEN into it in the first place, and a submodule   *)
(* link is always registered reciprocally -- the write-time invariants of      *)
(* internal/links/links.go:                                                    *)
(*                                                                         *)
(*   AddDep(from,to)  appends the edge, then calls cycle()/HasCycle() and       *)
(*     ROLLS BACK (returns an error, no state change) if the append closed a    *)
(*     cycle -- so a cycle is refused BEFORE it is ever persisted. Modeled as   *)
(*     AddDepAtomic: the edge is written only when the resulting graph stays    *)
(*     acyclic. The buggy variant AddDepUnchecked drops the check (a dropped or *)
(*     raced cycle guard) and can persist a cycle-closing edge.                 *)
(*                                                                         *)
(*   LinkSubmodules(a,b) registers BOTH submodules (an undirected link) in one  *)
(*     call -- both directions or neither is ever observable. Modeled as        *)
(*     LinkAtomic (both <<a,b>> and <<b,a>> in a single step). The buggy variant*)
(*     writes one direction (LinkOneDir) then later the reverse (LinkReverse),  *)
(*     exposing an intermediate NON-RECIPROCAL state where one submodule sees   *)
(*     the link and its peer does not.                                          *)
(*                                                                         *)
(* CONSTANT toggle AtomicWrite selects the fixed (TRUE) vs buggy (FALSE)        *)
(* protocol. Invariants (checked in EVERY reachable state):                    *)
(*   NoCycleWritten  -- the persisted dep graph is acyclic (a cycle literally   *)
(*     never appears, not merely "an existing cycle is handled").               *)
(*   ReciprocalLinks -- every persisted link edge has its reverse present.      *)
(***************************************************************************)

\* Write-time scenario nodes and candidates (concrete; not cfg-substituted).
WNodes == {"a", "b", "c"}

\* Three dep edges that, added in any order, can only close a cycle on the third
\* (a->b, b->c, c->a): the AddDep cycle check must refuse whichever edge closes it.
CandDeps == {<<"a", "b">>, <<"b", "c">>, <<"c", "a">>}

\* Candidate submodule link (unordered pair a--b) to register reciprocally.
CandLinks == {<<"a", "b">>}

\* Reachability / cycle detection over an ARBITRARY (state-carried) edge set E.
RECURSIVE ReachesIn(_, _, _, _)
ReachesIn(E, a, b, seen) ==
    \/ <<a, b>> \in E
    \/ \E m \in WNodes :
        /\ <<a, m>> \in E
        /\ m \notin seen
        /\ ReachesIn(E, m, b, seen \cup {a})

HasCycleIn(E) == \E n \in WNodes : ReachesIn(E, n, n, {})

(***************************************************************************)
(* Write-time actions                                                      *)
(***************************************************************************)

\* FIXED AddDep: persist a candidate edge only if the graph STAYS acyclic
\* (models append + cycle()-check + rollback as one atomic refusal).
AddDepAtomic(e) ==
    /\ e \notin wedges
    /\ e[1] # e[2]
    /\ ~HasCycleIn(wedges \cup {e})
    /\ wedges' = wedges \cup {e}
    /\ UNCHANGED <<status, wlinks>>

\* BUGGY AddDep: persist a candidate edge WITHOUT the cycle check (a dropped or
\* raced guard) -- can write an edge that closes a cycle.
AddDepUnchecked(e) ==
    /\ e \notin wedges
    /\ e[1] # e[2]
    /\ wedges' = wedges \cup {e}
    /\ UNCHANGED <<status, wlinks>>

\* FIXED LinkSubmodules: register BOTH directions atomically.
LinkAtomic(p) ==
    /\ <<p[1], p[2]>> \notin wlinks
    /\ wlinks' = wlinks \cup {<<p[1], p[2]>>, <<p[2], p[1]>>}
    /\ UNCHANGED <<status, wedges>>

\* BUGGY LinkSubmodules step 1: write only one direction (partial registration).
LinkOneDir(p) ==
    /\ <<p[1], p[2]>> \notin wlinks
    /\ <<p[2], p[1]>> \notin wlinks
    /\ wlinks' = wlinks \cup {<<p[1], p[2]>>}
    /\ UNCHANGED <<status, wedges>>

\* BUGGY LinkSubmodules step 2: later add the reverse (closes the reciprocity gap
\* -- but the intermediate non-reciprocal state was already observable).
LinkReverse(p) ==
    /\ <<p[1], p[2]>> \in wlinks
    /\ <<p[2], p[1]>> \notin wlinks
    /\ wlinks' = wlinks \cup {<<p[2], p[1]>>}
    /\ UNCHANGED <<status, wedges>>

\* Always-enabled stutter so a settled write graph is not read as a deadlock.
WriteStutter == UNCHANGED vars

WriteNext ==
    IF AtomicWrite
    THEN \/ \E e \in CandDeps  : AddDepAtomic(e)
         \/ \E p \in CandLinks : LinkAtomic(p)
         \/ WriteStutter
    ELSE \/ \E e \in CandDeps  : AddDepUnchecked(e)
         \/ \E p \in CandLinks : LinkOneDir(p)
         \/ \E p \in CandLinks : LinkReverse(p)
         \/ WriteStutter

WriteSpec == Init /\ [][WriteNext]_vars

(***************************************************************************)
(* Write-time invariants (safety; hold in EVERY reachable state).            *)
(***************************************************************************)

\* The persisted dependency graph is acyclic -- a cycle is never written.
NoCycleWritten == ~HasCycleIn(wedges)

\* Every persisted link edge is bidirectional (both submodules see the link).
ReciprocalLinks == \A e \in wlinks : <<e[2], e[1]>> \in wlinks
=============================================================================
