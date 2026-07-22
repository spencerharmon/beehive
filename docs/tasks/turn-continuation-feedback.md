# Turn continuation feedback

## Problem
Between turns, when a pass's completion predicate is not yet met, the runner sent
the agent the bare string `"continue"`. With no reminder of what "done" means for
its kind, the agent re-derived the finish conditions every turn — re-reading
protocol, doing git archaeology, and asking itself "am I done?" — burning tokens
for zero progress.

## Change
`internal/swarm`: the between-turn prompt (`Runner.nextPrompt`) now returns a
deterministic, runner-computed **status report** (`continuationReport`) that
enumerates every completion requirement for the pass's KIND and marks each
met/unmet, e.g.:

```
continue. Completion status for this work pass (runner-computed, authoritative —
do not re-derive or git-archaeology it). Finish the UNMET items, then STOP:
  [ ] terminal STATUS set (NEEDS-REVIEW | DONE | NEEDS-ARBITRATION)
  [x] change doc present at submodules/sm/docs/bee-T1-T1.md
When every item above is [x] the pass completes automatically — no extra checks or confirmation.
```

## Single source of truth
The report is built from `completionChecklist`, and `complete()` (the deterministic
completion check) is now **defined as** "every item in `completionChecklist` is
met". So the report can never disagree with what actually ends the pass — a met-all
report coincides exactly with completion. The old per-kind checks were folded into
`completionChecklist`, and the old `workDone` was replaced by `workChecklist` whose
AND is byte-for-byte the prior predicate (terminal status AND change-doc, with the
NEEDS-HUMAN early-return preserved as a single "escalation ready" item).

Per kind the checklist enumerates exactly the predicates the runner already checks:
- work: terminal STATUS set + change doc present at the exact path (NEEDS-HUMAN ->
  a single "escalation ready" item);
- review: task left NEEDS-REVIEW (-> DONE | NEEDS-ARBITRATION);
- arbitrate: task left NEEDS-ARBITRATION (-> DONE | TODO);
- reconcile: PLAN.md ROI stamp matches ROI HEAD;
- bootstrap: PLAN.md exists.

## Guarantees
- Runner-authored only, never agent-authored.
- Deterministic; reads only the beehive layer (PLAN.md, the docs/ dir, the ROI
  stamp) — no out-of-repo reads.
- Completion semantics and the turn cap are UNCHANGED — only the between-turn
  `"continue"` text the agent receives changed.
- A checklist read error degrades to the bare `"continue"` so the loop never fails
  on the report.

## Scope note
The lean-mode work-completion hint that `nextPrompt` previously fired at the
decision point is now subsumed by the report (which always names NEEDS-REVIEW and
the exact doc path), so it was removed in favor of the uniform per-kind checklist.
