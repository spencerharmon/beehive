----------------------------- MODULE TaskStatus -----------------------------
(***************************************************************************)
(* Layer 2 (part a): the task-lifecycle status state machine + the DONE     *)
(* gates.  Faithful to internal/plan/state.go: the legal edges, the         *)
(* attempts/limit escalation to NEEDS-HUMAN, the runner recovery edges       *)
(* (RecoverLostWork, FinalizeAlreadyMerged), and the honeybee/operator       *)
(* escalation edges (RequestHuman, Resolve).                                 *)
(*                                                                         *)
(* The single most important safety property here is NoFalseDone: a task    *)
(* reaches DONE only when its work is REAL -- committed and durable on the   *)
(* submodule origin AND merged into the tracked branch.  The handoff gate    *)
(* (internal/swarm/verify.go) is what enforces this; turning it off          *)
(* reproduces the family of silent false-DONE bugs:                          *)
(*   - fe6da39  uncommitted work landed on an empty bee-branch               *)
(*   - 2573066  NEEDS-REVIEW handoff gated on a COMMITTED change doc          *)
(*   - 72e2b4a  handoff gate verifies commits are durable on ORIGIN          *)
(*   - 743b1c6  reviewed commit read from the task's own bee tip, not the     *)
(*              ambient sibling gitlink (ambient false-DONE)                  *)
(*   - 92d2ed1  DONE entered on a reviewed commit while the task's declared    *)
(*              definition-of-done Check command was NOT satisfied            *)
(*              (jellyfin:zuul-image-build-publish false-DONE)                *)
(*                                                                         *)
(* DoD-check gate (verifyGate invariant 5, 92d2ed1): entering DONE by ANY     *)
(* path -- review approve, arbitration, interrupted-review finalize -- runs   *)
(* the task's declared `Check:` command and REFUSES the DONE unless it        *)
(* passes.  A `check=none` task declared a justified absence and is not gated. *)
(* So NoFalseDone strengthens: durable + merged is necessary but NOT          *)
(* sufficient; a declared check must also be satisfied.                       *)
(*                                                                         *)
(* Liveness: a NEEDS-REVIEW/NEEDS-ARBITRATION task whose work is lost         *)
(* everywhere eventually returns to TODO or escalates NEEDS-HUMAN -- it       *)
(* never strands forever at a phantom commit (4fdd953, 743c46f).  And the     *)
(* attempts/limit escalation guarantees the task always terminates           *)
(* (reaches DONE or NEEDS-HUMAN) rather than looping rework forever.          *)
(*                                                                         *)
(* Concurrency (the claim race between two sessions) is the companion         *)
(* module ClaimRace.tla; this module models a single worker at a time and     *)
(* focuses on edge legality + the gates + recovery.                          *)
(*                                                                         *)
(* CONSTANT Gated selects the handoff-gate-enforced protocol (TRUE) vs the    *)
(* ungated pre-fix runner (FALSE).  See the .cfg files.                      *)
(*                                                                         *)
(* Layer 2 (part b), the handoff-terminal-leak: `status` above models the    *)
(* AGENT-WRITTEN on-disk status only (the worktree's PLAN.md). It is NOT      *)
(* what the selector/peers observe -- that is `published`, a separate         *)
(* variable moved by the runner's own per-turn heartbeat                     *)
(* (internal/claim Heartbeat -> CommitPaths(planRel()) -> Publish), which     *)
(* commits + publishes PLAN.md to `main` EVERY turn, independent of the       *)
(* handoff gate and of whether the change doc exists anywhere. On gate-fail   *)
(* the pre-fix runner PINS `sel.Task.Status` at the unearned terminal value   *)
(* (swarm.go ~1205/~1401), so every later heartbeat re-publishes it forever   *)
(* -- the exact defect that let `beehive:active-state-live-poll` and          *)
(* `chris-agent:spec-lifecycle-state-machine` reach DONE on `main` with NO    *)
(* change doc.  CONSTANT RevertOverPin selects the fix (TRUE: on gate-fail    *)
(* the runner REVERTS the on-disk status back to its pre-handoff value and    *)
(* the heartbeat's publish of a terminal status is itself gated on the        *)
(* backing artifacts -- "runner-owned doc/commit synthesis") vs the buggy     *)
(* pre-fix behavior (FALSE: pin-to-terminal + an unconditional heartbeat      *)
(* publish).  See NoDoclessTerminal below and the `_leak_*.cfg` files.        *)
(***************************************************************************)
EXTENDS Naturals, TLC

CONSTANTS
    Limit,        \* reject/recover attempts limit; past it a task escalates NEEDS-HUMAN
    Gated,        \* TRUE: the handoff gate requires work durable-on-origin before DONE-ward edges
    CheckGated,   \* TRUE: entering DONE requires the task's declared DoD Check to pass (verifyGate inv 5)
    RevertOverPin \* TRUE: gate-fail reverts on-disk status + gates the heartbeat publish (the fix).
                  \* FALSE: gate-fail pins the on-disk status and the heartbeat publishes unconditionally
                  \* (the handoff-terminal-leak bug).

VARIABLES
    status,       \* AGENT-WRITTEN on-disk status (the worktree's PLAN.md), one of Statuses
    prevStatus,   \* status before the last step (to check edge legality as an invariant)
    attempts,     \* rework attempts; Reject/Strand/RecoverLostWork/GateCheck bump it (state.go)
    workDurable,  \* the task's own bee work is committed AND durable on the submodule origin
    merged,       \* the task's own bee work has been merged into the tracked branch
    workLost,     \* adversary flag: the durable work was subsequently lost (branch GC'd, publish never landed)
    checkDeclared,\* the task declares a machine-checkable `Check:` DoD command (vs check=none/undeclared)
    checkPassed,  \* the declared Check currently exits 0 (the acceptance bar is actually met)
    docWritten,   \* the agent has written the change doc on-disk (in its own worktree commit)
    docOnMain,    \* the change doc is committed on `main` (durable, peer-visible)
    published     \* the status the runner's heartbeat has actually published to `main` (selector/peer view)

vars == <<status, prevStatus, attempts, workDurable, merged, workLost,
          checkDeclared, checkPassed, docWritten, docOnMain, published>>

Statuses == {"TODO", "REVIEW", "ARB", "DONE", "HUMAN"}

\* The legal status edges. Agent edges (Transition, state.go:14-16):
\*   TODO->REVIEW, REVIEW->{DONE,ARB}, ARB->{TODO,DONE}.
\* Runner/operator edges (Reject/Strand/RecoverLostWork/BounceUnreachable/
\* FinalizeAlreadyMerged/RequestHuman/Resolve):
\*   REVIEW->{TODO,HUMAN,ARB}, ARB->{TODO,HUMAN,DONE}, TODO->HUMAN, HUMAN->TODO.
LegalEdges ==
    { <<"TODO","REVIEW">>, <<"REVIEW","DONE">>, <<"REVIEW","ARB">>,
      <<"ARB","TODO">>, <<"ARB","DONE">>,
      <<"REVIEW","TODO">>, <<"REVIEW","HUMAN">>,
      <<"ARB","HUMAN">>, <<"TODO","HUMAN">>,
      <<"HUMAN","TODO">>,
      \* Fix-introduced recovery edges (RevertOverPin): the runner reverts an
      \* unearned on-disk DONE back to whichever status preceded it (REVIEW via
      \* ReviewApprove/FinalizeAlreadyMerged, ARB via ArbSideImpl) instead of
      \* pinning it, so a subsequent ArtifactsPresent-gated re-check can retry;
      \* past Limit it escalates straight to NEEDS-HUMAN instead.
      <<"DONE","REVIEW">>, <<"DONE","ARB">>, <<"DONE","HUMAN">> }

TypeOK ==
    /\ status \in Statuses
    /\ prevStatus \in Statuses
    /\ attempts \in 0..(Limit + 1)
    /\ workDurable \in BOOLEAN
    /\ merged \in BOOLEAN
    /\ workLost \in BOOLEAN
    /\ checkDeclared \in BOOLEAN
    /\ checkPassed \in BOOLEAN
    /\ docWritten \in BOOLEAN
    /\ docOnMain \in BOOLEAN
    /\ published \in Statuses

\* The definition-of-done is satisfied iff no check is declared (check=none: the
\* absence is honest and review-scrutinized) or the declared check actually passes.
DodSatisfied == (~checkDeclared) \/ checkPassed

\* The backing artifacts required before an on-disk terminal status is "earned":
\* REVIEW needs the bee work durable on origin AND the change doc written;
\* DONE additionally needs it merged and its declared DoD Check satisfied.
\* Non-terminal statuses have nothing to gate.
ArtifactsPresent ==
    CASE status = "REVIEW" -> workDurable /\ docWritten
      [] status = "DONE"   -> workDurable /\ merged /\ docWritten /\ DodSatisfied
      [] OTHER             -> TRUE

(***************************************************************************)
(* Safety invariants                                                       *)
(***************************************************************************)

\* Every status change follows a sanctioned edge. (Steps that leave status
\* unchanged -- doing work, losing work -- are exempt.)
LegalTransitionsOnly ==
    (status /= prevStatus) => (<<prevStatus, status>> \in LegalEdges)

\* A task is DONE only when its own work is real: durable on origin AND merged
\* into the tracked branch AND (92d2ed1) its declared definition-of-done Check is
\* satisfied. This is the anti-false-DONE invariant.
NoFalseDone ==
    (status = "DONE") => (workDurable /\ merged /\ DodSatisfied)

\* The attempts counter never runs away past the escalation point.
AttemptsBounded == attempts <= Limit + 1

\* THE HANDOFF-TERMINAL-LEAK invariants: these are about `published` (what the
\* selector/peers actually observe on `main`), NOT `status` (the agent's own
\* worktree PLAN.md). NoFalseDone above already prevents an ON-DISK false-DONE;
\* these prevent the PUBLISH layer leaking a status whose backing artifacts are
\* not durable on `main` -- the defect a pinned, ungated heartbeat reproduces.

\* Extend the anti-false-DONE contract to the handoff itself: a PUBLISHED
\* NEEDS-REVIEW must have its change doc durably recorded on `main` too (the
\* "handoff" the ROI names -- TODO->NEEDS-REVIEW leaking to `main` before the
\* doc is real). Phrased on the sticky `docOnMain` latch rather than the
\* point-in-time `workDurable` flag: work legitimately durable AT handoff can
\* later be adversarially lost (LoseWork, recovered by LostWorkRecovers) without
\* retroactively making the ALREADY-PUBLISHED handoff itself false.
NoFalseHandoff ==
    (published = "REVIEW") => docOnMain

\* No terminal status ever reaches `main` without its change doc also being on
\* `main`. This is the exact shape of the two real false-DONE leaks
\* (beehive:active-state-live-poll, chris-agent:spec-lifecycle-state-machine):
\* DONE published with no doc anywhere.
NoDoclessTerminal ==
    (published \in {"REVIEW", "DONE"}) => docOnMain

(***************************************************************************)
(* Init                                                                    *)
(***************************************************************************)
Init ==
    /\ status = "TODO"
    /\ prevStatus = "TODO"
    /\ attempts = 0
    /\ workDurable = FALSE
    /\ merged = FALSE
    /\ workLost = FALSE
    /\ checkDeclared \in BOOLEAN   \* explore both a check-declared task and a check=none task
    /\ checkPassed = FALSE         \* the acceptance bar starts unmet
    /\ docWritten = FALSE
    /\ docOnMain = FALSE
    /\ published = "TODO"

(***************************************************************************)
(* Actions. Every action sets prevStatus' = status so the edge just taken   *)
(* is checkable by LegalTransitionsOnly in the post-state.                  *)
(***************************************************************************)

\* The Work agent does the work FIRST: commits bee-<taskid> and pushes it to the
\* submodule origin (durable). Status stays TODO until the handoff.
DoWork ==
    /\ status = "TODO"
    /\ ~workDurable
    /\ workDurable' = TRUE
    /\ workLost' = FALSE
    /\ prevStatus' = status
    /\ UNCHANGED <<status, attempts, merged, checkDeclared, checkPassed, docWritten, docOnMain, published>>

\* The agent meets the acceptance bar: it does the real work so the declared DoD
\* Check command now exits 0. Optional and unforced -- an agent CAN hand off
\* without meeting it (the jellyfin defect); the check gate is what refuses the
\* resulting DONE. Enabled while the task is still being worked/reviewed.
PassCheck ==
    /\ status \in {"TODO", "REVIEW", "ARB"}
    /\ checkDeclared
    /\ ~checkPassed
    /\ checkPassed' = TRUE
    /\ prevStatus' = status
    /\ UNCHANGED <<status, attempts, workDurable, merged, workLost, checkDeclared, docWritten, docOnMain, published>>

\* TODO -> NEEDS-REVIEW. The handoff gate: in the fixed protocol the terminal
\* flip is refused unless the work is durable on origin (verify.go +
\* RemoteContainsCommit). Ungated (buggy) it flips regardless.
HandoffToReview ==
    /\ status = "TODO"
    /\ (Gated => workDurable)
    /\ status' = "REVIEW"
    /\ prevStatus' = status
    /\ UNCHANGED <<attempts, workDurable, merged, workLost, checkDeclared, checkPassed, docWritten, docOnMain, published>>

\* Review approves: merge the bee tip into the tracked branch, NEEDS-REVIEW -> DONE.
\* Gated: refuse to approve work that is not durable on origin (no ambient/phantom
\* false-DONE). CheckGated (verifyGate inv 5): refuse to approve a declared-check
\* task whose acceptance bar is not met.
ReviewApprove ==
    /\ status = "REVIEW"
    /\ (Gated => workDurable)
    /\ ((CheckGated /\ checkDeclared) => checkPassed)
    /\ merged' = TRUE
    /\ status' = "DONE"
    /\ prevStatus' = status
    /\ UNCHANGED <<attempts, workDurable, workLost, checkDeclared, checkPassed, docWritten, docOnMain, published>>

\* Review rejects: NEEDS-REVIEW -> NEEDS-ARBITRATION (the agent reject edge).
ReviewReject ==
    /\ status = "REVIEW"
    /\ status' = "ARB"
    /\ prevStatus' = status
    /\ UNCHANGED <<attempts, workDurable, merged, workLost, checkDeclared, checkPassed, docWritten, docOnMain, published>>

\* Arbiter sides with the implementer: merge, NEEDS-ARBITRATION -> DONE. Also
\* gated on the DoD check (the gate covers DONE entered via arbitration too).
ArbSideImpl ==
    /\ status = "ARB"
    /\ (Gated => workDurable)
    /\ ((CheckGated /\ checkDeclared) => checkPassed)
    /\ merged' = TRUE
    /\ status' = "DONE"
    /\ prevStatus' = status
    /\ UNCHANGED <<attempts, workDurable, workLost, checkDeclared, checkPassed, docWritten, docOnMain, published>>

\* Arbiter sides with the reviewer: rework. NEEDS-ARBITRATION -> TODO, attempts++;
\* once attempts exceed the limit the task escalates NEEDS-HUMAN instead of
\* auto-recycling (state.go Reject semantics).
ArbSideReviewer ==
    /\ status = "ARB"
    /\ attempts' = attempts + 1
    /\ status' = IF attempts + 1 > Limit THEN "HUMAN" ELSE "TODO"
    /\ prevStatus' = status
    /\ UNCHANGED <<workDurable, merged, workLost, checkDeclared, checkPassed, docWritten, docOnMain, published>>

\* Adversary: the durable work is subsequently lost -- the bee branch was reclaimed
\* / GC'd and the publish never landed, so what looked reviewable now points at a
\* phantom commit. Enabled only while under review/arbitration and not yet merged.
LoseWork ==
    /\ status \in {"REVIEW", "ARB"}
    /\ workDurable
    /\ ~merged
    /\ workDurable' = FALSE
    /\ workLost' = TRUE
    /\ prevStatus' = status
    /\ UNCHANGED <<status, attempts, merged, checkDeclared, checkPassed, docWritten, docOnMain, published>>

\* Runner recovers a task whose work is unrecoverable everywhere: reset to TODO
\* (attempts++, past limit -> NEEDS-HUMAN). Valid from REVIEW or ARB (state.go
\* RecoverLostWork). This is the self-heal that stops a phantom-commit strand.
RecoverLostWork ==
    /\ status \in {"REVIEW", "ARB"}
    /\ ~workDurable
    /\ ~merged
    /\ attempts' = attempts + 1
    /\ status' = IF attempts + 1 > Limit THEN "HUMAN" ELSE "TODO"
    /\ workLost' = FALSE
    /\ prevStatus' = status
    /\ UNCHANGED <<workDurable, merged, checkDeclared, checkPassed, docWritten, docOnMain, published>>

\* Runner completes interrupted review bookkeeping: the bee work is already merged
\* into tracked main, so finalize NEEDS-REVIEW/ARB -> DONE without a new session.
\* Requires merged, so the work is real; and (verifyGate inv 5) the DoD check must
\* be satisfied -- the interrupted-review finalize is exactly the path the jellyfin
\* false-DONE walked through, so it is gated on the check too.
FinalizeAlreadyMerged ==
    /\ status \in {"REVIEW", "ARB"}
    /\ merged
    /\ workDurable
    /\ ((CheckGated /\ checkDeclared) => checkPassed)
    /\ status' = "DONE"
    /\ prevStatus' = status
    /\ UNCHANGED <<attempts, workDurable, merged, workLost, checkDeclared, checkPassed, docWritten, docOnMain, published>>

\* Honeybee escalates a concrete blocker: any non-DONE working status -> NEEDS-HUMAN.
RequestHuman ==
    /\ status \in {"TODO", "REVIEW", "ARB"}
    /\ status' = "HUMAN"
    /\ prevStatus' = status
    /\ UNCHANGED <<attempts, workDurable, merged, workLost, checkDeclared, checkPassed, docWritten, docOnMain, published>>

\* NOTE: the operator Resolve edge NEEDS-HUMAN -> TODO (state.go Resolve) is a
\* legal edge (kept in LegalEdges) but is deliberately NOT an action here: it is
\* an out-of-band operator action, outside the autonomous protocol's liveness
\* scope. Modeling it would make NEEDS-HUMAN non-terminal and let a resolve/retry
\* loop grow attempts without bound (Resolve does not reset attempts in the code) --
\* realistic, but not a property of the autonomous machine. Within this module
\* NEEDS-HUMAN is terminal, which is exactly the selector's own view (a NEEDS-HUMAN
\* task is excluded from selection until an operator reopens it).

(***************************************************************************)
(* The handoff-terminal-leak layer: WriteDoc/GateCheck/Heartbeat.           *)
(***************************************************************************)

\* The agent writes the change doc in its own worktree, alongside its bee-branch
\* commit -- an on-disk artifact exactly like workDurable, independent of status.
WriteDoc ==
    /\ ~docWritten
    /\ docWritten' = TRUE
    /\ prevStatus' = status
    /\ UNCHANGED <<status, attempts, workDurable, merged, workLost,
                   checkDeclared, checkPassed, docOnMain, published>>

\* The runner's handoff gate: fires whenever the on-disk status sits at an
\* UNEARNED terminal value (its ArtifactsPresent bar is not met). This is the
\* moment verify.go's 5 invariants would refuse the flip.
\*   RevertOverPin = TRUE  (the fix): revert the on-disk status back to what it
\*     was before the handoff (prevStatus) so nothing wrong is left to leak,
\*     bumping attempts (past Limit -> NEEDS-HUMAN) so the task still terminates.
\*   RevertOverPin = FALSE (the bug, swarm.go ~1205/~1401): PIN status at the
\*     unearned terminal value -- it is left exactly where it is, forever, so
\*     every subsequent Heartbeat keeps re-publishing it.
GateCheck ==
    /\ status \in {"REVIEW", "DONE"}
    /\ ~ArtifactsPresent
    /\ IF RevertOverPin
          THEN /\ attempts' = attempts + 1
               /\ status' = IF attempts + 1 > Limit THEN "HUMAN" ELSE prevStatus
          ELSE /\ UNCHANGED attempts
               /\ UNCHANGED status
    /\ prevStatus' = status
    /\ UNCHANGED <<workDurable, merged, workLost, checkDeclared, checkPassed,
                   docWritten, docOnMain, published>>

\* The runner's per-turn heartbeat: internal/claim Heartbeat ->
\* CommitPaths(planRel()) -> Publish. It commits + publishes PLAN.md to `main`
\* EVERY turn -- this action is unconditionally enabled and is what carries the
\* on-disk `status` onto `published`.
\*   RevertOverPin = FALSE (the bug): publishes status verbatim, no matter
\*     whether its backing artifacts are on `main` -- "ungated heartbeat
\*     publish". Combined with GateCheck's pin, an unearned terminal status
\*     leaks to `main` and stays leaked forever.
\*   RevertOverPin = TRUE (the fix, "runner-owned doc/commit synthesis"): the
\*     heartbeat itself is gated -- a terminal status is only published (and
\*     its doc synthesized onto main, docOnMain' = TRUE) once ArtifactsPresent;
\*     otherwise it holds `published` at its last value. Because GateCheck also
\*     reverts any unearned on-disk terminal, the two together mean `published`
\*     can never show a terminal status whose doc is not also on `main`.
Heartbeat ==
    /\ IF status \in {"REVIEW", "DONE"}
          THEN IF RevertOverPin
                  THEN /\ published' = IF ArtifactsPresent THEN status ELSE published
                       /\ docOnMain'  = IF ArtifactsPresent THEN TRUE ELSE docOnMain
                  ELSE /\ published' = status
                       /\ UNCHANGED docOnMain
          ELSE /\ published' = status
               /\ UNCHANGED docOnMain
    /\ UNCHANGED <<status, prevStatus, attempts, workDurable, merged, workLost,
                   checkDeclared, checkPassed, docWritten>>

\* Terminal idle so a completed task does not read as a deadlock.
Done ==
    /\ status \in {"DONE", "HUMAN"}
    /\ UNCHANGED vars

Next ==
    \/ DoWork
    \/ PassCheck
    \/ HandoffToReview
    \/ ReviewApprove
    \/ ReviewReject
    \/ ArbSideImpl
    \/ ArbSideReviewer
    \/ LoseWork
    \/ RecoverLostWork
    \/ FinalizeAlreadyMerged
    \/ RequestHuman
    \/ WriteDoc
    \/ GateCheck
    \/ Heartbeat
    \/ Done

(***************************************************************************)
(* Liveness (checked in the fixed cfg):                                     *)
(*  - the task always terminates (DONE or NEEDS-HUMAN);                      *)
(*  - lost work never strands -- it leads back to TODO or NEEDS-HUMAN;       *)
(*  - published catches up with an on-disk terminal that is genuinely       *)
(*    earned, so the revert loop (GateCheck reverting a pinned unearned      *)
(*    terminal) never livelocks -- a task whose substance never actually     *)
(*    appears still terminates (DONE-with-substance or NEEDS-HUMAN past      *)
(*    Limit), it never spins forever re-flipping.                            *)
(* Fairness on the progress + recovery edges; the LoseWork adversary and the *)
(* operator Resolve edge are deliberately NOT forced.                        *)
(***************************************************************************)
Terminates == <>(status \in {"DONE", "HUMAN"})

LostWorkRecovers ==
    (status \in {"REVIEW", "ARB"} /\ ~workDurable /\ ~merged)
        ~> (status \in {"TODO", "HUMAN"})

\* Published eventually converges with an EARNED on-disk terminal: once the
\* work is genuinely durable+merged+doc-written (or the task terminates at
\* NEEDS-HUMAN), the runner's publish layer is not stuck forever behind an
\* unrecognized/reverted status.
PublishConverges ==
    (status \in {"DONE", "HUMAN"}) ~> (published = status)

Fairness ==
    /\ WF_vars(DoWork)
    /\ WF_vars(PassCheck)
    /\ WF_vars(HandoffToReview)
    /\ WF_vars(ReviewApprove)
    /\ WF_vars(ArbSideImpl)
    /\ WF_vars(ArbSideReviewer)
    /\ WF_vars(RecoverLostWork)
    /\ WF_vars(FinalizeAlreadyMerged)
    /\ WF_vars(WriteDoc)
    /\ WF_vars(GateCheck)
    /\ WF_vars(Heartbeat)

Spec == Init /\ [][Next]_vars /\ Fairness
=============================================================================
