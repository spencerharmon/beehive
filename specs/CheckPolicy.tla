---------------------------- MODULE CheckPolicy ----------------------------
(***************************************************************************)
(* The command-layer policy for a task's definition-of-done `Check:`         *)
(* command, modeled by internal/checkpolicy `Policy.Validate`. A Check is     *)
(* the shell string the runner EXECUTES at the DONE gate; this layer bounds   *)
(* WHICH command words it may invoke, independent of the filesystem-          *)
(* confinement (bwrap) layer.                                                 *)
(*                                                                         *)
(* The design under test is a DENYLIST, not an allowlist. The universe of    *)
(* commands a honeybee may run at all is owned by the agent runtime          *)
(* (opencode) permission config (`OpencodeAllowed`); a check is a SUBSET of   *)
(* that universe. `checkpolicy.Validate` ADMITS anything opencode permits     *)
(* EXCEPT the commands on the denylist (`Denied`), and REFUSES any check it   *)
(* cannot statically analyze (`analyzable = FALSE`) so a denied command can   *)
(* never be smuggled past it (fail closed).                                   *)
(*                                                                         *)
(* Two failure classes are modeled as the two buggy configurations:          *)
(*                                                                         *)
(*   1. ALLOWLIST (the pre-change design): admit only commands on a finite    *)
(*      positive `Allowlist`. A real test runner not enumerated there         *)
(*      (`go test`, `dotnet test`, `pytest`, `nix build`) is REFUSED even     *)
(*      though opencode permits it and it is not abusive -- the usability     *)
(*      defect that forced an operator to widen config per tool.              *)
(*      `RealFrameworkUsable` violated. (UseDenylist = FALSE)                 *)
(*                                                                         *)
(*   2. ABUSE HOLE: a denylist that OMITS a fake-test tool (a misconfigured   *)
(*      `Denied` that leaves `grep` in). A check built only from source-      *)
(*      inspection tools is then ADMITTED -- a honeybee passes off a source-  *)
(*      text assertion as a real definition of done. `NoFakeOnlyAdmitted`     *)
(*      violated. (UseDenylist = TRUE, Denied missing the fake tools)         *)
(*                                                                         *)
(* The fixed configuration (denylist that contains the fake-test tools AND    *)
(* the code-smuggling/destructive backstop) satisfies every property.        *)
(*                                                                         *)
(* Mapping (see docs/formal-spec-mapping.md): the `w \notin Denied` conjunct  *)
(* is `checkpolicy.Validate` + `DefaultDeniedCommands`; the                   *)
(* `check \subseteq OpencodeAllowed` conjunct is opencode's own permission    *)
(* config (swarm/opencode.go Open sets it); `~analyzable => refuse` is        *)
(* Validate's fail-closed error path (lexer.go commandWords returning err).   *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets

CONSTANTS
    Commands,        \* the finite universe of command words in the model
    RealFramework,   \* commands that constitute a REAL test (go test, curl, kubectl, ...)
    OpencodeAllowed, \* the commands opencode permits at all (the universe a check subsets)
    Denied,          \* the denylist (used when UseDenylist = TRUE)
    Allowlist,       \* the positive allowlist (used when UseDenylist = FALSE, the buggy model)
    UseDenylist      \* TRUE: denylist design (the fix). FALSE: allowlist design (the bug).

ASSUME RealFramework \subseteq Commands
ASSUME OpencodeAllowed \subseteq Commands
ASSUME Denied \subseteq Commands
ASSUME Allowlist \subseteq Commands
ASSUME UseDenylist \in BOOLEAN

VARIABLES
    check,      \* the current check: the set of command words it invokes (its command positions)
    analyzable, \* whether the check is statically resolvable (else Validate fails closed)
    decision    \* "none" (initial), "admit", or "refuse"

vars == <<check, analyzable, decision>>

Decisions == {"none", "admit", "refuse"}

\* Nonempty subsets of Commands: every candidate check invokes at least one command.
NonEmptyChecks == { c \in SUBSET Commands : c # {} }

\* The set of commands a check may use under the DENYLIST design: everything
\* opencode permits, minus the denylist. This is the "subset of all allowed
\* commands (opencode) minus the denylist" the operator specified.
Admissible == OpencodeAllowed \ Denied

\* The policy decision Validate makes for a candidate check c with analyzability a.
Decide(c, a) ==
    IF ~a
        THEN "refuse"                                          \* fail closed
    ELSE IF UseDenylist
        THEN (IF c \subseteq Admissible THEN "admit" ELSE "refuse")
        ELSE (IF c \subseteq Allowlist  THEN "admit" ELSE "refuse")

TypeOK ==
    /\ check \in SUBSET Commands
    /\ analyzable \in BOOLEAN
    /\ decision \in Decisions

(***************************************************************************)
(* Safety properties                                                        *)
(***************************************************************************)

\* No admitted check contains a denied command. The core denylist guarantee.
NoDeniedAdmitted ==
    (decision = "admit") => (\A w \in check : w \notin Denied)

\* Every admitted check is a subset of what opencode permits -- the check-command
\* set is a subset of the universe opencode owns.
SubsetOpencodeAllowed ==
    (decision = "admit") => (check \subseteq OpencodeAllowed)

\* Fail closed: a check that cannot be statically analyzed is never admitted, so a
\* denied command can never be smuggled through a variable/eval/command-subst.
FailClosed ==
    (~analyzable) => (decision # "admit")

\* Anti-abuse: an admitted check invokes at least one real framework -- a check
\* built ONLY from fake source-inspection/no-op tools (grep/find/test/true) is
\* refused. Holds because those tools are on the denylist; the abuse-hole cfg
\* drops them from Denied and reproduces the violation.
NoFakeOnlyAdmitted ==
    (decision = "admit") => (\E w \in check : w \in RealFramework)

\* Usability (the denylist win): a real-framework check that opencode permits,
\* that is not denied, and that is analyzable, is ADMITTED -- with NO positive
\* per-tool allowlist entry required. The allowlist (buggy) design violates this
\* for any real framework absent from its finite Allowlist (e.g. `go test`).
RealFrameworkUsable ==
    ( /\ decision # "none"
      /\ analyzable
      /\ check # {}
      /\ check \subseteq RealFramework
      /\ check \subseteq OpencodeAllowed
      /\ \A w \in check : w \notin Denied )
    => (decision = "admit")

(***************************************************************************)
(* Init / Next                                                              *)
(*                                                                         *)
(* Pure safety exploration: each step nondeterministically picks a candidate *)
(* check (any nonempty set of command words) and an analyzability flag, then *)
(* records the policy's decision. TLC enumerates every check over the small  *)
(* Commands universe, so the invariants are checked against all of them.     *)
(***************************************************************************)
Init ==
    /\ check = {}
    /\ analyzable = TRUE
    /\ decision = "none"

Pick ==
    \E c \in NonEmptyChecks, a \in BOOLEAN :
        /\ check' = c
        /\ analyzable' = a
        /\ decision' = Decide(c, a)

Next == Pick

Spec == Init /\ [][Next]_vars
=============================================================================
