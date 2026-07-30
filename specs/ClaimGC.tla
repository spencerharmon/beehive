------------------------------ MODULE ClaimGC ------------------------------
(***************************************************************************)
(* Layer 2 (part b, continued): the heartbeat-vs-TTL claim-GC TIMING          *)
(* invariant that keeps `swarm.Runner`'s mid-turn keepalive and selection's    *)
(* claim-GC safe together.                                                    *)
(*                                                                          *)
(* `ClaimRace.tla` models the commit RACE between two competing sessions,      *)
(* with the keepalive-vs-staleness timing left as an over-approximated,        *)
(* per-tick decision (`Fixed` toggles whether a dispatched owner's heartbeat    *)
(* tracks the clock at all). This module pins down the actual PARAMETER-        *)
(* ORDERING that timing rests on: a live pass re-stamps its own claim's          *)
(* heartbeat roughly every `TTL/3` for the whole duration of its turn            *)
(* (`internal/select/select.go`'s keepalive, modeled here as period `K`),        *)
(* and GC reclaims any claim whose heartbeat has gone stale for longer than      *)
(* `TTL` (`internal/config/config.go`'s `TTLMinutes`). The invariant is          *)
(* exactly the ordering `K < TTL`: as long as the keepalive fires strictly       *)
(* more often than the GC threshold, a live claim's heartbeat can never fall      *)
(* behind far enough to be reclaimed -- this is the honeybee-claim analogue      *)
(* of the editor's `LiveGuard` guarantee (`EditorSessionNamespace.tla`).          *)
(*                                                                          *)
(* Two invariants:                                                            *)
(*                                                                          *)
(*   NoLiveReclaim -- whenever K < TTL, a LIVE pass's claim is NEVER            *)
(*     reclaimed by GC. Violating this means duplicate dispatch on the same     *)
(*     task and lost in-progress work (the exact class `301964d` fixed).        *)
(*                                                                          *)
(*   EventuallyReclaimDead -- a DEAD pass's claim (heartbeat stops advancing     *)
(*     because the process died / was killed) IS eventually reclaimed by GC --   *)
(*     no permanent wedge holding the task forever.                             *)
(*                                                                          *)
(* CONSTANT toggle: the buggy cfg sets K >= TTL (the keepalive interval sits     *)
(* AT OR PAST the GC's reclaim window) -- TLC finds a trace where a still-live   *)
(* pass's heartbeat goes stale to GC and gets reclaimed while the pass is        *)
(* still dispatched (a second pass would then dispatch on the very same task).   *)
(* The fixed cfg sets K < TTL with margin -- NoLiveReclaim and                  *)
(* EventuallyReclaimDead both hold.                                              *)
(***************************************************************************)
EXTENDS Naturals, TLC

CONSTANTS
    MaxAge,  \* bound on ticks-since-last-heartbeat we track (keeps state finite;
             \* must be > TTL so a dead claim's age can actually cross the GC threshold)
    K,       \* keepalive period: a live pass re-stamps its heartbeat every K ticks
    TTL      \* GC reclaim threshold: a claim idle longer than TTL is reclaimable

VARIABLES
    age,        \* ticks elapsed since the claim's heartbeat was last stamped
    alive,      \* TRUE while the owning pass is still live (its turn has not ended/died)
    reclaimed   \* TRUE once GC has reclaimed the claim

vars == <<age, alive, reclaimed>>

(***************************************************************************)
(* `age` replaces an absolute wall clock: rather than track `clock` and       *)
(* `heartbeat` separately (which would force a global clock bound that can     *)
(* deadlock the model when a pass dies right at the bound, before its claim    *)
(* has had time to go stale), we track directly how many ticks have elapsed    *)
(* since the last heartbeat stamp -- exactly the quantity GC compares against  *)
(* `TTL`. It resets to 0 on every keepalive restamp and saturates at `MaxAge`   *)
(* while dead, so `Tick` never needs a "clock exhausted" guard and the model    *)
(* never deadlocks before a dead claim's age can cross `TTL`.                   *)
(***************************************************************************)
TypeOK ==
    /\ age \in 0..MaxAge
    /\ alive \in BOOLEAN
    /\ reclaimed \in BOOLEAN

(***************************************************************************)
(* Safety invariant: a live pass's claim can never be reclaimed out from       *)
(* under it. Whether this actually HOLDS is entirely a function of the          *)
(* K/TTL CONSTANTS a given .cfg picks (mirroring ClaimRace.tla's `Fixed`         *)
(* toggle): with K < TTL (the fixed cfg) the keepalive always restamps           *)
(* before GC's threshold is crossed, so this holds always; with K >= TTL          *)
(* (the buggy cfg) the keepalive falls behind and TLC finds a trace where a       *)
(* still-live claim gets reclaimed.                                              *)
(***************************************************************************)
NoLiveReclaim == alive => ~reclaimed

(***************************************************************************)
(* Init                                                                     *)
(***************************************************************************)
Init ==
    /\ age = 0
    /\ alive = TRUE
    /\ reclaimed = FALSE

(***************************************************************************)
(* Actions                                                                  *)
(***************************************************************************)

\* Time advances. While the pass is live, `swarm.Runner`'s mid-turn keepalive
\* re-stamps the claim's heartbeat every K ticks (internal/select/select.go's
\* "roughly every TTL/3" cadence, generalized to the parameter K here) -- so
\* `age` resets to 0 the instant it would reach K. Once the pass has died no
\* further keepalive restamps happen, so `age` simply keeps growing (capped at
\* MaxAge, which is > TTL, to keep the state space finite without ever
\* preventing `age` from crossing TTL).
Tick ==
    /\ ~reclaimed
    /\ IF alive
         THEN age' = IF age + 1 >= K THEN 0 ELSE age + 1
         ELSE age' = IF age < MaxAge THEN age + 1 ELSE age
    /\ UNCHANGED <<alive, reclaimed>>

\* The live pass's turn ends / the process dies: no further keepalive restamps.
\* This can happen at any point while still live and not yet reclaimed --
\* modeling a crash, an OOM-kill, a wall-clock/idle-timeout abort, or the
\* runner tearing the worktree down mid-turn.
Die ==
    /\ alive
    /\ ~reclaimed
    /\ alive' = FALSE
    /\ UNCHANGED <<age, reclaimed>>

\* Selection's claim-GC reclaims a claim whose heartbeat has gone stale for
\* longer than TTL (plan.Plan.Candidates(now, ttl) treating the task as a
\* candidate again). Legitimate when the owner is dead; the duplicate-dispatch
\* bug when the owner is still live but K was allowed to reach/exceed TTL, so
\* `age` peaks (at K-1) above TTL before the next restamp resets it.
GC ==
    /\ ~reclaimed
    /\ age > TTL
    /\ reclaimed' = TRUE
    /\ UNCHANGED <<age, alive>>

\* Terminal idle once reclaimed (nothing further to model past claim-GC).
Done ==
    /\ reclaimed
    /\ UNCHANGED vars

Next ==
    \/ Tick
    \/ Die
    \/ GC
    \/ Done

(***************************************************************************)
(* Liveness (checked in the fixed cfg): once the pass has died, its claim is   *)
(* eventually reclaimed -- no permanent wedge on a dead owner's claim.          *)
(***************************************************************************)
EventuallyReclaimDead == (~alive) ~> reclaimed

Fairness ==
    /\ WF_vars(Tick)
    /\ WF_vars(GC)

Spec == Init /\ [][Next]_vars /\ Fairness
=============================================================================
