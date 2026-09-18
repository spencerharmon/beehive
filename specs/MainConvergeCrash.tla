------------------------- MODULE MainConvergeCrash -------------------------
(***************************************************************************)
(* Crash-window fidelity layer for the main-convergence protocol.          *)
(*                                                                         *)
(* WHY THIS MODULE EXISTS (the gap MainConvergence.tla missed):            *)
(* MainConvergence.tla's `PublishConverging` advances BOTH anchors in a     *)
(* single atomic step (mainLocal' = merged /\ mainRemote' = merged). Git    *)
(* has no such primitive: the real direct-on-primary write path is three    *)
(* separate git invocations --                                             *)
(*     SyncMainFromRemote  (merge remote into local)                       *)
(*     CommitPaths         (commit onto primary main -> LOCAL advances)     *)
(*     PublishPrimaryMain  (push -> REMOTE advances)                        *)
(* -- and the process can die (kill / MaxTurns / push error / a truncated   *)
(* transcript) BETWEEN CommitPaths and PublishPrimaryMain. In that window   *)
(* local main holds a commit the remote does not, with no rollback. Another *)
(* worktree publish then advances the remote off the shared base, and the   *)
(* two anchors FORK -- which beehived's ff-only pullMain can never cross,    *)
(* freezing local main permanently (the 2026-09-16 pillar incident).       *)
(*                                                                         *)
(* Because the atomic action could not express the inter-step crash, the    *)
(* old spec proved a protocol the code does not implement. This module      *)
(* restores fidelity: the publish is DECOMPOSED into its real ordered       *)
(* steps, a WriterCrash may fire between them, and a concurrent ExternalPush *)
(* (another host / another honeybee worktree publish to origin) and the     *)
(* ff-only PullMain run alongside.                                         *)
(*                                                                         *)
(* CONSTANT PushFirst selects the ordering:                                *)
(*   FALSE = the CURRENT code (CommitPaths advances LOCAL, then push        *)
(*           advances REMOTE). Local goes ahead of remote in the crash       *)
(*           window -> LocalNeverAhead and Reconcilable are violated.        *)
(*   TRUE  = the FIX (author on a staging ref, PUSH advances REMOTE first,   *)
(*           then fast-forward LOCAL up to remote). Local is only ever set   *)
(*           to a subset-or-equal of remote, so every reachable state --     *)
(*           including every crash point -- keeps local an ancestor of       *)
(*           remote. No fork is reachable.                                  *)
(*                                                                         *)
(* Commits are abstracted to the SET of artifacts they contain (as in       *)
(* MainConvergence.tla): fast-forward = superset, a fork = two incomparable  *)
(* sets. This proves reconcilability / no-silent-loss, not content-level     *)
(* merge correctness (out of scope).                                        *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS
    Artifacts,   \* finite set of authorable work items (opaque commit contents)
    PushFirst,   \* TRUE: fix ordering (remote advances before local); FALSE: current
    HealOnFork   \* TRUE: beehived's reconcile MERGE-heals a detected fork (defense 2);
                 \*   FALSE: the current ff-only pullMain that records a fork and
                 \*   proceeds without merging, freezing local main permanently.

VARIABLES
    mainLocal,   \* contents of local primary main (beehived's committed tree)
    mainRemote,  \* contents of remote/main (the shared ref)
    authored,    \* every artifact any writer ever committed (conserved)
    wp,          \* writer phase of the in-flight direct-on-primary publish
    pend         \* the artifact currently mid-publish ("none" when idle)

vars == <<mainLocal, mainRemote, authored, wp, pend>>

NoArtifact == "none"   \* sentinel outside Artifacts (model values a1..a3)

FastForward(old, new) == old \subseteq new

TypeOK ==
    /\ mainLocal  \subseteq Artifacts
    /\ mainRemote \subseteq Artifacts
    /\ authored   \subseteq Artifacts
    /\ wp \in {"idle", "committed", "pushed"}
    /\ (pend \in Artifacts \/ pend = NoArtifact)

(***************************************************************************)
(* Safety                                                                  *)
(***************************************************************************)

\* No fork: the two anchors stay comparable (one an ancestor/subset of the
\* other) -- the exact condition ff-only pullMain can cross. Its negation is
\* the frozen-fork state of the pillar incident.
Reconcilable == (mainLocal \subseteq mainRemote) \/ (mainRemote \subseteq mainLocal)

\* The crisp guarantee the fix maintains and the current ordering breaks:
\* local main is never AHEAD of remote. If it holds at every reachable state,
\* a crash can only ever leave local BEHIND remote, which ff-only pullMain
\* heals -- so Reconcilable is implied and no fork is ever manufactured.
LocalNeverAhead == mainLocal \subseteq mainRemote

\* Nothing committed vanishes from both anchors.
NoSilentLoss == authored \subseteq (mainLocal \cup mainRemote)

(***************************************************************************)
(* Init                                                                    *)
(***************************************************************************)
Init ==
    /\ mainLocal  = {}
    /\ mainRemote = {}
    /\ authored   = {}
    /\ wp   = "idle"
    /\ pend = NoArtifact

(***************************************************************************)
(* Shared actions                                                          *)
(***************************************************************************)

\* Another host, or another honeybee/editor worktree publishing to origin:
\* a fetch-merged, fast-forward advance of the REMOTE only. This is the
\* concurrent advance that turns a "local ahead" window into a true fork.
ExternalPush ==
    \E a \in Artifacts \ authored :
        /\ mainRemote' = mainRemote \cup {a}
        /\ authored'   = authored \cup {a}
        /\ UNCHANGED <<mainLocal, wp, pend>>

\* beehived background pullMain: git pull --ff-only. Advances local when the
\* remote is strictly ahead (ff possible). On a FORK it records divergence and
\* PROCEEDS without merging -- the ff-only-cannot-cross seam that freezes local
\* main. Modeled as a no-op when incomparable (no state change), so a fork it
\* cannot heal simply persists.
PullMainFFOnly ==
    /\ FastForward(mainLocal, mainRemote)
    /\ mainLocal /= mainRemote
    /\ mainLocal' = mainRemote
    /\ UNCHANGED <<mainRemote, authored, wp, pend>>

\* Defense 2: beehived's background reconcile CONVERGES the two anchors by merge
\* whenever they differ -- SyncMainFromRemote (merge remote into local) then
\* PublishPrimaryMain (push the result to remote), so both reach the UNION. This
\* is bidirectional on purpose: it heals a true fork (incomparable anchors, which
\* ff-only pullMain cannot cross) AND drains a local-ahead state (a commit stuck
\* on local main that the crash window left un-pushed -- the precursor a later
\* ExternalPush turns into a fork). The merge preserves BOTH sides, so no
\* committed artifact is ever dropped. Modeled landing the union on both anchors:
\* the reconcile is idempotent and retried each cycle, and any intermediate
\* local-ahead state is itself drained by the next cycle. Without it, a
\* local-ahead or forked state is frozen forever (the pillar incident).
HealReconcile ==
    /\ HealOnFork
    /\ mainLocal /= mainRemote
    /\ mainLocal'  = mainLocal \cup mainRemote
    /\ mainRemote' = mainLocal \cup mainRemote
    /\ UNCHANGED <<authored, wp, pend>>

(***************************************************************************)
(* CURRENT ordering (PushFirst = FALSE): commit-local-then-push.           *)
(***************************************************************************)

\* SyncMainFromRemote (merge remote in) folded with CommitPaths: the commit
\* lands on the PRIMARY MAIN -> local advances to (local U remote U {a}); the
\* remote is NOT advanced yet. Local is now ahead of remote: the fork seam.
CommitLocal ==
    /\ ~PushFirst
    /\ wp = "idle"
    /\ \E a \in Artifacts \ authored :
        /\ mainLocal' = mainLocal \cup mainRemote \cup {a}
        /\ authored'  = authored \cup {a}
        /\ wp'   = "committed"
        /\ pend' = a
        /\ UNCHANGED mainRemote

\* PublishPrimaryMain: push the local bump to the remote. Fast-forward when the
\* remote is behind local; a concurrent ExternalPush makes it non-ff, so the
\* code re-merges remote into local and retries (still ahead until it lands).
PushRemote ==
    /\ ~PushFirst
    /\ wp = "committed"
    /\ IF FastForward(mainRemote, mainLocal)
         THEN /\ mainRemote' = mainLocal
              /\ wp'   = "idle"
              /\ pend' = NoArtifact
              /\ UNCHANGED <<mainLocal, authored>>
         ELSE \* non-ff: re-merge remote and retry (still committed, still ahead)
              /\ mainLocal' = mainLocal \cup mainRemote
              /\ UNCHANGED <<mainRemote, authored, wp, pend>>

\* The pass dies after CommitLocal, before PushRemote lands (kill / MaxTurns /
\* push error / truncated transcript). Local keeps its extra commit; the remote
\* never got it. A fresh pass starts idle -- the fork is left behind, unhealed.
CommitCrash ==
    /\ ~PushFirst
    /\ wp = "committed"
    /\ wp'   = "idle"
    /\ pend' = NoArtifact
    /\ UNCHANGED <<mainLocal, mainRemote, authored>>

(***************************************************************************)
(* FIX ordering (PushFirst = TRUE): author-on-staging, push-remote-first,   *)
(* then fast-forward local. Local is never set to anything but a subset-or-  *)
(* equal of remote, so it can never be ahead -- no fork is reachable.        *)
(***************************************************************************)

\* Author the commit on a STAGING ref off (local U remote) and push it to
\* remote/main. The push is the atomic linearization point: the staging ref is
\* invisible to both anchors until the push lands, at which moment the REMOTE
\* advances. Local main is deliberately NOT moved here.
StageAndPush ==
    /\ PushFirst
    /\ wp = "idle"
    /\ \E a \in Artifacts \ authored :
        /\ mainRemote' = mainLocal \cup mainRemote \cup {a}
        /\ authored'   = authored \cup {a}
        /\ wp'   = "pushed"
        /\ pend' = a
        /\ UNCHANGED mainLocal

\* UpdateLocalMain: fast-forward local up to the remote it just published to.
\* Enabled only as a fast-forward (remote is a superset of local), which the
\* ordering guarantees.
AdvanceLocal ==
    /\ PushFirst
    /\ wp = "pushed"
    /\ FastForward(mainLocal, mainRemote)
    /\ mainLocal' = mainRemote
    /\ wp'   = "idle"
    /\ pend' = NoArtifact
    /\ UNCHANGED <<mainRemote, authored>>

\* The pass dies after the push, before AdvanceLocal. The remote already has
\* the commit; local is merely BEHIND -- ff-only pullMain heals it. No fork.
PushedCrash ==
    /\ PushFirst
    /\ wp = "pushed"
    /\ wp'   = "idle"
    /\ pend' = NoArtifact
    /\ UNCHANGED <<mainLocal, mainRemote, authored>>

(***************************************************************************)
(* Terminal idle so a fully-converged run is not read as deadlock.         *)
(***************************************************************************)
Done ==
    /\ authored = Artifacts
    /\ mainLocal = mainRemote
    /\ wp = "idle"
    /\ UNCHANGED vars

Next ==
    \/ ExternalPush
    \/ PullMainFFOnly
    \/ HealReconcile
    \/ CommitLocal
    \/ PushRemote
    \/ CommitCrash
    \/ StageAndPush
    \/ AdvanceLocal
    \/ PushedCrash
    \/ Done

(***************************************************************************)
(* Liveness (fixed cfg only): the system always fully converges.           *)
(* WF on PullMainFFOnly guarantees local catches up to remote even under a  *)
(* storm of PushedCrash preempting AdvanceLocal.                           *)
(***************************************************************************)
Converged == mainLocal = mainRemote
EventuallyConverged == <>[](authored = Artifacts /\ Converged)

Fairness ==
    /\ WF_vars(StageAndPush)
    /\ WF_vars(AdvanceLocal)
    /\ WF_vars(PullMainFFOnly)
    /\ WF_vars(HealReconcile)
    /\ WF_vars(PushRemote)
    /\ SF_vars(CommitLocal)

Spec == Init /\ [][Next]_vars /\ Fairness
=============================================================================
