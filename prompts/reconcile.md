# ROI Reconcile Prompt (priority 0)

ROI.md changed since PLAN.md was last reconciled. Fold the intent changes into PLAN.md.

You are given the diff of ROI.md from the last-reconciled commit to HEAD (ROI.md scope only):
  git diff <last-reconciled-sha>..HEAD -- submodules/<sm>/ROI.md

- Read the diff. Update PLAN.md: add/modify/remove/retire tasks so the plan matches new intent.
- Preserve in-flight task status; retiring a task in flight -> NEEDS-REVIEW with a doc, not silent delete.
- Add design docs for new tasks. Tag dependencies. Rightsize for one context window.
- Update the PLAN.md ROI stamp to the current ROI.md commit: `<!-- Beehive-ROI: <sha> -->`.
- NEVER edit ROI.md. Commit PLAN.md to main; conflict -> reselect.
- Do NOT implement tasks; reconciliation ends at a committed, restamped PLAN.md.
