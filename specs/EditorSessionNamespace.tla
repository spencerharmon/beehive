------------------------ MODULE EditorSessionNamespace -----------------------
(***************************************************************************)
(* Layer 3: beehived's chat-diff editor Manager and the reclaim/gc dance it   *)
(* runs over the shared beehive-root .worktrees/ directory (faithful to        *)
(* internal/editor/editor.go).  Multiple subsystems cut edit-* worktrees in    *)
(* that one shared dir; the Manager must recover/reclaim its OWN sessions       *)
(* without ever destroying an operator's in-flight edit or another subsystem's  *)
(* worktree.                                                                   *)
(*                                                                         *)
(* Three protections, each a CONSTANT toggle, each guarding one invariant:     *)
(*                                                                         *)
(*   NamespaceScoped -- the Manager owns EXACTLY the "hive-edit-" prefix and    *)
(*     never enumerates/adopts/reclaims a foreign bare edit-* worktree (the     *)
(*     resolve agent's edit-resolve-* branch, the bootstrap chat editor's       *)
(*     bare edit-* branch).  Guards NoForeignReclaim.  Off => b08c995's           *)
(*     namespace-capture half.                                                  *)
(*                                                                         *)
(*   LiveGuard -- a branch currently registered in Manager.byID is a LIVE       *)
(*     session (an operator has it open) and is NEVER reclaimed, whatever its   *)
(*     record age or worktree cleanliness.  Guards LiveSessionNeverReclaimed.   *)
(*     Off => b08c995's live-wipe half (the gc dance deletes an open-but-idle,  *)
(*     stale-record session out from under the operator -> 404 next turn).      *)
(*                                                                         *)
(*   RemoteDurable -- each turn commits the session to a tracked branch and     *)
(*     pushes it to a trusted remote (PushBranchReconciled, never force), so a  *)
(*     pending edit survives total local loss (crash / wiped scratch / other    *)
(*     host).  Guards SessionDurable.  Off => c64efe7's pre-fix: a pending      *)
(*     session that loses its local worktree is unrecoverable.                  *)
(*                                                                         *)
(* The LLM agent interior is unmodeled; only the worst-case git/fs EFFECTS      *)
(* (worktree vanished, record aged, branch present/absent locally and on the   *)
(* trusted remote) are represented, and the specs prove the Manager's guards    *)
(* defend the invariants regardless.                                          *)
(***************************************************************************)
EXTENDS TLC

CONSTANTS
    NamespaceScoped,  \* TRUE: Manager touches only its own hive-edit- namespace
    LiveGuard,        \* TRUE: a live (byID) session is never reclaimed
    RemoteDurable     \* TRUE: each turn pushes the session to a trusted remote

\* Two worktrees share the dir: "m" is a Manager (hive-edit-) session; "f" is a
\* FOREIGN subsystem's bare edit-* worktree the Manager must never touch.
Sessions == {"m", "f"}
Owner(s) == IF s = "m" THEN "mgr" ELSE "foreign"

VARIABLES
    present,      \* [Sessions -> BOOLEAN] worktree dir exists in .worktrees/
    live,         \* [Sessions -> BOOLEAN] registered in Manager.byID (operator has it open)
    recordFresh,  \* [Sessions -> BOOLEAN] persisted store record younger than TTL
    pending,      \* [Sessions -> BOOLEAN] worktree carries an unpublished operator edit
    localBranch,  \* [Sessions -> BOOLEAN] the local branch ref exists
    remoteBranch, \* [Sessions -> BOOLEAN] the branch is pushed to the trusted remote
    \* ghost history variables (record what the Manager's reclaim actually did):
    mgrDestroyedForeign, \* the Manager ever reclaimed a foreign worktree
    liveReclaimed         \* the Manager ever reclaimed a live session

vars == <<present, live, recordFresh, pending, localBranch, remoteBranch,
          mgrDestroyedForeign, liveReclaimed>>

BoolFn(f) == \A s \in Sessions : f[s] \in BOOLEAN

TypeOK ==
    /\ BoolFn(present) /\ BoolFn(live) /\ BoolFn(recordFresh)
    /\ BoolFn(pending) /\ BoolFn(localBranch) /\ BoolFn(remoteBranch)
    /\ mgrDestroyedForeign \in BOOLEAN
    /\ liveReclaimed \in BOOLEAN

\* A Manager session is recoverable iff its worktree is still present, or its
\* local branch ref survives (recoverMissingWorktree prefers the local ref so an
\* unpushed commit is never discarded for an older remote tip), or -- when remote
\* durability is on -- the trusted remote still carries it.
Recoverable(s) ==
    \/ present[s]
    \/ localBranch[s]
    \/ (RemoteDurable /\ remoteBranch[s])

(***************************************************************************)
(* Invariants                                                              *)
(***************************************************************************)

\* The Manager never reclaims/adopts a foreign subsystem's worktree.
NoForeignReclaim == ~mgrDestroyedForeign

\* The Manager never reclaims a session an operator currently has open.
LiveSessionNeverReclaimed == ~liveReclaimed

\* A Manager session with an unpublished pending edit is always recoverable --
\* its in-flight work is never lost.
SessionDurable ==
    \A s \in Sessions : (Owner(s) = "mgr" /\ pending[s]) => Recoverable(s)

(***************************************************************************)
(* Init: the foreign worktree already exists in the shared dir; the Manager  *)
(* session has not been opened yet.                                          *)
(***************************************************************************)
Init ==
    /\ present      = [s \in Sessions |-> s = "f"]
    /\ live         = [s \in Sessions |-> FALSE]
    /\ recordFresh  = [s \in Sessions |-> FALSE]
    /\ pending      = [s \in Sessions |-> FALSE]
    /\ localBranch  = [s \in Sessions |-> s = "f"]
    /\ remoteBranch = [s \in Sessions |-> FALSE]
    /\ mgrDestroyedForeign = FALSE
    /\ liveReclaimed = FALSE

(***************************************************************************)
(* Manager / operator actions (target the Manager session "m").             *)
(***************************************************************************)

\* Operator opens a chat-diff session: cut the worktree + local branch, register
\* it live, stamp a fresh record. No edit and no push yet.
OpenSession ==
    /\ ~present["m"]
    /\ present'      = [present      EXCEPT !["m"] = TRUE]
    /\ live'         = [live         EXCEPT !["m"] = TRUE]
    /\ recordFresh'  = [recordFresh  EXCEPT !["m"] = TRUE]
    /\ localBranch'  = [localBranch  EXCEPT !["m"] = TRUE]
    /\ pending'      = [pending      EXCEPT !["m"] = FALSE]
    /\ UNCHANGED <<remoteBranch, mgrDestroyedForeign, liveReclaimed>>

\* A chat turn that produces/continues an edit: runTurn commits the change (+the
\* transcript sidecar) to the local branch and, when a trusted remote exists,
\* pushes it. So a pending edit and its remote copy are established atomically.
TurnEdit ==
    /\ live["m"]
    /\ present'      = [present      EXCEPT !["m"] = TRUE]
    /\ pending'      = [pending      EXCEPT !["m"] = TRUE]
    /\ recordFresh'  = [recordFresh  EXCEPT !["m"] = TRUE]
    /\ localBranch'  = [localBranch  EXCEPT !["m"] = TRUE]
    /\ remoteBranch' = [remoteBranch EXCEPT !["m"] = RemoteDurable]
    /\ UNCHANGED <<live, mgrDestroyedForeign, liveReclaimed>>

\* Operator navigates away: the session is no longer live in byID, but any pending
\* edit and its durable branch state remain (it is now idle, awaiting gc/recovery).
CloseSession ==
    /\ live["m"]
    /\ live' = [live EXCEPT !["m"] = FALSE]
    /\ UNCHANGED <<present, recordFresh, pending, localBranch, remoteBranch,
                   mgrDestroyedForeign, liveReclaimed>>

\* Time passes: the persisted record ages past the TTL. Adversary -- models the
\* "open-but-idle" window in which the gc dance would (wrongly, without LiveGuard)
\* consider a still-live session reclaimable.
AgeRecord ==
    /\ recordFresh["m"]
    /\ recordFresh' = [recordFresh EXCEPT !["m"] = FALSE]
    /\ UNCHANGED <<present, live, pending, localBranch, remoteBranch,
                   mgrDestroyedForeign, liveReclaimed>>

\* Total local loss of a worktree: a crash, a wiped scratch dir, or a different
\* host. The worktree dir and its LOCAL branch ref are gone; the trusted-remote
\* copy (if any) survives. Adversary; can hit either worktree.
LoseLocalWorktree(s) ==
    /\ present[s]
    /\ present'     = [present     EXCEPT ![s] = FALSE]
    /\ localBranch' = [localBranch EXCEPT ![s] = FALSE]
    /\ UNCHANGED <<live, recordFresh, pending, remoteBranch,
                   mgrDestroyedForeign, liveReclaimed>>

\* Reload's recoverMissingWorktree rebuilds a Manager session whose dir vanished,
\* from the surviving local ref (preferred) or the trusted remote. Manager-only:
\* the recovery scan is scoped to the hive-edit- namespace.
RecoverReload(s) ==
    /\ Owner(s) = "mgr"
    /\ ~present[s]
    /\ (localBranch[s] \/ (RemoteDurable /\ remoteBranch[s]))
    /\ present'     = [present     EXCEPT ![s] = TRUE]
    /\ localBranch' = [localBranch EXCEPT ![s] = TRUE]
    /\ UNCHANGED <<live, recordFresh, pending, remoteBranch,
                   mgrDestroyedForeign, liveReclaimed>>

\* The gc dance's KEEP-or-RECLAIM decision (evaluateWorktree / Reclaimable).
\* RECLAIM = delete the worktree + local branch + (best-effort) the remote copy,
\* so a reclaimed session is never resurrected by a later remote scan.
\* Guards, in the fixed protocol:
\*   NamespaceScoped => only the Manager's own (hive-edit-) worktrees;
\*   LiveGuard       => never a live (byID) session;
\* and unconditionally: only a stale-record, non-pending worktree is eligible.
Reclaim(s) ==
    /\ present[s]
    /\ (NamespaceScoped => Owner(s) = "mgr")
    /\ (LiveGuard => ~live[s])
    /\ ~recordFresh[s]
    /\ ~pending[s]
    /\ present'      = [present      EXCEPT ![s] = FALSE]
    /\ localBranch'  = [localBranch  EXCEPT ![s] = FALSE]
    /\ remoteBranch' = [remoteBranch EXCEPT ![s] = FALSE]
    /\ mgrDestroyedForeign' = (mgrDestroyedForeign \/ (Owner(s) = "foreign"))
    /\ liveReclaimed' = (liveReclaimed \/ live[s])
    /\ UNCHANGED <<live, recordFresh, pending>>

\* Idle so a fully-settled state is not read as a deadlock.
Stutter == UNCHANGED vars

Next ==
    \/ OpenSession
    \/ TurnEdit
    \/ CloseSession
    \/ AgeRecord
    \/ \E s \in Sessions : LoseLocalWorktree(s)
    \/ \E s \in Sessions : RecoverReload(s)
    \/ \E s \in Sessions : Reclaim(s)
    \/ Stutter

Spec == Init /\ [][Next]_vars
=============================================================================
