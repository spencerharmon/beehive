# Record of Intent — beehive

Intent for beehive itself: a swarm-coordinated AI coding platform where state is a git
repo and honeybees reconcile code to intent via commit-race. P0-P4 scaffolded; this ROI
sets next-step intent. Honeybees rightsize these into PLAN.md tasks.

## Goal
Make the swarm coordination real and safe, finish promised features, and bring the
frontend to a usable, attractive state. No placeholders, no fake locks.

## Correctness blockers (highest priority)
The race-safe guarantees are currently no-ops; coordination must actually work.

- Git remote ops: internal/git lacks Fetch, Pull, Push, HardReset. Commit-race, pointer
  auto-advance, and tracked-branch tip sync cannot occur. Add them; everything below
  depends on them.
- Claim lock is fake: claim.go verify re-reads the local file, never re-pulls. Two bees
  both win. Pull main after commit, reload, abandon on stamp mismatch. Same for Heartbeat.
- opencode turn engine is fire-and-forget: Prompt returns on accept, all turns burn in ms.
  Poll session until the assistant turn is idle before the completion check.
- Worktrees branch off HEAD with no sync; must fetch+hard-reset tracked tip first, then
  branch off the synced tip. Wire scripts/submodule-sync.sh into the runner.
- GC path orphans worktrees on cap: remove worktree (or record GC marker) at the cap, not
  only on DONE.

## Completeness vs plan
- Reconcile completion never fires: compare ROI stamp by prefix, not exact (short vs full
  sha). Replace "ROOT" sentinel with empty-tree sha for the diff range.
- Cross-submodule deps + wait-cycle detection unused in select; resolve linked-submodule
  deps and run links.HasCycle during candidate selection. Cycle check must run on honeybee
  dep-tag commits, not only CLI.
- internal/artifacts: model ARTIFACTS.md / INFRASTRUCTURE.md; web reads raw today.
- ROI protection: add server/pre-receive hook; pre-commit alone leaves pushes unprotected.
- Frontend write paths must reuse CLI logic: submodule add must `git submodule add` (not
  bare mkdir); link must go through links.AddDep (cycle-checked) and write valid YAML.
- Claimer.Reject must guard status (only NEEDS-REVIEW/ARBITRATION) before bumping attempts.

## Frontend aesthetics
Make beehived presentable and consistent.
- Replace ad-hoc style.css with a coherent design system: typography scale, spacing,
  status-color tokens (TODO/IN-PROGRESS/REVIEW/ARBITRATION/DONE/HUMAN), light+dark.
- Dashboard: submodule cards with live swarm status, env badge, NEEDS-HUMAN count.
- Plan view: clear state pills, dependency + heartbeat/TTL indicators, change-doc links.
- Branch graph: sectioned/paginated per submodule, commit-stamp linkage; no cross crawl.
- htmx interactions polished (loading states, inline edit, confirm on destructive merge).
- Keep single-binary embed; no SPA.

## Deferred features to complete
- Frontend perf cache: parse-once, invalidate on commit; state supported submodule ceiling.
- Multi-beehive management UI: manage/merge multiple beehive repos, per-repo keyrings for
  strict secret isolation.
- Release: confirm CI cross-compile + cosign signing produce verifiable static artifacts.

## Constraints
- Pure Go, static binaries (CGO_ENABLED=0). Single binary per component, embedded assets.
- ROI.md is human-owned; honeybees never edit it. opencode is the provider-agnostic agent.
- Every fix ships with tests; no weakened tests, no swallowed errors, no stub values.
