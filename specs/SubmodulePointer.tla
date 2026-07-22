-------------------------- MODULE SubmodulePointer --------------------------
(***************************************************************************)
(* Layer 1 (companion): the submodule gitlink as a shared, durable pointer. *)
(*                                                                         *)
(* The superproject records exactly one commit per submodule -- its        *)
(* gitlink. The canonical invariant (docs/submodule-pointer-invariant.md): *)
(*                                                                         *)
(*   A submodule gitlink MUST ALWAYS equal the tip of that submodule's     *)
(*   configured tracked branch as PUBLISHED ON ORIGIN -- never a           *)
(*   bee-<taskid> tip, never any other commit.                             *)
(*                                                                         *)
(* Two distinct defect classes this module reproduces / locks:             *)
(*                                                                         *)
(*  (1) Dangling pointer (1a9bcea, 35442f4). A Work agent bumps the gitlink *)
(*      to its disposable bee-<taskid> tip. The runner merges that branch   *)
(*      into the tracked branch (a NEW merge commit, not the bee tip) and   *)
(*      reclaims/GCs the bee branch. The bee tip sha is now GC'd off origin *)
(*      -> the gitlink names an unresolvable commit -> `git submodule       *)
(*      update` fails -> the primary tree is unhealably dirty -> every      *)
(*      honeybee preflight aborts -> the swarm halts.                       *)
(*      Fix: the RUNNER owns the gitlink and pins it to the tracked-branch  *)
(*      tip at completion; the agent never writes it. A bump to a commit    *)
(*      not durable on origin is refused (RemoteContainsCommit/BumpGitlink).*)
(*                                                                         *)
(* Commits are abstracted to opaque ids; "durable on origin" is a set       *)
(* membership. This proves pointer durability + tracked-tip; it does NOT    *)
(* model the ambient-pointer false-DONE race (743b1c6) -- that belongs to   *)
(* Layer 2 (TaskLifecycle), where task status and review live.              *)
(*                                                                         *)
(* CONSTANT Fixed selects the runner-owns-gitlink protocol (TRUE) vs the    *)
(* old agent-bumps-pointer protocol (FALSE). See the .cfg files.           *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS
    MaxTasks,   \* bound on how many work tasks run (state-space cap)
    Fixed       \* TRUE: runner pins gitlink to tracked tip; FALSE: agent bumps bee tip

VARIABLES
    trackedTip,     \* commit id at the tip of the tracked branch on origin
    originDurable,  \* set of commit ids currently resolvable on the submodule origin
    beeTip,         \* in-flight bee-<taskid> branch tip (0 = no active work)
    beeMerged,      \* has the active bee branch been merged into the tracked branch?
    gitlink,        \* commit id the superproject records for the submodule
    nextId,         \* fresh commit-id counter
    started         \* count of work tasks started (bounded by MaxTasks)

vars == <<trackedTip, originDurable, beeTip, beeMerged, gitlink, nextId, started>>

Commits == 0..(MaxTasks*2 + 1)

TypeOK ==
    /\ trackedTip \in Commits
    /\ originDurable \subseteq Commits
    /\ beeTip \in Commits
    /\ beeMerged \in BOOLEAN
    /\ gitlink \in Commits
    /\ nextId \in Commits
    /\ started \in 0..MaxTasks

(***************************************************************************)
(* Safety invariants (both are the canonical pointer invariant restated)   *)
(***************************************************************************)

\* The recorded gitlink is always resolvable on origin -- never a GC'd sha.
PointerDurable == gitlink \in originDurable

\* The recorded gitlink is always exactly the tracked-branch tip.
PointerIsTrackedTip == gitlink = trackedTip

(***************************************************************************)
(* Init: commit 0 is the initial tracked tip; gitlink points at it.        *)
(***************************************************************************)
Init ==
    /\ trackedTip = 0
    /\ originDurable = {0}
    /\ beeTip = 0
    /\ beeMerged = FALSE
    /\ gitlink = 0
    /\ nextId = 1
    /\ started = 0

(***************************************************************************)
(* Actions                                                                 *)
(***************************************************************************)

\* A Work agent starts a task: creates a bee-<taskid> commit and pushes the
\* branch to the submodule origin (so its tip is durable while the branch lives).
StartWork ==
    /\ beeTip = 0
    /\ started < MaxTasks
    /\ beeTip' = nextId
    /\ originDurable' = originDurable \cup {nextId}
    /\ nextId' = nextId + 1
    /\ started' = started + 1
    /\ UNCHANGED <<trackedTip, beeMerged, gitlink>>

\* OLD PROTOCOL (buggy): the agent bumps the submodule pointer to its own bee
\* tip. HONEYBEE.md used to instruct exactly this ("bump the submodule pointer").
AgentBumpsBeeTip ==
    /\ ~Fixed
    /\ beeTip /= 0
    /\ gitlink' = beeTip
    /\ UNCHANGED <<trackedTip, originDurable, beeTip, beeMerged, nextId, started>>

\* The bee branch is approved and merged into the tracked branch: a NEW merge
\* commit becomes the tracked tip (durable on origin). In the FIXED protocol the
\* runner pins the gitlink to that tracked tip at this completion step; the bump
\* is refused if the target is not durable on origin (BumpGitlink guard).
MergeToTracked ==
    /\ beeTip /= 0
    /\ ~beeMerged
    /\ trackedTip' = nextId
    /\ originDurable' = originDurable \cup {nextId}
    /\ nextId' = nextId + 1
    /\ beeMerged' = TRUE
    /\ gitlink' = IF Fixed THEN nextId ELSE gitlink
    /\ UNCHANGED <<beeTip, started>>

\* The runner reclaims the merged bee branch; its disposable tip sha is GC'd off
\* origin (it was never the tracked tip, so nothing else keeps it alive).
ReclaimBranch ==
    /\ beeTip /= 0
    /\ beeMerged
    /\ originDurable' = originDurable \ {beeTip}
    /\ beeTip' = 0
    /\ beeMerged' = FALSE
    /\ UNCHANGED <<trackedTip, gitlink, nextId, started>>

\* Terminal idle once every task has run and no bee branch is in flight.
Done ==
    /\ started = MaxTasks
    /\ beeTip = 0
    /\ UNCHANGED vars

Next ==
    \/ StartWork
    \/ AgentBumpsBeeTip
    \/ MergeToTracked
    \/ ReclaimBranch
    \/ Done

Spec == Init /\ [][Next]_vars
=============================================================================
