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
(*                                                                         *)
(* WRITE-TIME half (internal/links/links.go AddDep/LinkSubmodules): the        *)
(* invariants above model the RUNTIME wedge -- a cycle that already EXISTS      *)
(* deadlocking silently. This section models the complementary guarantee that  *)
(* `AddDep`/`LinkSubmodules` must uphold at WRITE time so that wedge can never  *)
(* be reached in the first place:                                              *)
(*                                                                         *)
(*   - AddDep checks-then-writes an edge in TWO conceptual steps (compute a     *)
(*     cycle verdict, then commit it). A correct implementation re-validates    *)
(*     the verdict ATOMICALLY against the live graph at commit time (matching   *)
(*     links.go's real `l.Deps = append(...); if cycle(l.Deps) != nil { undo }`  *)
(*     sequence, which never yields control between the tentative append and    *)
(*     the cycle check). A buggy/non-atomic implementation instead trusts a     *)
(*     STALE verdict computed earlier: two concurrent `AddDep` calls each check  *)
(*     against the pre-write graph and each get "no cycle", then both commit --  *)
(*     jointly closing a cycle neither call would have permitted alone (a       *)
(*     classic check-then-act race).                                           *)
(*   - LinkSubmodules registers a bidirectional link; `SUBMODULE-LINKS.yaml`    *)
(*     must never observably contain only ONE direction. A correct              *)
(*     implementation writes both directions in one atomic step (matching       *)
(*     `LinkSubmodules(a, b)` appending both `a` and `b` to the same in-memory   *)
(*     `Submodules` list before a single `Save`). A buggy/non-atomic            *)
(*     implementation writes the two directions as separate steps, exposing an  *)
(*     intermediate state where a submodule sees the link but its peer does     *)
(*     not.                                                                     *)
(*                                                                         *)
(* CONSTANT toggles (independent of DepGuard/CycleGuard above):                *)
(*   AtomicWrite = TRUE  -> AddDep re-checks for a cycle ATOMICALLY against the *)
(*                          live graph at commit time (fixed: matches links.go) *)
(*                 FALSE -> AddDep commits on a STALE verdict computed earlier, *)
(*                          racing a concurrent AddDep (buggy).                 *)
(*   AtomicLink  = TRUE  -> LinkSubmodules writes both directions in one step   *)
(*                          (fixed: no partial link is ever observable).       *)
(*                 FALSE -> LinkSubmodules writes each direction separately,    *)
(*                          exposing a non-reciprocal intermediate (buggy).    *)
(*                                                                         *)
(* Invariants (safety, write-time):                                            *)
(*   NoCycleWritten   -- the write-time dependency graph (`wedges`) is acyclic  *)
(*     in EVERY reachable state -- not merely "a cycle that exists deadlocks",  *)
(*     a cycle is never WRITTEN in the first place.                            *)
(*   ReciprocalLinks  -- every proposed submodule link pair is bidirectional    *)
(*     (both directions present, or neither) in EVERY reachable state.         *)
(***************************************************************************)
EXTENDS TLC

CONSTANTS
    Tasks,      \* the set of REAL task ids (a dep naming anything outside is phantom)
    DepEdges,   \* set of <<from, to>> edges; `to` may be a phantom id not in Tasks
    DepGuard,   \* TRUE: a phantom-dep yield escalates NEEDS-HUMAN instead of silent HELD
    CycleGuard, \* TRUE: a cyclic task escalates NEEDS-HUMAN instead of silent HELD
    WNodes,       \* write-time model: universe of node ids AddDep may connect
    Writers,      \* write-time model: set of concurrent AddDep callers
    ProposedEdge, \* [Writers -> <<from, to>>]: the edge each writer proposes to add
    AtomicWrite,  \* TRUE: AddDep re-checks atomically at commit (fixed)
    LinkPairs,    \* write-time model: set of <<a, b>> submodule pairs to link
    AtomicLink    \* TRUE: LinkSubmodules writes both directions atomically (fixed)

VARIABLE status   \* [Tasks -> Statuses]
VARIABLE wedges     \* set of <<from, to>> edges actually committed by AddDep so far
VARIABLE wdecision  \* [Writers -> {"none", "allow", "deny"}]: each writer's cycle verdict
VARIABLE wphase     \* [Writers -> {"init", "checked", "done"}]: each writer's write phase
VARIABLE seenBy     \* set of <<submodule, peer>> directions LinkSubmodules has written
vars == <<status, wedges, wdecision, wphase, seenBy>>

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
(* Write-time scenario: two concurrent AddDep writers whose proposed edges     *)
(* are individually acyclic against an empty graph but jointly close a cycle   *)
(* (b->a then a->b) -- the classic check-then-act race. WritersAB/WNodesAB are *)
(* shared by both the AtomicWrite=TRUE (fixed) and AtomicWrite=FALSE (buggy)   *)
(* configurations; only AtomicWrite differs between them.                     *)
(***************************************************************************)

WNodesAB == {"a", "b"}
WritersAB == {"w1", "w2"}
RaceEdges == [w1 |-> <<"b", "a">>, w2 |-> <<"a", "b">>]

\* Write-time link scenario: one submodule pair to be linked bidirectionally.
LinkPairsOne == {<<"sm1", "sm2">>}

\* No-op write-time scenario for cfgs that only exercise the runtime
\* (PhantomNeverHeld/CycleNeverHeld) invariants above: empty writers/pairs mean
\* CheckDep/WriteDep/LinkBoth/LinkOneDirection/LinkOtherDirection are all
\* vacuously disabled, so NoCycleWritten/ReciprocalLinks hold trivially and the
\* write-time model does not interfere with the runtime scenario.
NoWriters == {}
NoProposedEdge == [w \in NoWriters |-> <<"x", "x">>]
NoLinkPairs == {}

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
(* Write-time helpers (parameterized on an edge set, unlike RealEdges above    *)
(* which is fixed over the CONSTANT DepEdges): a bounded DFS cycle check over   *)
(* WNodes, mirroring links.go's `cycle()` -- deterministic, terminates.        *)
(***************************************************************************)

RECURSIVE WReaches(_, _, _, _)
WReaches(edges, a, b, seen) ==
    \/ <<a, b>> \in edges
    \/ \E m \in WNodes :
        /\ <<a, m>> \in edges
        /\ m \notin seen
        /\ WReaches(edges, m, b, seen \cup {a})

\* edges contains a cycle: some node can reach itself.
WHasCycle(edges) == \E n \in WNodes : WReaches(edges, n, n, {})

(***************************************************************************)
(* Invariants (safety, write-time)                                          *)
(***************************************************************************)

\* The write-time dependency graph is acyclic in every reachable state -- a
\* cycle is never WRITTEN, not merely detected-and-excluded after the fact.
NoCycleWritten == ~WHasCycle(wedges)

\* Every proposed submodule link pair is bidirectional in every reachable
\* state: either both directions are recorded, or neither is.
ReciprocalLinks ==
    \A p \in LinkPairs : (<<p[1], p[2]>> \in seenBy) <=> (<<p[2], p[1]>> \in seenBy)

(***************************************************************************)
(* Init: every task starts TODO; the write-time graph/links start empty.     *)
(***************************************************************************)
Init ==
    /\ status = [t \in Tasks |-> "TODO"]
    /\ wedges = {}
    /\ wdecision = [w \in Writers |-> "none"]
    /\ wphase = [w \in Writers |-> "init"]
    /\ seenBy = {}

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
    /\ UNCHANGED <<wedges, wdecision, wphase, seenBy>>

\* A TODO task whose real deps are already all DONE, with no cycle and no phantom,
\* is worked straight through to DONE (no yield needed).
DirectDone(t) ==
    /\ status[t] = "TODO"
    /\ ~OnCycle(t)
    /\ ~HasPhantom(t)
    /\ DepsAllDone(status, t)
    /\ status' = [status EXCEPT ![t] = "DONE"]
    /\ UNCHANGED <<wedges, wdecision, wphase, seenBy>>

\* A legitimately HELD task (no cycle, no phantom) whose real deps are now all
\* DONE is released back into the ready pool.
Unblock(t) ==
    /\ status[t] = "HELD"
    /\ ~OnCycle(t)
    /\ ~HasPhantom(t)
    /\ DepsAllDone(status, t)
    /\ status' = [status EXCEPT ![t] = "READY"]
    /\ UNCHANGED <<wedges, wdecision, wphase, seenBy>>

\* The now-ready task is worked to completion.
Progress(t) ==
    /\ status[t] = "READY"
    /\ status' = [status EXCEPT ![t] = "DONE"]
    /\ UNCHANGED <<wedges, wdecision, wphase, seenBy>>

\* Terminal idle so an all-settled graph is not read as a deadlock. UNCHANGED
\* over the full `vars` (not just `status`) so this stutter-step keeps
\* self-enabled forever regardless of write-time progress, giving TLC a
\* successor state in every configuration.
Terminal ==
    /\ \A t \in Tasks : status[t] \in {"DONE", "HUMAN"}
    /\ UNCHANGED vars

(***************************************************************************)
(* Write-time actions: AddDep modeled as a check-then-write TOCTOU race.       *)
(*                                                                         *)
(* CheckDep(w): writer w computes a cycle verdict for its proposed edge        *)
(* against the graph AS OBSERVED right now, and records it -- but does not     *)
(* yet write the edge. This is the read-half of links.go's                    *)
(* `l.Deps = append(l.Deps, e); if cycle(l.Deps) != nil { undo }` sequence,     *)
(* split into two steps so a concurrent writer's commit can land in between.   *)
(***************************************************************************)
CheckDep(w) ==
    /\ wphase[w] = "init"
    /\ LET cand == wedges \cup {ProposedEdge[w]} IN
        wdecision' = [wdecision EXCEPT ![w] = IF WHasCycle(cand) THEN "deny" ELSE "allow"]
    /\ wphase' = [wphase EXCEPT ![w] = "checked"]
    /\ UNCHANGED <<status, wedges, seenBy>>

\* WriteDep(w): writer w commits (or refuses) its proposed edge.
\*   AtomicWrite = TRUE  -> re-validate the verdict against the LIVE graph in
\*     this same atomic step (matches links.go: the check and the commit/undo
\*     happen with no yield of control in between) -- always safe.
\*   AtomicWrite = FALSE -> trust the STALE verdict from CheckDep, ignoring any
\*     edge another writer committed in the meantime -- the race.
WriteDep(w) ==
    /\ wphase[w] = "checked"
    /\ LET liveCand == wedges \cup {ProposedEdge[w]} IN
        wedges' =
            IF AtomicWrite
            THEN (IF WHasCycle(liveCand) THEN wedges ELSE liveCand)
            ELSE (IF wdecision[w] = "allow" THEN liveCand ELSE wedges)
    /\ wphase' = [wphase EXCEPT ![w] = "done"]
    /\ UNCHANGED <<status, wdecision, seenBy>>

(***************************************************************************)
(* Write-time actions: LinkSubmodules writes a pair's two directions.         *)
(*   AtomicLink = TRUE  -> LinkBoth writes both directions in ONE step (the    *)
(*     only enabled link action) -- no partial link is ever observable,        *)
(*     matching `LinkSubmodules(a, b)` appending both ids before one `Save`.    *)
(*   AtomicLink = FALSE -> LinkOneDirection/LinkOtherDirection write each       *)
(*     direction as a SEPARATE step, exposing the non-reciprocal intermediate.  *)
(***************************************************************************)
LinkBoth(p) ==
    /\ AtomicLink
    /\ <<p[1], p[2]>> \notin seenBy
    /\ seenBy' = seenBy \cup {<<p[1], p[2]>>, <<p[2], p[1]>>}
    /\ UNCHANGED <<status, wedges, wdecision, wphase>>

LinkOneDirection(p) ==
    /\ ~AtomicLink
    /\ <<p[1], p[2]>> \notin seenBy
    /\ seenBy' = seenBy \cup {<<p[1], p[2]>>}
    /\ UNCHANGED <<status, wedges, wdecision, wphase>>

LinkOtherDirection(p) ==
    /\ ~AtomicLink
    /\ <<p[1], p[2]>> \in seenBy
    /\ <<p[2], p[1]>> \notin seenBy
    /\ seenBy' = seenBy \cup {<<p[2], p[1]>>}
    /\ UNCHANGED <<status, wedges, wdecision, wphase>>

Next ==
    \/ \E t \in Tasks : Yield(t)
    \/ \E t \in Tasks : DirectDone(t)
    \/ \E t \in Tasks : Unblock(t)
    \/ \E t \in Tasks : Progress(t)
    \/ \E w \in Writers : CheckDep(w)
    \/ \E w \in Writers : WriteDep(w)
    \/ \E p \in LinkPairs : LinkBoth(p)
    \/ \E p \in LinkPairs : LinkOneDirection(p)
    \/ \E p \in LinkPairs : LinkOtherDirection(p)
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
