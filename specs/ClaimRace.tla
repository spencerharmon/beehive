------------------------------ MODULE ClaimRace ------------------------------
(***************************************************************************)
(* Layer 2 (part b): the commit-race claim protocol between concurrent       *)
(* honeybee passes.  Faithful to internal/claim and internal/swarm's         *)
(* selection/dispatch: a pass stamps a PLAN.md task with its session id +     *)
(* a heartbeat, and the runner re-stamps the heartbeat each turn while the    *)
(* agent runs (the mid-turn keepalive).  Selection treats a claim whose       *)
(* heartbeat is older than a decoupled staleness threshold as reclaimable.    *)
(*                                                                         *)
(* Two properties, at two strengths:                                         *)
(*                                                                         *)
(*   AtMostOneLands  -- the CORRECTNESS backstop: two sessions never both     *)
(*     complete the task.  The publish is the race; a competing claim lands   *)
(*     first and the loser's publish conflicts (ErrLost), so it backs off     *)
(*     without landing.  This holds ALWAYS -- even in the buggy variant --    *)
(*     because it rests on the single-owner publish conflict, not on the      *)
(*     dispatch heuristic.                                                    *)
(*                                                                         *)
(*   NoDuplicateDispatch -- the EFFICIENCY property the fix adds: two         *)
(*     sessions never both get dispatched onto the same live task (wasting a  *)
(*     whole session).  Reproduces 301964d: without the mid-turn heartbeat    *)
(*     keepalive + decoupled selection staleness + pre-dispatch re-confirm,   *)
(*     a live owner's heartbeat goes stale to selection and a second session  *)
(*     dispatches on top of it.                                              *)
(*                                                                         *)
(* Fidelity: an owner's keepalive is modeled as tracking the clock WHILE      *)
(* it is dispatched (the runner re-stamps every turn); when it is not         *)
(* dispatched its heartbeat stops advancing and legitimately becomes          *)
(* reclaimable.  In the fixed protocol a live owner is therefore never seen   *)
(* stale; in the buggy protocol (no keepalive) it is.                         *)
(*                                                                         *)
(* CONSTANT Fixed selects the keepalive-and-reconfirm protocol (TRUE) vs the  *)
(* pre-301964d runner (FALSE).  See the .cfg files.                          *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS
    Sessions,   \* the set of competing pass sessions (model with {1, 2})
    MaxClock,   \* logical clock bound (keeps the state space finite)
    SelStale,   \* selection staleness threshold: a claim older than this is reclaimable
    Fixed       \* TRUE: mid-turn heartbeat keepalive keeps a live claim fresh to selection

VARIABLES
    clock,       \* logical wall clock
    owner,       \* session whose claim is currently published on the task (0 = unclaimed)
    ownerHb,     \* heartbeat time of the published claim
    dispatched,  \* sessions currently executing a turn believing they hold the task
    landed       \* sessions that landed a terminal publish (correctness: <= 1 ever)

vars == <<clock, owner, ownerHb, dispatched, landed>>

NoOwner == 0

TypeOK ==
    /\ clock \in 0..MaxClock
    /\ owner \in (Sessions \cup {NoOwner})
    /\ ownerHb \in 0..MaxClock
    /\ dispatched \subseteq Sessions
    /\ landed \subseteq Sessions

(***************************************************************************)
(* Safety invariants                                                       *)
(***************************************************************************)

\* CORRECTNESS backstop (holds in every cfg): the task is never completed twice.
AtMostOneLands == Cardinality(landed) <= 1

\* EFFICIENCY property the 301964d fix guarantees: never two sessions dispatched
\* on the same live task at once.
NoDuplicateDispatch == Cardinality(dispatched) <= 1

(***************************************************************************)
(* Init                                                                    *)
(***************************************************************************)
Init ==
    /\ clock = 0
    /\ owner = NoOwner
    /\ ownerHb = 0
    /\ dispatched = {}
    /\ landed = {}

(***************************************************************************)
(* Actions                                                                 *)
(***************************************************************************)

\* Time advances. In the fixed protocol the runner re-stamps a live (dispatched)
\* owner's heartbeat every turn, so its claim tracks the clock and never looks
\* stale to selection. In the buggy protocol there is no keepalive, so a live
\* owner's heartbeat falls behind and eventually looks reclaimable.
Tick ==
    /\ clock < MaxClock
    /\ clock' = clock + 1
    /\ ownerHb' = IF (Fixed /\ owner /= NoOwner /\ owner \in dispatched)
                    THEN clock + 1
                    ELSE ownerHb
    /\ UNCHANGED <<owner, dispatched, landed>>

\* A session claims a wholly unclaimed task and is dispatched.
ClaimFresh(s) ==
    /\ landed = {}
    /\ s \notin dispatched
    /\ owner = NoOwner
    /\ owner' = s
    /\ ownerHb' = clock
    /\ dispatched' = dispatched \cup {s}
    /\ UNCHANGED <<clock, landed>>

\* A session reclaims a claim that looks STALE to selection (heartbeat older than
\* the threshold) and is dispatched. This is legitimate GC-reclaim when the owner
\* really died; it is the duplicate-dispatch bug when the owner is actually still
\* live but its heartbeat was allowed to go stale (buggy: no keepalive).
ClaimStale(s) ==
    /\ landed = {}
    /\ s \notin dispatched
    /\ owner /= NoOwner
    /\ owner /= s
    /\ clock - ownerHb > SelStale
    /\ owner' = s
    /\ ownerHb' = clock
    /\ dispatched' = dispatched \cup {s}
    /\ UNCHANGED <<clock, landed>>

\* A dispatched session that still holds the published claim completes its turn:
\* it publishes its terminal transition and lands.
Finish(s) ==
    /\ s \in dispatched
    /\ owner = s
    /\ landed' = landed \cup {s}
    /\ owner' = NoOwner
    /\ dispatched' = dispatched \ {s}
    /\ UNCHANGED <<clock, ownerHb>>

\* A dispatched session whose claim was stolen out from under it loses the publish
\* race (ErrLost) and backs off WITHOUT landing -- the correctness backstop.
LoseRace(s) ==
    /\ s \in dispatched
    /\ owner /= s
    /\ dispatched' = dispatched \ {s}
    /\ UNCHANGED <<clock, owner, ownerHb, landed>>

\* Terminal idle once the task has landed and no session is still dispatched.
Done ==
    /\ landed /= {}
    /\ dispatched = {}
    /\ UNCHANGED vars

Next ==
    \/ Tick
    \/ \E s \in Sessions : ClaimFresh(s)
    \/ \E s \in Sessions : ClaimStale(s)
    \/ \E s \in Sessions : Finish(s)
    \/ \E s \in Sessions : LoseRace(s)
    \/ Done

(***************************************************************************)
(* Liveness (checked in the fixed cfg): the task eventually lands.          *)
(***************************************************************************)
EventuallyLanded == <>(landed /= {})

Fairness ==
    /\ \A s \in Sessions : WF_vars(ClaimFresh(s))
    /\ \A s \in Sessions : WF_vars(Finish(s))
    /\ \A s \in Sessions : WF_vars(LoseRace(s))

Spec == Init /\ [][Next]_vars /\ Fairness
=============================================================================
