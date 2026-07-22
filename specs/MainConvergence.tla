-------------------------- MODULE MainConvergence --------------------------
(***************************************************************************)
(* Layer 1 of the beehive formal protocol: the shared fast-forward ref.    *)
(*                                                                         *)
(* Models the two `main` anchors every hive writer converges on -- the     *)
(* local primary main (the tree beehived owns, target of                   *)
(* receive.denyCurrentBranch=updateInstead) and remote/main (gitea/main,   *)
(* the shared ref other hosts and external pushers converge on) -- and the *)
(* several actors that write them: honeybee/editor worktree publishes,     *)
(* direct-on-primary CLI verbs, beehived's ff-only background pullMain, an  *)
(* external push, and an external force-rewrite.                           *)
(*                                                                         *)
(* Fidelity note (the "no magic variables" rule): each writer's view of    *)
(* main is the ref it last observed, not live global state; the buggy      *)
(* direct-on-primary path authors on a possibly-stale local main WITHOUT   *)
(* first pulling the remote. That stale base is the fork seam.  Commits    *)
(* are abstracted to the SET of artifacts they contain; fast-forward =     *)
(* superset; a fork = two incomparable sets.  This proves reconcilability  *)
(* and no-silent-loss; it deliberately says nothing about content-level    *)
(* merge correctness (out of scope -- that is agent quality, not protocol).*)
(*                                                                         *)
(* Reproduces (before fix) / locks (after fix):                            *)
(*   - b48b927  git: heal main forks that ff-only pullMain silently drops  *)
(*   - f152b9b  route direct-on-primary-main CLI verbs through convergence *)
(*   - f8e7828  pre-receive: refuse non-fast-forward force-rewind of main   *)
(*                                                                         *)
(* CONSTANTS pick which protocol/guards are in force so the SAME module     *)
(* reproduces the broken behavior (invariant violated, with a trace) and   *)
(* the fixed behavior (no error).  See the .cfg files.                     *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS
    Artifacts,        \* finite set of authorable work items (opaque commit contents)
    Fixed,            \* TRUE: hive writers SyncMainFromRemote/merge before authoring
    PreReceiveGuard   \* TRUE: server refuses non-fast-forward rewrites of refs/heads/main

VARIABLES
    mainLocal,        \* contents of local primary main (beehived's checked-out tree)
    mainRemote,       \* contents of remote/main (the shared ref)
    authored          \* every artifact any writer ever committed (conserved quantity)

vars == <<mainLocal, mainRemote, authored>>

TypeOK ==
    /\ mainLocal  \subseteq Artifacts
    /\ mainRemote \subseteq Artifacts
    /\ authored   \subseteq Artifacts

\* A ref only ever fast-forwards: it may advance to a superset of its contents.
FastForward(old, new) == old \subseteq new

(***************************************************************************)
(* Safety invariants                                                       *)
(***************************************************************************)

\* The two anchors must stay reconcilable: one an ancestor (subset) of the
\* other. A state where neither contains the other is a FORK -- the exact
\* condition ff-only pullMain can no longer cross, which is what eventually
\* drops a line silently. This is the leading safety property of the layer.
Reconcilable == (mainLocal \subseteq mainRemote) \/ (mainRemote \subseteq mainLocal)

\* No committed artifact is ever silently dropped from BOTH anchors.
NoSilentLoss == authored \subseteq (mainLocal \cup mainRemote)

(***************************************************************************)
(* Init                                                                    *)
(***************************************************************************)
Init ==
    /\ mainLocal  = {}
    /\ mainRemote = {}
    /\ authored   = {}

(***************************************************************************)
(* Actions                                                                 *)
(***************************************************************************)

\* An external writer (another host, or an operator push to gitea/main) that
\* fetch-merged first: it only ADDS, a pure fast-forward of the remote.
ExternalPush ==
    \E a \in Artifacts \ authored :
        /\ mainRemote' = mainRemote \cup {a}
        /\ authored'   = authored \cup {a}
        /\ UNCHANGED mainLocal

\* Fixed protocol: pull-merge remote into the base FIRST (SyncMainFromRemote /
\* PublishToMain fetch+merge), author, publish to remote, advance local main.
\* Both anchors end containing everything -- always reconcilable.
PublishConverging ==
    \E a \in Artifacts \ authored :
        LET merged == mainLocal \cup mainRemote \cup {a} IN
        /\ mainLocal'  = merged
        /\ mainRemote' = merged
        /\ authored'   = authored \cup {a}

\* Buggy direct-on-primary path (CLI verb committing via CommitPaths on the
\* primary main WITHOUT SyncMainFromRemote first). Only local advances; the
\* remote is not merged in. Authoring on a stale base is the fork seam.
PublishDirectStale ==
    \E a \in Artifacts \ authored :
        /\ mainLocal' = mainLocal \cup {a}
        /\ authored'  = authored \cup {a}
        /\ UNCHANGED mainRemote

\* beehived background pullMain: git pull --ff-only. Advances local when the
\* remote is strictly ahead (ff possible). On a FORK (incomparable) it records
\* divergence and PROCEEDS -- it never merges (the ff-only-cannot-cross seam).
PullMainFFOnly ==
    /\ FastForward(mainLocal, mainRemote)
    /\ mainLocal /= mainRemote
    /\ mainLocal' = mainRemote
    /\ UNCHANGED <<mainRemote, authored>>

\* PublishPrimaryMain push of the local bump to the remote. Succeeds only on
\* fast-forward; a non-ff push is refused (never force). Advances the remote.
PushPrimary ==
    /\ FastForward(mainRemote, mainLocal)
    /\ mainRemote /= mainLocal
    /\ mainRemote' = mainLocal
    /\ UNCHANGED <<mainLocal, authored>>

\* External force-push that REWRITES main non-fast-forward, dropping artifacts.
\* PreReceiveGuard = TRUE refuses it (f8e7828); FALSE lets it rewind and lose work.
ExternalForceRewind ==
    /\ ~PreReceiveGuard
    /\ \E new \in SUBSET mainRemote :
        /\ new /= mainRemote
        /\ mainRemote' = new
    /\ UNCHANGED <<mainLocal, authored>>

\* Terminal idle once every artifact is authored and both anchors agree, so a
\* healthy fully-converged run does not read as a deadlock.
Done ==
    /\ authored = Artifacts
    /\ mainLocal = mainRemote
    /\ UNCHANGED vars

Next ==
    \/ ExternalPush
    \/ (IF Fixed THEN PublishConverging ELSE PublishDirectStale)
    \/ PullMainFFOnly
    \/ PushPrimary
    \/ ExternalForceRewind
    \/ Done

(***************************************************************************)
(* Liveness: every writer's work eventually reaches BOTH anchors.          *)
(* Checked only in the fixed cfg (the buggy fork cannot converge).         *)
(***************************************************************************)
Converged == mainLocal = mainRemote
EventuallyConverged == <>[](authored = Artifacts /\ Converged)

Fairness ==
    /\ WF_vars(PublishConverging)
    /\ WF_vars(PullMainFFOnly)
    /\ WF_vars(PushPrimary)

Spec == Init /\ [][Next]_vars /\ Fairness
=============================================================================
