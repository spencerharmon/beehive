# The check-framework registry (CHECKS.md)

Status: **specified + enforced.** The whitelist half of the definition-of-done
contract (`docs/dod-verification-spec.md`). Code: `internal/checks`.

## Why

A task's `Check:` / `Verify-After-Merge:` command is the machine definition of
done the runner gates on. Left unconstrained, an agent satisfies the gate with a
check that only asserts a SOURCE-TEXT fact:

```
Check: grep -q NoDoclessTerminal repo/specs/run-tlc.sh
Check: test -f repo/internal/x.go
Check: grep -rq 'TestRevertOverPin' repo/internal/swarm
```

Every one passes the moment the code is written and proves NOTHING about the real
effect — the exact disease (a DoD that lies) one layer up from the jellyfin
false-DONE. The corpus of such checks in a live PLAN.md is the motivating defect.

CHECKS.md closes the hole: **every non-DONE task's check MUST MATCH an APPROVED
framework stub registered in the submodule's `CHECKS.md`.** A bare source-grep
matches no framework stub, so it is refused — by the linter, the runner handoff
gate, and the reconcile/bootstrap completion gate alike.

## Ownership & location

`submodules/<name>/CHECKS.md` is a **beehive-layer** file alongside `PLAN.md`
(NOT inside `repo/`). It is **owned by honeybees and operator-directed agents**:
as a target's testing frameworks evolve, add or refine a stub here, then point
tasks' checks at it. Edit it through the same worktree/publish protocol as
`PLAN.md`/`docs/` (never in the live checkout).

## Format

Line-oriented, stable round-trip (mirrors `PLAN.md`):

```
# Checks — <submodule>
<free-form header prose>

## <stub-id> <!-- category=<category> -->
<one-or-more description lines>
Match: <RE2 regexp the whole check command string must match>
Example: <a concrete check command that matches>
```

- The first `## ` header begins the stub list; prose before it is the header.
- **`category=`** (required, in the header comment) — the kind of verification.
  Recommended vocabulary (not closed): `unit`, `compile`, `lint`, `integration`,
  `e2e`, `pipeline`, `deploy`, `endpoint`, `artifact`. A category outside the set
  is a lint WARNING (typo guard), not an error.
- **`Match:`** (required) — an RE2 regexp matched against the WHOLE check command
  string. It must compile and must NOT match the empty string (a `.*`-style
  matcher would approve EVERY command — including a bare grep — and defeat the
  whitelist; it is a parse error).
- **`Example:`** (optional but recommended) — a concrete command that matches.

A stub names a REAL verification framework. **Never register a stub whose `Match:`
approves a source-text grep** — that reopens the hole the registry exists to
close. Reviewers reject such a stub.

## Matching

A task's check is APPROVED iff its command (trimmed) matches at least one stub's
`Match:` regexp. A compound check (`go test ./... && curl -sf …`) is approved as
long as it matches a stub — write the `Match:` to require the real framework
invocation, not an incidental substring.

## Enforcement points

| Concern | Enforced at | Code |
|---------|-------------|------|
| open task's check matches an approved stub | CLI linter | `beehive plan lint` (`cmd/beehive/cmd_plan.go`) |
| declared check matches an approved stub | handoff gate (Work/Review/Arbitrate terminal flip) | `verifyGate` invariant 4.5 (`internal/swarm/verify.go`) |
| plan-authoring pass emits only approved checks | reconcile/bootstrap completion gate | `checksApprovedItem` (`internal/swarm/swarm.go`) |
| author feedback on `task add/set-check` | CLI warning | `warnUnapprovedCheck` (`cmd/beehive/cmd_task.go`) |
| CHECKS.md schema (category, compilable non-empty Match) | parse | `internal/checks` |

## Migration

DONE tasks are grandfathered (history is never retro-gated). Only OPEN (non-DONE)
tasks' checks are constrained. A reconcile pass backfills approved checks and adds
the target's framework stubs to CHECKS.md; `beehive plan lint <sm>` reports the
backlog (unapproved checks + a missing registry) so coverage is visible as it
climbs.
