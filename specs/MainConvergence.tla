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
(* Authoring is NON-ATOMIC (the staged-heal fidelity fix): a writer first  *)
(* MUTATES the local primary working tree (stages content) and only later  *)
(* COMMITS it into mainLocal. Between those two steps the content lives in  *)
(* a staged-but-uncommitted view distinct from committed mainLocal. The    *)
(* dirty-tree heal (`healLocalMain`, `git reset --hard HEAD`) discards      *)
(* anything staged-but-uncommitted -- modeled by HealResetHard. When the    *)
(* mutation and commit are not atomic and a heal interleaves between them,  *)
(* the in-flight write is SILENTLY LOST. That is the 2026-07-29 `submodule  *)
(* add` orphan-gitlink loss: an operator/CLI write staged into the live     *)
(* primary tree, eaten by the reset-dirty-tree heal before it was          *)
(* committed on an anchor. NoStagedLoss makes that loss a checkable safety  *)
(* violation; the fixed model authors atomically (stage+commit fused, or in *)
(* a worktree the heal never touches) so no begun write is ever dropped.    *)
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
(*   - 2026-07-29 submodule-add orphan-gitlink: staged-but-uncommitted     *)
(*     primary-tree write eaten by the reset-dirty-tree heal (NoStagedLoss)*)
(*                                                                         *)
(* CONSTANTS pick which protocol/guards are in force so the SAME module     *)
(* reproduces the broken behavior (invariant violated, with a trace) and   *)
(* the fixed behavior (no error).  See the .cfg files.                     *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS
    Artifacts,        \* finite set of authorable work items (opaque commit contents)
    Fixed,            \* TRUE: hive writers SyncMainFromRemote/merge before authoring
    PreReceiveGuard,  \* TRUE: server refuses non-fast-forward rewrites of refs/heads/main
    StagedAtomic      \* TRUE: primary-tree mutation is committed atomically (or in a
                      \*   worktree the heal never touches) so no begun write is left
                      \*   staged-but-uncommitted for HealResetHard to discard.

VARIABLES
    mainLocal,        \* contents of local primary main (beehived's COMMITTED tree)
    mainRemote,       \* contents of remote/main (the shared ref)
    authored,         \* every artifact any writer ever committed (conserved quantity)
    mainStaged,       \* staged-but-uncommitted view of the local primary working tree
                      \*   (always a superset of mainLocal until a heal resets it)
    begun             \* every artifact some local writer has STAGED into the primary
                      \*   tree (begun authoring); it must never vanish uncommitted.

vars == <<mainLocal, mainRemote, authored, mainStaged, begun>>

TypeOK ==
    /\ mainLocal  \subseteq Artifacts
    /\ mainRemote \subseteq Artifacts
    /\ authored   \subseteq Artifacts
    /\ mainStaged \subseteq Artifacts
    /\ begun      \subseteq Artifacts

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

\* No artifact a writer has BEGUN (staged into the primary tree) is ever
\* dropped from BOTH the staged view and both committed anchors without being
\* committed. The dirty-tree heal (HealResetHard) may only discard staged
\* content that is still recoverable -- content some writer already committed,
\* or content still visible in the staged view for a writer to re-commit --
\* never a silently-lost in-flight operator/CLI write. A begun artifact that
\* is in none of {mainStaged, mainLocal, mainRemote} has been eaten: exactly
\* the 2026-07-29 orphan-gitlink loss.
NoStagedLoss == begun \subseteq (mainStaged \cup mainLocal \cup mainRemote)

(***************************************************************************)
(* Init                                                                    *)
(***************************************************************************)
Init ==
    /\ mainLocal  = {}
    /\ mainRemote = {}
    /\ authored   = {}
    /\ mainStaged = {}
    /\ begun      = {}

(***************************************************************************)
(* Actions                                                                 *)
(***************************************************************************)

\* An external writer (another host, or an operator push to gitea/main) that
\* fetch-merged first: it only ADDS, a pure fast-forward of the remote.
ExternalPush ==
    \E a \in Artifacts \ authored :
        /\ mainRemote' = mainRemote \cup {a}
        /\ authored'   = authored \cup {a}
        /\ UNCHANGED <<mainLocal, mainStaged, begun>>

\* Fixed protocol: pull-merge remote into the base FIRST (SyncMainFromRemote /
\* PublishToMain fetch+merge), author, publish to remote, advance local main.
\* Both anchors end containing everything -- always reconcilable. The publish
\* commits atomically, so the staged view reflects the committed result.
PublishConverging ==
    \E a \in Artifacts \ authored :
        LET merged == mainLocal \cup mainRemote \cup {a} IN
        /\ mainLocal'  = merged
        /\ mainRemote' = merged
        /\ mainStaged' = mainStaged \cup merged
        /\ authored'   = authored \cup {a}
        /\ begun'      = begun \cup {a}

\* Buggy direct-on-primary path (CLI verb committing via CommitPaths on the
\* primary main WITHOUT SyncMainFromRemote first). Only local advances; the
\* remote is not merged in. Authoring on a stale base is the fork seam.
PublishDirectStale ==
    \E a \in Artifacts \ authored :
        /\ mainLocal'  = mainLocal \cup {a}
        /\ mainStaged' = mainStaged \cup {a}
        /\ authored'   = authored \cup {a}
        /\ begun'      = begun \cup {a}
        /\ UNCHANGED mainRemote

\* beehived background pullMain: git pull --ff-only. Advances local when the
\* remote is strictly ahead (ff possible). On a FORK (incomparable) it records
\* divergence and PROCEEDS -- it never merges (the ff-only-cannot-cross seam).
PullMainFFOnly ==
    /\ FastForward(mainLocal, mainRemote)
    /\ mainLocal /= mainRemote
    /\ mainLocal'  = mainRemote
    /\ mainStaged' = mainStaged \cup mainRemote
    /\ UNCHANGED <<mainRemote, authored, begun>>

\* PublishPrimaryMain push of the local bump to the remote. Succeeds only on
\* fast-forward; a non-ff push is refused (never force). Advances the remote.
PushPrimary ==
    /\ FastForward(mainRemote, mainLocal)
    /\ mainRemote /= mainLocal
    /\ mainRemote' = mainLocal
    /\ UNCHANGED <<mainLocal, mainStaged, authored, begun>>

\* External force-push that REWRITES main non-fast-forward, dropping artifacts.
\* PreReceiveGuard = TRUE refuses it (f8e7828); FALSE lets it rewind and lose work.
ExternalForceRewind ==
    /\ ~PreReceiveGuard
    /\ \E new \in SUBSET mainRemote :
        /\ new /= mainRemote
        /\ mainRemote' = new
    /\ UNCHANGED <<mainLocal, mainStaged, authored, begun>>

\* Stage a write into the local primary WORKING tree (a `submodule add`, a
\* CommitPaths that has written the index/worktree but not yet committed, an
\* operator edit). It marks the artifact BEGUN. When StagedAtomic the stage
\* and commit are fused -- the artifact lands on mainLocal/authored in the
\* same step, so nothing is left uncommitted for a heal to eat. When NOT
\* atomic the artifact sits staged-but-uncommitted (the buggy seam).
MutatePrimaryTree ==
    \E a \in Artifacts \ authored :
        /\ a \notin mainStaged
        /\ begun' = begun \cup {a}
        /\ IF StagedAtomic
             THEN \* atomic author: merge the remote base in and publish to BOTH
                  \* anchors in the same step (fixed convergent publish) -- no
                  \* staged-but-uncommitted window for a heal to eat.
                  LET merged == mainLocal \cup mainRemote \cup {a} IN
                  /\ mainStaged' = mainStaged \cup merged
                  /\ mainLocal'  = merged
                  /\ mainRemote' = merged
                  /\ authored'   = authored \cup {a}
             ELSE \* buggy: staged into the live tree but NOT committed.
                  /\ mainStaged' = mainStaged \cup {a}
                  /\ UNCHANGED <<mainLocal, mainRemote, authored>>

\* Commit the staged-but-uncommitted primary-tree content onto mainLocal (the
\* commit that promotes a staged write into committed history). Once committed
\* the write is durable on an anchor and can no longer be lost by a heal.
CommitStaged ==
    /\ mainStaged \ mainLocal /= {}
    /\ mainLocal' = mainStaged
    /\ authored'  = authored \cup mainStaged
    /\ UNCHANGED <<mainRemote, mainStaged, begun>>

\* The dirty-tree heal: `healLocalMain` runs `git reset --hard HEAD`, discarding
\* everything staged-but-uncommitted (resetting the staged view back to the
\* committed mainLocal). This is the reset-dirty-with-WARNING preflight guard.
\* It is SAFE only when every begun write was already committed (StagedAtomic);
\* interleaved before CommitStaged it silently eats the in-flight write, which
\* NoStagedLoss then catches as a counterexample.
HealResetHard ==
    /\ mainStaged /= mainLocal
    /\ mainStaged' = mainLocal
    /\ UNCHANGED <<mainLocal, mainRemote, authored, begun>>

\* Terminal idle once every artifact is authored, both anchors agree, and the
\* staged view is clean, so a healthy fully-converged run does not read as a
\* deadlock.
Done ==
    /\ authored = Artifacts
    /\ mainLocal = mainRemote
    /\ mainStaged = mainLocal
    /\ UNCHANGED vars

Next ==
    \/ ExternalPush
    \/ (IF Fixed THEN PublishConverging ELSE PublishDirectStale)
    \/ PullMainFFOnly
    \/ PushPrimary
    \/ ExternalForceRewind
    \/ MutatePrimaryTree
    \/ CommitStaged
    \/ HealResetHard
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
    /\ WF_vars(CommitStaged)

Spec == Init /\ [][Next]_vars /\ Fairness
=============================================================================
