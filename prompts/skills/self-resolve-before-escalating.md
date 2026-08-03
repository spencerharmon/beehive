# Skill: Self-resolve before escalating to NEEDS-HUMAN

> Read this BEFORE any `beehive task human`. Human escalation is a NARROW channel
> for what the swarm genuinely cannot do — not a work queue for the operator. The
> recorded escalation corpus shows the MAJORITY of NEEDS-HUMAN filings were
> self-resolvable and should never have blocked on a human. This skill is the
> "rule these out first" gate. Audience: honeybee (work / review / arbitrate).

The four legitimate categories are `secret`, `external-permission`, `contradiction`,
`architecture` (see HONEYBEE.md "Steps §4"). Everything below is a FALSE blocker that
masquerades as one of them. If any pattern fits, do the self-resolution — do NOT
`task human`.

## 1. "Install tool X" / the DoD `Check:` won't run in the sandbox — FALSE

There is **NO command allowlist — only a denylist.** A `Check:` may invoke anything
the agent runtime (opencode) can run: `go test`, `dotnet test`, `pytest`, `nix build`,
`helm`, `flux`, `kubectl`, `skopeo`, `curl`, a `tla2tools.jar` you fetched — the whole
universe EXCEPT a small denied set (fake-test tools `grep`/`find`/`cat`/`test -f`,
no-ops `true`/`echo`, and code-smuggling/destructive commands `bash -c`/`python -c`/
`… | sh`/`rm`/`dd`). The check is filesystem-confined to your submodule + its linked
submodules + `~/.kube`.

So "the tool isn't installed / isn't allowlisted" is **never** a blocker:
- Re-target the `Check:` to a real framework that runs in-sandbox and register it in
  `CHECKS.md` (e.g. `kubectl … | jq` asserting the LIVE deployed effect instead of a
  local `helm lint`; `skopeo inspect` of the pushed image instead of a local build).
- If a heavy build genuinely needs a prerequisite, BUILD/VENDOR it — fetch it in a
  build step, vendor it under the submodule, or run it in the gitea-Actions / zuul CI
  job the swarm owns and gate on the CI result. HONEYBEE.md forbids `task human` for a
  prerequisite the swarm can build itself.
- A tool needing a credential OUTSIDE your submodule + `~/.kube` is the ONE real
  operator hook: a `check_read_paths` config line — file it once, don't re-escalate.

## 2. "Commit unreachable / nothing to review / no implementer work" — FALSE (lost work)

A prior pass authored in its worktree but was capped/killed before the runner could
push `bee-<taskid>` + bump the gitlink, so the work is gone. There is NO decision for a
human — it just has to be redone.
- A **review/arbitrate** pass that finds NO reviewable work (branch + commit + change
  doc all absent) resets the task to TODO with `--commits-none` so a fresh implementer
  redoes it — it does NOT `task human`.
- If the deliverable ALREADY landed on the submodule `origin/main` (an early attempt
  pushed straight to main) and the gitlink already points at it, do not loop
  re-deriving "is it merged": reconcile the bookkeeping (stamp `commits=<sha>`, write
  the change doc, flip status) to what is already true on disk.
- A task that keeps losing work every pass and has burned `attempts` past the reject
  limit is the operator `reset-lost-work-task.md` case — but the DEFAULT move is redo,
  not escalate.

## 3. Stale / disproven premise & DONE-but-absent dependency — FALSE (dep hygiene, NOT contradiction)

A dependency is marked DONE but its effect is absent, or the task's spec went stale
against the code (references a removed API, a Kustomization that will never exist). A
status disagreeing with observed reality is **not** a `contradiction` (that is strictly
two OPPOSING stated intents). Two honest in-band outcomes:
- **Not yet converged** → DEFER and re-check (`skills/deferred-verification.md`).
- **Genuine gap / deprecated surface** → FILE a follow-up task in the OWNING submodule
  and `beehive task block` your task on it; OR if the task chases a removed/deprecated
  surface, de-scope/close it and prune its dangling `deps=`. A honeybee closes both.

## 4. "Can't verify — cache poisoned / private IP / not converged" — FALSE

In-cluster action is YOUR job, not a human's:
- Poisoned cache / wedged workload → restart/scale/clear via `kubectl` (within your
  `INFRASTRUCTURE.md` authority).
- A private IP unreachable from the sandbox → verify with an in-cluster probe
  (`kubectl exec` / a Job) or a `kubectl`-observable signal, not an outside-in curl.
- Async-but-pollable convergence → DEFER, never NEEDS-HUMAN.

## 5. A systemic block stranding N tasks — escalate ONCE

One git-remote/`.gitmodules` edit, one config line, or one runner defect can strand
many tasks (e.g. an HTTPS-vs-SSH submodule URL failing every push). Escalate it ONCE
with the single fix, and cross-link the stranded siblings to that one escalation.
Never file a fresh NEEDS-HUMAN per victim; for a recurring RUNNER defect, file ONE fix
task against the `beehive` submodule and block the victims on it.

## What DOES remain a real escalation

Only after all five are ruled out: a raw **secret** only the operator holds (author the
`provision-*.sh` bridge + Secret contract first, then escalate with the exact store key
+ literal command); an **external-permission** op outside GitOps/the cluster (host-root,
vendor, registrar — ship a reviewable executable artifact per
`needs-human-executable-artifact`); two genuinely OPPOSING stated intents
(**contradiction**); or a hard-to-reverse user-visible **architecture** decision.
