--------------------------- MODULE PlanConvergence ---------------------------
(***************************************************************************)
(* Layer 2 (part c): the reconcile-vs-status-flip merge race on structured   *)
(* PLAN.md CONTENT itself -- as opposed to MainConvergence.tla (Layer 1,      *)
(* the raw `main` ref) or TaskStatus.tla (Layer 2a, a single task's own       *)
(* legal edges). A reconcile pass rewrites PLAN.md wholesale from ROI.md      *)
(* while work/review/arbitration passes concurrently flip individual task    *)
(* STATUS fields and an operator/maintenance pass leans DONE-task bodies      *)
(* (ArchiveDone, internal/plan/archive.go); publish 3-way-merges whatever     *)
(* lands (internal/swarm/swarm.go reconcile dedup-skip ~:604 is the existing  *)
(* partial mitigation; internal/plan/compat.go Candidates is the selector     *)
(* reading whatever PLAN.md state actually lands).                           *)
(*                                                                         *)
(* Failure class = silent lost update / status regression on structured      *)
(* PLAN CONTENT (the NoSilentLoss shape of MainConvergence.tla, one layer     *)
(* up: MainConvergence proves the RAW REF never forks/loses a committed       *)
(* artifact SET; this module proves the STRUCTURED FIELDS a git merge         *)
(* algorithm treats as opaque text -- a task's status, its archived-ness --   *)
(* also survive a concurrent whole-plan rewrite). Three concrete shapes:      *)
(*   - a committed status transition (say REVIEW->DONE, landed on main by a   *)
(*     review pass) gets silently reverted by a reconcile pass whose own      *)
(*     snapshot of PLAN.md predates that transition, if the reconcile         *)
(*     publish is a blind whole-file overwrite of its stale-base-derived      *)
(*     content rather than a true per-field (line-level) merge;               *)
(*   - an ArchiveDone lean (a DONE task's narrative lifted out of its body)   *)
(*     gets silently undone -- "resurrected" -- by the same stale-overwrite    *)
(*     mechanism, because the archived-ness lives in the same PLAN.md text     *)
(*     a naive reconcile rewrite clobbers wholesale;                          *)
(*   - two reconcile passes race the SAME ROI delta: without the dedup-skip    *)
(*     re-check (re-pull + prefix-compare against the FRESH stamp before        *)
(*     publishing, not the stale snapshot), a second, already-superseded       *)
(*     session still executes its own redundant publish -- the "zero-progress *)
(*     reconcile pass" defect the swarm.go comment names, and precisely the    *)
(*     window in which ITS OWN stale-base overwrite (if not merge-safe) could  *)
(*     ALSO clobber whatever landed after its snapshot.                       *)
(*                                                                         *)
(* Fidelity: git's actual 3-way text merge is line-based and per-hunk, so a   *)
(* reconcile edit and a concurrent status-flip edit on DISTINCT lines merge   *)
(* cleanly with no textual conflict at all -- this is exactly ThreeWayMerge   *)
(* = TRUE below: the reconcile publish only ever changes the lines its own    *)
(* ROI delta actually touches (a fresh re-read of the fields it does not      *)
(* touch), never a blind snapshot-derived overwrite of the whole file. The    *)
(* buggy counterfactual (ThreeWayMerge = FALSE) is what a reconcile publish    *)
(* that regenerated and force-wrote the ENTIRE file from its (possibly        *)
(* stale) snapshot would do -- silently discarding any field it did not       *)
(* itself intend to change but which changed concurrently underneath it.      *)
(* DedupGuard models the reconcile-dedup-skip mitigation already in           *)
(* swarm.go ~:604 (Runner.reconciled): re-pull + prefix-check the FRESH        *)
(* stamp immediately before publishing and skip a no-op fold entirely.        *)
(*                                                                         *)
(* CONSTANTS DedupGuard and ThreeWayMerge independently select the fixed vs   *)
(* buggy protocol for each mechanism; see the .cfg files.                     *)
(***************************************************************************)
EXTENDS Naturals, TLC

CONSTANTS
    RSessions,      \* the set of concurrent reconcile sessions racing the same ROI delta
    DedupGuard,     \* TRUE: a reconcile publish re-checks the FRESH ROI stamp and skips
                     \*   a no-op fold instead of redundantly re-publishing (swarm.go ~:604)
    ThreeWayMerge   \* TRUE: reconcile publish touches only the lines its own delta owns
                     \*   (real per-field git merge). FALSE: reconcile publish blindly
                     \*   overwrites the whole plan from its (possibly stale) snapshot,
                     \*   clobbering any concurrent status-flip / archive-lean it never
                     \*   intended to touch.

VARIABLES
    t1Status,       \* status of task t1, the status-flip race subject (TODO/REVIEW/DONE)
    t1HighWater,    \* sticky: the highest status level ever legitimately published for t1
    t2Archived,     \* whether task t2's DONE narrative has been leaned (ArchiveDone) on main
    t2EverArchived, \* sticky: was t2 ever archived
    roiVersion,     \* the ROI delta folded into main so far (0 = not yet, 1 = the one delta)
    rPhase,         \* [RSessions -> {"idle", "snapshotted"}]
    rBaseT1,        \* [RSessions -> Statuses] each session's snapshot of t1Status at pull time
    rBaseT2Arch,    \* [RSessions -> BOOLEAN] each session's snapshot of t2Archived at pull time
    rBaseVersion,   \* [RSessions -> 0..1] each session's snapshot of roiVersion at pull time
    publishCount    \* total number of reconcile publishes that actually executed a fold
                     \* (as opposed to a dedup-skip no-op) -- the redundancy counter

vars == <<t1Status, t1HighWater, t2Archived, t2EverArchived, roiVersion,
          rPhase, rBaseT1, rBaseT2Arch, rBaseVersion, publishCount>>

Statuses == {"TODO", "REVIEW", "DONE"}

StatusLevel(s) == CASE s = "TODO" -> 0 [] s = "REVIEW" -> 1 [] s = "DONE" -> 2

\* Cardinality is only used for a loose type bound on publishCount; TLC's
\* model-checked state space is finite regardless (bounded by 0..1 roiVersion
\* + finite RSessions), so a generous constant bound avoids needing FiniteSets
\* just for TypeOK.
Cardinality_RSessions == 8

TypeOK ==
    /\ t1Status \in Statuses
    /\ t1HighWater \in 0..2
    /\ t2Archived \in BOOLEAN
    /\ t2EverArchived \in BOOLEAN
    /\ roiVersion \in 0..1
    /\ rPhase \in [RSessions -> {"idle", "snapshotted"}]
    /\ rBaseT1 \in [RSessions -> Statuses]
    /\ rBaseT2Arch \in [RSessions -> BOOLEAN]
    /\ rBaseVersion \in [RSessions -> 0..1]
    /\ publishCount \in 0..(Cardinality_RSessions)

(***************************************************************************)
(* Safety invariants                                                       *)
(***************************************************************************)

\* A committed status transition survives a concurrent reconcile rewrite: t1's
\* published status never regresses below the highest level any WorkFlip ever
\* legitimately reached. This is NoSilentLoss for structured PLAN content: the
\* only thing that can lower t1Status is a stale reconcile whole-file overwrite
\* (ThreeWayMerge = FALSE); a true per-field merge never touches t1's lines at
\* all, since no ROI delta in this model targets t1.
NoLostStatus == StatusLevel(t1Status) = t1HighWater

\* Archived tasks never resurrect: once ArchiveDone has leaned t2's DONE
\* narrative out of PLAN.md, it never comes back -- t2Archived is one-way.
\* A stale reconcile whole-file overwrite that predates the archive commit is
\* the only way t2Archived could flip back to FALSE.
NoResurrect == t2EverArchived => t2Archived

\* Reconcile is idempotent when a peer already applied it: at most one
\* session's publish ever actually executes the fold for this one ROI delta
\* -- never a redundant re-publish burning a whole zero-progress session/turn,
\* which is also the exact window a non-merge-safe overwrite could clobber
\* whatever landed after its stale snapshot. Whether this holds is exactly
\* what DedupGuard selects (see the .cfg files): with the guard, a second,
\* already-superseded session re-checks the FRESH stamp and no-ops; without
\* it, it blindly redundantly re-publishes.
NoRedundantReconcile == publishCount <= 1

(***************************************************************************)
(* Init                                                                    *)
(***************************************************************************)
Init ==
    /\ t1Status = "TODO"
    /\ t1HighWater = 0
    /\ t2Archived = FALSE
    /\ t2EverArchived = FALSE
    /\ roiVersion = 0
    /\ rPhase = [s \in RSessions |-> "idle"]
    /\ rBaseT1 = [s \in RSessions |-> "TODO"]
    /\ rBaseT2Arch = [s \in RSessions |-> FALSE]
    /\ rBaseVersion = [s \in RSessions |-> 0]
    /\ publishCount = 0

(***************************************************************************)
(* Actions                                                                 *)
(***************************************************************************)

\* A work/review pass advances t1's status directly (a single-task PLAN.md
\* edit that lands on main independent of any reconcile fold). Monotonic:
\* TODO -> REVIEW -> DONE, never touched by any ROI delta in this model.
WorkFlip ==
    /\ StatusLevel(t1Status) < 2
    /\ t1Status' = IF t1Status = "TODO" THEN "REVIEW" ELSE "DONE"
    /\ t1HighWater' = StatusLevel(t1Status')
    /\ UNCHANGED <<t2Archived, t2EverArchived, roiVersion,
                   rPhase, rBaseT1, rBaseT2Arch, rBaseVersion, publishCount>>

\* An operator/maintenance pass leans t2's DONE narrative (ArchiveDone): a
\* one-way PLAN.md body edit, independent of any reconcile fold.
ArchiveLean ==
    /\ ~t2Archived
    /\ t2Archived' = TRUE
    /\ t2EverArchived' = TRUE
    /\ UNCHANGED <<t1Status, t1HighWater, roiVersion,
                   rPhase, rBaseT1, rBaseT2Arch, rBaseVersion, publishCount>>

\* A reconcile session pulls main and snapshots PLAN.md (refreshMain + parse).
\* Only meaningful while the one ROI delta this model has is still unfolded
\* (once roiVersion = 1 there is nothing left for a fresh reconcile to do --
\* a real session would see its stamp already prefixes ROI head and never even
\* select the reconcile kind).
ReconcileSnapshot(s) ==
    /\ rPhase[s] = "idle"
    /\ roiVersion = 0
    /\ rPhase' = [rPhase EXCEPT ![s] = "snapshotted"]
    /\ rBaseT1' = [rBaseT1 EXCEPT ![s] = t1Status]
    /\ rBaseT2Arch' = [rBaseT2Arch EXCEPT ![s] = t2Archived]
    /\ rBaseVersion' = [rBaseVersion EXCEPT ![s] = roiVersion]
    /\ UNCHANGED <<t1Status, t1HighWater, t2Archived, t2EverArchived,
                   roiVersion, publishCount>>

\* A reconcile session publishes its fold. DedupGuard: re-check the FRESH
\* (not snapshotted) roiVersion right before publishing; if a peer already
\* landed this delta, skip -- no-op, no redundant publish. Otherwise fold:
\*   ThreeWayMerge = TRUE  -- the real per-field git merge: only the fields
\*     this session's OWN ROI delta owns (roiVersion) actually change; t1Status
\*     and t2Archived are left exactly as they currently (freshly) stand,
\*     because the delta's textual diff never touches those lines.
\*   ThreeWayMerge = FALSE -- the buggy whole-file overwrite: the session
\*     publishes t1Status/t2Archived FROM ITS OWN STALE SNAPSHOT, silently
\*     discarding any WorkFlip/ArchiveLean that landed after that snapshot.
ReconcilePublish(s) ==
    /\ rPhase[s] = "snapshotted"
    /\ LET target == rBaseVersion[s] + 1
       IN IF DedupGuard /\ roiVersion >= target
            THEN /\ rPhase' = [rPhase EXCEPT ![s] = "idle"]
                 /\ UNCHANGED <<t1Status, t1HighWater, t2Archived, t2EverArchived,
                                roiVersion, publishCount, rBaseT1, rBaseT2Arch, rBaseVersion>>
            ELSE /\ rPhase' = [rPhase EXCEPT ![s] = "idle"]
                 /\ publishCount' = publishCount + 1
                 /\ roiVersion' = target
                 /\ IF ThreeWayMerge
                      THEN UNCHANGED <<t1Status, t2Archived>>
                      ELSE /\ t1Status' = rBaseT1[s]
                           /\ t2Archived' = rBaseT2Arch[s]
                 /\ UNCHANGED <<t1HighWater, t2EverArchived, rBaseT1, rBaseT2Arch, rBaseVersion>>

\* Terminal idle: once every session is idle and there is nothing left to
\* snapshot/publish, stutter -- this keeps a fully-converged state from
\* reading as a spurious deadlock (TLC's default deadlock check).
Done ==
    /\ \A s \in RSessions : rPhase[s] = "idle"
    /\ UNCHANGED vars

Next ==
    \/ WorkFlip
    \/ ArchiveLean
    \/ \E s \in RSessions : ReconcileSnapshot(s)
    \/ \E s \in RSessions : ReconcilePublish(s)
    \/ Done

Spec == Init /\ [][Next]_vars

=============================================================================
