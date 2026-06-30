# session bee-links-graph-enforcement-1782781988

submodule: beehive · kind: review · branch: bee-links-graph-enforcement

## user

# Context (REVIEW — judge existing work, do NOT reimplement, do NOT set IN-PROGRESS)
cwd is the beehive repo root. Submodule: beehive. Task under review: links-graph-enforcement.
Beehive layer (read/write on main): submodules/beehive/PLAN.md, submodules/beehive/docs/. ROI.md is read-only.
Implementer's work is on branch bee-links-graph-enforcement in submodules/beehive/repo — inspect read-only via git (fetch from origin if the branch is absent locally). Change doc: submodules/beehive/docs/bee-links-graph-enforcement-links-graph-enforcement.md; the PLAN.md task body has a `Review:` note.
APPROVE -> merge the submodule pointer bump + PLAN.md task DONE + unlock dependents. REJECT -> PLAN.md task NEEDS-ARBITRATION + rejection doc submodules/beehive/docs/links-graph-enforcement-review-reject.md.
The run completes when the task leaves NEEDS-REVIEW. Act autonomously.

Claim: the runner stamped this task session=bee-1782781988 and re-stamps it each turn. Before doing work each turn, confirm submodules/beehive/PLAN.md still shows session=bee-1782781988 on task links-graph-enforcement with a fresh heartbeat. If a DIFFERENT session holds it, STOP immediately — you lost the race and the runner will reselect. Do not edit the session/heartbeat yourself.

# Review Prompt (task is NEEDS-REVIEW)

An implementer finished this task and set it NEEDS-REVIEW. You are the REVIEWER. Your job is to
JUDGE the existing work against the task and ROI — **do NOT reimplement it, do NOT set it back to
IN-PROGRESS.** The status you were given is real; treat it as a review, not fresh work.

What to read (all read-only):
- The task body in submodules/<sm>/PLAN.md, including its `Review:` note (implementer branch,
  submodule commit, change-doc path).
- The implementer's code on branch `bee-<taskid>` in the submodule checkout `submodules/<sm>/repo`.
  Inspect via git, e.g. `git -C submodules/<sm>/repo log/show/diff bee-<taskid>`. If the branch is
  not present locally, fetch it from the submodule origin first (`git -C submodules/<sm>/repo fetch
  origin bee-<taskid>`).
- The change doc at submodules/<sm>/docs/<branch>-<taskid>.md.

Then decide and commit on main:
- **APPROVE**: the work satisfies the task and ROI, tests pass. Merge the implementer's submodule
  pointer bump into the submodule's tracked branch, set the PLAN.md task -> DONE, and unlock any
  dependents (same plan or linked submodule). Commit.
- **REJECT**: it does not. Set the PLAN.md task -> NEEDS-ARBITRATION and write a rejection doc at
  submodules/<sm>/docs/<taskid>-review-reject.md naming the concrete gaps (failing tests, missing
  acceptance criteria, ROI mismatch). Commit. Do not delete or rewrite the implementer's branch.

The run completes when the task leaves NEEDS-REVIEW. Never edit ROI.md.



## assistant

> 💭 I need to verify my claim on this task and review the implementation, so I'm starting by checking the session status, reading through the task requirements in PLAN.md, examining the code changes on the branch, and reviewing the change documentation before deciding whether to approve or reject.

I'll start by confirming my claim and understanding the task under review.

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782781988/submodules/beehive/PLAN.md`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782781988/submodules/beehive/PLAN.md</path>
<type>file</type>
<content>
1: <!-- Beehive-ROI: bcda44a45c10ac8c54f2ca533966ca7ae31456c0 -->
2: # Plan — beehive
3: 
4: Rebootstrapped from the expanded ROI.md (the prior PLAN.md was deleted via the frontend
5: delete-plan/force-rebootstrap path). Decomposes intent into smallest parallelizable,
6: context-sized tasks. Each task has a terse design doc at docs/tasks/<id>.md (read it before
7: implementing).
8: 
9: Global constraints (every task): pure Go, static binaries (CGO_ENABLED=0); every fix ships
10: tests; no weakened tests, no swallowed errors, no stub values; NEVER edit ROI.md.
11: 
12: Priority order mirrors ROI: correctness blockers > completeness > configuration > frontend
13: aesthetics > generalized chat-diff editor > deferred. Weight encodes priority for selection;
14: deps gate ordering (resolve deps first). Many newer features (shipped chat editor, live
15: session list, polling panes, edit-worktrees) live on the submodule's tracked-branch tip,
16: ahead of the recorded pointer — sync the tip first (`beehive submodule sync beehive`) before
17: working any frontend/editor task, then anchor to the real code.
18: 
19: PLAN.md format is line-oriented (see internal/plan/plan.go): the ROI stamp comment, then
20: H2 task headers `## <id> [STATUS] <!-- attempts=N deps=a,b weight=W -->` with free-form body.
21: 
22: ## git-remote-ops [TODO] <!-- attempts=0 deps= weight=5 -->
23: Add Fetch, Pull, Push, HardReset to internal/git/git.go. FOUNDATION: commit-race claim,
24: pointer auto-advance, and tracked-tip sync are all no-ops without these. Unblocks
25: claim-repull-verify and worktree-sync-base.
26: Files: internal/git/git.go, internal/git/git_test.go.
27: Doc: docs/tasks/git-remote-ops.md
28: Accept: ctx-aware wrappers (fetch/prune, pull --ff-only, push, reset --hard <ref>) with
29: real error surfacing; unit tests against a temp local bare remote.
30: 
31: ## claim-repull-verify [DONE] <!-- attempts=0 deps=git-remote-ops weight=4 -->
32: Make the claim lock real. claim.go verify (claim.go:79) re-reads the LOCAL PLAN.md and never
33: re-pulls, so two bees both "win". After Commit in Claim (claim.go:57) and Heartbeat
34: (claim.go:95): pull beehive main, reload PLAN.md, verify our stamp owns it, ErrLost on
35: mismatch (abandon + reselect).
36: Files: internal/claim/claim.go, internal/claim/claim_test.go.
37: Doc: docs/tasks/claim-repull-verify.md
38: Accept: two-claimer race test yields exactly one winner; loser gets ErrLost.
39: Reconciled (ROI bcda44a): SHIPPED. ROI "Shipped since this ROI" now records the real claim race —
40: Claim/Heartbeat pull main, verify the session survived, and a publish-to-main conflict yields
41: ErrLost so the loser reselects instead of double-working. Closed as DONE; no further work.
42: 
43: ## claimer-reject-guard [DONE] <!-- attempts=0 deps= weight=3 -->
44: Claimer.Reject (claim.go:133) bumps Attempts unconditionally. Guard status first: only
45: NEEDS-REVIEW or NEEDS-ARBITRATION may be rejected (mirror plan.Task.Reject, state.go:56).
46: Reject from any other status must error and leave Attempts/Status untouched.
47: Files: internal/claim/claim.go, internal/claim/claim_test.go.
48: Doc: docs/tasks/claimer-reject-guard.md
49: Accept: reject on TODO/DONE (any non-review/arb status) errors; attempts unchanged; review/arb work.
50: Reconciled (ROI bcda44a): SHIPPED. ROI "Shipped since this ROI" records "Claimer.Reject is
51: status-guarded." Closed as DONE; no further work. (Note: IN-PROGRESS is no longer a status under
52: the unified claim model — "actively worked" is derived from session+heartbeat.)
53: 
54: ## opencode-turn-poll [TODO] <!-- attempts=0 deps= weight=4 -->
55: opencode turn engine is fire-and-forget: ocSession.Prompt (opencode.go:49) returns on accept,
56: so the runner's completion check runs before the assistant acts; all turns burn in ms.
57: Poll the session (GET session messages/state) until the assistant turn is idle before
58: returning from a turn / running complete().
59: Files: internal/swarm/opencode.go, internal/swarm/swarm.go (Run loop), swarm_test.go.
60: Doc: docs/tasks/opencode-turn-poll.md
61: Accept: Prompt blocks until turn idle; fake-server test proves no premature completion.
62: 
63: ## worktree-sync-base [TODO] <!-- attempts=0 deps=git-remote-ops weight=4 -->
64: Worktrees start from the recorded pointer, not the live tip, so bees work stale code. The honeybee
65: Work setup now inits the submodule at its recorded pointer and branches the worktree off HEAD
66: (swarm.go ~:78). At that insertion point, before branching, fetch + hard-reset the submodule checkout
67: to the tracked-branch tip (origin/<branch> from .gitmodules, default main) so the worktree always
68: starts from latest. Use the git-remote-ops verbs (Fetch + HardReset) on the submodule checkout;
69: advancing the beehive pointer to the synced tip is no-review.
70: Files: internal/swarm/swarm.go, internal/git/git.go, swarm_test.go.
71: Doc: docs/tasks/worktree-sync-base.md
72: Accept: runner fetch+hard-resets the tracked tip at the Work-setup insertion point before branching
73: the worktree; test asserts the new worktree base == remote tip (and any pointer move was committed).
74: 
75: ## gc-worktree-reclaim [TODO] <!-- attempts=0 deps= weight=3 -->
76: GC orphans worktrees on cap: Run sets GCMarked but never removes the worktree at the
77: turn/wall cap (swarm.go:116) — only the DONE path (swarm.go:108) reclaims. Remove the
78: worktree (or record a GC marker) at the cap too, leaving the stale session+heartbeat claim
79: metadata (no IN-PROGRESS status exists under the unified claim model) so stale GC reclaims/
80: re-TODOs the task. Do not flip the task status here.
81: Files: internal/swarm/swarm.go, swarm_test.go.
82: Doc: docs/tasks/gc-worktree-reclaim.md
83: Accept: cap path reclaims worktree/records marker; task status is unchanged and keeps a stale
84: session+heartbeat so stale-claim GC can reclaim it.
85: 
86: ## reconcile-prefix-match [TODO] <!-- attempts=0 deps= weight=3 -->
87: Reconcile completion never fires and the diff base is bogus. swarm.go reconciled()
88: (swarm.go:133) does exact `stamp == head` but stamp is short and head is full sha. Compare
89: by prefix. select.go reconcileRange (select.go:121) uses a "ROOT" sentinel for the empty
90: base; replace with the git empty-tree sha 4b825dc642cb6eb9a060e54bf8d69288fbee4904.
91: Files: internal/swarm/swarm.go, internal/select/select.go, *_test.go.
92: Doc: docs/tasks/reconcile-prefix-match.md
93: Accept: short/full-sha prefix match fires once and clears; empty-base diff range is valid.
94: 
95: ## links-graph-enforcement [NEEDS-REVIEW] <!-- attempts=0 deps= weight=3 session=bee-1782781988 heartbeat=2026-06-30T01:13:08Z -->
96: Cross-submodule deps and wait-cycle detection are unused in selection. Resolve linked-
97: submodule deps and run links.HasCycle during candidate selection (select.go fromSubmodule /
98: plan.Candidates). The cycle check must also run on honeybee dep-tag commits (PLAN.md writes),
99: not only the CLI (links.AddDep).
100: Files: internal/select/select.go, internal/links/links.go, internal/plan, commit-path guard.
101: Doc: docs/tasks/links-graph-enforcement.md
102: Accept: linked-submodule dep gates selection; a dep-tag commit forming a cycle is rejected.
103: Review: branch bee-links-graph-enforcement commit 03265b0 bumps the beehive pointer; change doc
104: submodules/beehive/docs/bee-links-graph-enforcement-links-graph-enforcement.md. Impl: select owns
105: the combined graph (select/graph.go LoadEdges/Validate/InCycle); plan stays links-free (Selectable
106: defers ":"-qualified deps); cross-submodule dep needs a SUBMODULE-LINKS link + DONE; cyclic TODO
107: tasks excluded; `beehive lint` + the pre-commit guard reject cyclic PLAN.md dep-tag commits. Tests
108: in links/plan/select/config + commit-boundary e2e; go test ./... green, vet clean, static build.
109: 
110: ## artifacts-package [TODO] <!-- attempts=0 deps= weight=2 -->
111: internal/artifacts is missing; the web reads ARTIFACTS.md / INFRASTRUCTURE.md raw
112: (web.go:129 explorer, env.go for INFRA). Add a typed model + parser, wire web reads through it.
113: Files: new internal/artifacts/*, internal/web/web.go, internal/web/env.go.
114: Doc: docs/tasks/artifacts-package.md
115: Accept: structured parse/render of both files; web stops reading raw; round-trip tests.
116: 
117: ## roi-pre-receive-hook [TODO] <!-- attempts=0 deps= weight=2 -->
118: ROI protection is incomplete: only a pre-commit hook exists (config/hook.go); pushes are
119: unprotected. Add a server-side pre-receive hook (mirror the honeybee-identity check) plus an
120: install path.
121: Files: internal/config/hook.go, internal/config/hook_test.go.
122: Doc: docs/tasks/roi-pre-receive-hook.md
123: Accept: a push touching ROI.md under honeybee identity is rejected; frontend push allowed.
124: 
125: ## frontend-write-paths-cli [TODO] <!-- attempts=0 deps= weight=2 -->
126: Frontend write paths bypass CLI logic. submoduleAdd does a bare mkdir (web.go:259) instead of
127: `git submodule add`; submoduleLink appends raw `from: [to]` YAML (web.go:276) instead of
128: links.AddDep (cycle-checked). Extract a shared helper from cmd/beehive/cmd_submodule.go and
129: call it from both CLI and web.
130: Files: internal/web/web.go, cmd/beehive/cmd_submodule.go, shared helper pkg, tests.
131: Doc: docs/tasks/frontend-write-paths-cli.md
132: Accept: web add creates a real tracked submodule; web link is cycle-checked valid YAML.
133: 
134: ## web-plan-parser-unify [TODO] <!-- attempts=0 deps= weight=3 -->
135: The web has its own PLAN.md parser (web/plan.go) expecting a legacy `- STATUS id` bullet format
136: that does NOT match the real H2 header format (internal/plan/plan.go: `## id [STATUS] <!-- ... -->`
137: with session/heartbeat claim metadata). Dashboard pending/NEEDS-HUMAN counts, plan view, and human
138: view all misparse real plans. Delegate web parsing to internal/plan; unify the duplicate roiStamp
139: regex. The frontend must derive active/stale from `session=<id>` + a fresh `heartbeat` (the unified
140: claim model), NOT from a status — there is no IN-PROGRESS status. Enabler for dashboard-cards and
141: plan-view-pills.
142: Files: internal/web/plan.go, internal/web/web.go, internal/web/env.go, web_test.go.
143: Doc: docs/tasks/web-plan-parser-unify.md
144: Accept: a real H2 header-format PLAN.md (with session/heartbeat metadata) parses; counts/pills
145: correct; active vs stale derived from session+heartbeat freshness; tests use the real format.
146: 
147: ## publish-main-writes [TODO] <!-- attempts=0 deps=git-remote-ops weight=2 -->
148: beehived writes commit locally but never push, so edits don't reach origin and other
149: hosts/honeybees never see them. Every handler uses s.commit (web.go:341): roiPost (web.go:182),
150: secretsPost (209), mergePost (232), submoduleAdd (259), submoduleLink (276), envDeploy (306).
151: Add publishMain (commit + push origin main, ff-retry on non-ff) and route all writes through it.
152: Files: internal/web/web.go, web_test.go.
153: Doc: docs/tasks/publish-main-writes.md
154: Accept: each write lands on origin main (temp-origin test); concurrent advance retries, no lost
155: write; no-origin install still commits locally.
156: 
157: ## config-layered [TODO] <!-- attempts=0 deps= weight=2 -->
158: config.Load (config.go:53) reads one flat /etc/beehive/config.yaml; no layering, no per-submodule
159: scope, no temperature/token-limit knobs. Add Resolve(root, submodule) merging layers most-
160: specific-wins: Defaults -> host file -> in-repo global -> per-submodule override; add
161: Temperature/MaxTokens and thread them into opencode (opencode.go:51). Callers resolve the
162: effective config per submodule at runtime.
163: Files: internal/config/config.go, config_test.go, internal/swarm/opencode.go.
164: Doc: docs/tasks/config-layered.md
165: Accept: per-scope precedence verified field-by-field (submodule>global>host>default); unset
166: fields fall through; bare single-host install still works.
167: 
168: ## design-system-css [TODO] <!-- attempts=0 deps= weight=2 -->
169: Replace the ad-hoc 4-line style.css with a coherent design system: typography scale, spacing
170: scale, and status-color tokens for TODO / NEEDS-REVIEW / NEEDS-ARBITRATION / DONE / NEEDS-HUMAN,
171: plus an "active" overlay derived from a fresh session+heartbeat (no IN-PROGRESS status exists), in
172: light + dark (prefers-color-scheme). Foundation for all frontend views. Keep the single-binary
173: embed (no SPA, no CDN).
174: Files: internal/web/assets/style.css, internal/web/templates/layout.html.
175: Doc: docs/tasks/design-system-css.md
176: Accept: tokens cover all five statuses plus the active overlay; dark mode works; assets stay embedded.
177: 
178: ## dashboard-cards [TODO] <!-- attempts=0 deps=design-system-css,web-plan-parser-unify -->
179: Dashboard as submodule cards: live swarm status (active/dormant/bootstrap), env badge
180: (blue/green), and a NEEDS-HUMAN count. Counts must come from the unified parser.
181: Files: internal/web/templates/dashboard.html, internal/web/web.go (subView).
182: Doc: docs/tasks/dashboard-cards.md
183: Accept: one card per submodule with correct state, env badge, and human count.
184: 
185: ## plan-view-pills [TODO] <!-- attempts=0 deps=design-system-css,web-plan-parser-unify -->
186: Plan view: clear status pills (tokenized), dependency indicators, an "active" overlay plus
187: heartbeat/TTL freshness derived from a fresh session+heartbeat (no IN-PROGRESS status), and
188: change-doc links from the commit stamp.
189: Files: internal/web/templates/plan_items.html, internal/web/web.go.
190: Doc: docs/tasks/plan-view-pills.md
191: Accept: pills per status; deps shown; active/stale derived from session+heartbeat freshness; doc links resolve.
192: 
193: ## branch-graph-sectioned [TODO] <!-- attempts=0 deps=design-system-css -->
194: Branch graph sectioned/paginated per submodule with commit-stamp linkage and no cross-
195: submodule crawl (branches.go already paginates one repo; add sectioning + stamp links UI).
196: Files: internal/web/templates/branch_view.html, internal/web/branches.go.
197: Doc: docs/tasks/branch-graph-sectioned.md
198: Accept: per-submodule sections, paginated, each commit links its Beehive change doc.
199: 
200: ## htmx-polish [TODO] <!-- attempts=0 deps=design-system-css -->
201: Polish htmx interactions: loading states/indicators, inline edit affordances, and a confirm
202: prompt on destructive merge. Keep single-binary embed; no SPA.
203: Files: internal/web/templates/*.html.
204: Doc: docs/tasks/htmx-polish.md
205: Accept: visible loading state on swaps; inline edit on ROI/secrets; merge asks confirm.
206: 
207: ## poll-scroll-preserve [TODO] <!-- attempts=0 deps=design-system-css weight=2 -->
208: htmx poll refresh replaces scrollable panes (markdown explorer, chat panel, session list),
209: resetting scrollTop to 0 mid-read. Patch-in-place (hx-preserve/morph or OOB row swaps) or
210: save/restore scrollTop around htmx:beforeSwap/afterSwap via embedded JS in the layout; keep
211: chat/stream bottom-pinned. (Polling panes live on the synced tip, ahead of 082d38c.)
212: Files: internal/web/templates/*.html, internal/web/assets/*, web_test.go.
213: Doc: docs/tasks/poll-scroll-preserve.md
214: Accept: polling fragments carry preserve/pane-id wiring and the restore script is embedded; live
215: updates not dropped; manual scroll check documented.
216: 
217: ## merge-button-wire [TODO] <!-- attempts=0 deps=git-remote-ops weight=2 -->
218: The merge action must publish a change, not no-op. mergePost (web.go:232) merges + commits LOCALLY
219: but never pushes origin nor advances/commits the beehive pointer; the branch-view publish control
220: is inert. Route merge through publishMain: Merge -> Push origin tracked-branch -> stage+commit the
221: pointer; wire the UI control with destructive-confirm.
222: Files: internal/web/web.go, internal/web/templates/merge_panel.html, branch_view.html, web_test.go.
223: Doc: docs/tasks/merge-button-wire.md
224: Accept: POST /merge ff's + pushes origin + advances pointer (temp-origin test); conflict returns
225: 409, origin untouched; idempotent on already-merged.
226: 
227: ## chat-diff-editor-core [TODO] <!-- attempts=0 deps=opencode-turn-poll weight=2 -->
228: Generalize the per-file editor into one chat-diff surface over any repo file. 082d38c has only a
229: textarea (roiGet/roiPost web.go:172-198); the shipped chat editor is on the synced tip. Add a
230: generic /edit?path= handler backed by an opencode session in a per-edit ROOT worktree: render
231: worktree state inline, propose a unified diff, apply+commit only on human approval. Reuse
232: opencode-turn-poll to await the assistant turn.
233: Files: internal/web/web.go, internal/web/templates/*, internal/swarm, web_test.go.
234: Doc: docs/tasks/chat-diff-editor-core.md
235: Accept: a chat turn for an arbitrary path yields a proposed diff; approve writes+commits in the
236: edit worktree, reject is a no-op; path traversal rejected. FOUNDATION for the chat-diff tasks.
237: 
238: ## chat-diff-file-context [TODO] <!-- attempts=0 deps=chat-diff-editor-core -->
239: Per-file edits need injected rules so they stay formatted/protocol-safe. Add a path->context
240: resolver (ROI.md conventions+FORBIDDEN, PLAN.md format/state machine per internal/plan, RULES.md +
241: AGENTS.md, generic default) and seed it into the opencode session (NewSession system arg,
242: opencode.go:25) for both per-file links and the generic window. Per-file links route into the
243: generic handler with path preset, replacing bare textareas.
244: Files: internal/web (context resolver + handlers), templates, web_test.go.
245: Doc: docs/tasks/chat-diff-file-context.md
246: Accept: distinct preambles per path; seeded prompt contains the target's rules; editing PLAN.md
247: still round-trips plan.Parse.
248: 
249: ## submodule-rules-md [TODO] <!-- attempts=0 deps=chat-diff-editor-core weight=2 -->
250: Add a per-submodule beehive-owned RULES.md at submodules/<sm>/RULES.md (outside repo/), additive
251: to any AGENTS.md, read into honeybee + chat-diff editor context and shown in the explorer
252: (web.go:129 docs map). Add the path constant alongside ROIFile/PlanFile/InfraFile/Artifacts
253: (internal/repo). Absence is a safe no-op.
254: Files: internal/repo (constants), internal/web/web.go, context resolver, web_test.go.
255: Doc: docs/tasks/submodule-rules-md.md
256: Accept: present RULES.md appears in explorer + agent/edit context; AGENTS.md+RULES.md both present,
257: order AGENTS-then-RULES; absence no-op; never auto-edits ROI.md.
258: 
259: ## chat-skills [TODO] <!-- attempts=0 deps=chat-diff-editor-core -->
260: Expose named, invocable maintenance skills from the chat surface: cleanup-stale, gc (reuse
261: gc-worktree-reclaim/editor-session-persist), resources (INFRASTRUCTURE.md rigs), infra-conventions.
262: Skills propose diffs via the approval path or report-only; destructive ones require confirm.
263: Files: internal/web (skill registry + chat wiring), templates, tests.
264: Doc: docs/tasks/chat-skills.md
265: Accept: registry lookup + dry-run returns a deterministic plan without mutating; applying performs
266: exactly the proposed change; unknown skill errors; no destructive action without approval.
267: 
268: ## frontend-perf-cache [TODO] <!-- attempts=0 deps=web-plan-parser-unify -->
269: Deferred. Parse-once frontend cache: cache derived views, invalidate on commit (HEAD change);
270: document the supported-submodule ceiling the cache assumes.
271: Files: internal/web (cache layer), web_test.go.
272: Doc: docs/tasks/frontend-perf-cache.md
273: Accept: repeated reads hit cache; a commit invalidates; ceiling documented + tested.
274: 
275: ## multi-beehive-ui [TODO] <!-- attempts=0 deps= -->
276: Deferred + coarse (re-decompose when prioritized). Manage/merge multiple beehive repos from
277: one frontend, with per-repo gpg keyrings for strict secret isolation.
278: Files: internal/web, internal/config, internal/secrets.
279: Doc: docs/tasks/multi-beehive-ui.md
280: Accept: register/switch repos; secrets never cross repo keyrings; design doc enumerates subtasks.
281: 
282: ## release-verify [TODO] <!-- attempts=0 deps= -->
283: Deferred. Confirm CI cross-compiles static binaries (CGO_ENABLED=0) and cosign-signs them, and
284: that the artifacts verify. Fix the pipeline if not.
285: Files: .github/workflows/*, packaging/*, docs/RELEASE-NOTES-TEMPLATE.md.
286: Doc: docs/tasks/release-verify.md
287: Accept: CI produces signed static artifacts; `cosign verify-blob` passes in a clean checkout.
288: 
289: ## editor-markdown-render [TODO] <!-- attempts=0 deps=design-system-css -->
290: Deferred. Editor/explorer render markdown as raw text (roiGet web.go:172 textarea; explorer
291: web.go:123 dumps strings; chat-diff shows raw diffs). Render markdown->HTML for VIEW panes with a
292: pure-Go CGO-free renderer (goldmark/gomarkdown), sanitized; keep an editable raw source view; style
293: diffs with design tokens.
294: Files: internal/web (render helper), templates, assets, web_test.go.
295: Doc: docs/tasks/editor-markdown-render.md
296: Accept: markdown renders to expected sanitized HTML; raw edit path returns verbatim; diff renders
297: with add/del classes; dependency is CGO-free.
298: 
299: ## editor-session-persist [TODO] <!-- attempts=0 deps= -->
300: Deferred. The shipped chat editor leaks ROOT edit worktrees (live: edit-* branches in git worktree
301: list) and loses in-flight sessions on restart. Persist session state (path, session id, branch,
302: activity); at startup prune stale edit-* worktrees + branches (TTL/no live session) via internal/git
303: Worktree* (mirror gc-worktree-reclaim), never touching bee-* or the main checkout.
304: Files: internal/web or internal/swarm (session store + startup prune), internal/git, tests.
305: Doc: docs/tasks/editor-session-persist.md
306: Accept: startup prune removes exactly stale edit worktrees+branches, keeps active/bee-*; session
307: state survives a simulated restart; never removes worktrees with pending approved changes.
308: 
309: ## gc-source-branch-reclaim [TODO] <!-- attempts=0 deps=git-remote-ops,gc-worktree-reclaim -->
310: Deferred. Honeybees now push the per-task `bee-<taskid>` source branch to the submodule remote so the
311: bumped pointer resolves for peers (an unpushed commit dangles the pointer for other hosts). Nothing
312: deletes those remote branches or their local worktrees, so both accumulate without bound. Reclaim
313: them in the GC path on DONE/cap: when a task is DONE (branch merged / pointer landed on main) or
314: GC'd past TTL, delete the remote `bee-<taskid>` branch on the submodule origin and reclaim its local
315: worktree/ref (rides gc-worktree-reclaim). Guard hard: only delete a branch whose pointer/merge is
316: already on main; never delete an unmerged in-flight branch. Needs a remote-delete verb (git-remote-
317: ops Push with a delete refspec, e.g. `git push origin --delete bee-<taskid>`).
318: Files: internal/swarm/swarm.go (DONE + cap reclaim), internal/git/git.go (delete remote branch), swarm_test.go.
319: Doc: docs/tasks/gc-source-branch-reclaim.md
320: Accept: a DONE/capped task's pushed bee-<taskid> branch is deleted on origin (temp-origin test) and
321: its worktree reclaimed; an unmerged in-flight branch is left intact; a missing branch is a no-op.
322: 
323: ## remote-host-session-view [TODO] <!-- attempts=0 deps=git-remote-ops -->
324: Deferred. Follow off-box honeybee runs: sessions are markdown under docs/sessions/<branch>.md.
325: Producer periodically commits+pushes session appends (remote-flagged cadence); viewer periodically
326: pulls --ff-only (git-remote-ops Pull) and re-renders the session pane, surfacing last-pulled
327: staleness. Combine with poll-scroll-preserve.
328: Files: internal/swarm (session writer cadence), internal/web (viewer pull+render), tests.
329: Doc: docs/tasks/remote-host-session-view.md
330: Accept: fake-origin periodic session commits are picked up by the viewer pull and rendered;
331: ff-only divergence handled; commits coalesced per interval.
332: 
333: ## agent-output-streaming [TODO] <!-- attempts=0 deps=opencode-turn-poll -->
334: Deferred. opencode is request/response (Prompt opencode.go:49 returns on accept). Consume opencode's
335: streaming/event channel to emit assistant tokens live; add a streaming read path in internal/swarm
336: beside the poll path (stream-end => turn-idle, consistent with opencode-turn-poll). Surface via
337: embedded SSE to the chat/session pane (bottom-pinned); fall back to poll-then-render if unsupported.
338: Files: internal/swarm/opencode.go, swarm_test.go, internal/web (SSE pane), templates.
339: Doc: docs/tasks/agent-output-streaming.md
340: Accept: fake streaming server tokens assemble incrementally; stream-end maps to turn-idle;
341: non-streaming server falls back; ctx-cancel leaks no goroutines.

(End of file - total 341 lines)
</content>
```


## assistant

> 💭 I'm checking the task details for `links-graph-enforcement` and confirming the session ID matches mine. The heartbeat timestamp shows 2026-06-30, but today is June 29, so it's dated for tomorrow.
> 
> Now I'm reviewing the task requirements: I need to integrate cross-submodule dependency resolution and cycle detection into the candidate selection process in `select.go`, ensuring that both the CLI and honeybee dep-tag commits validate against cycles. The implementation should move graph logic into a new `select/graph.go` module while keeping the plan layer dependency-free, and I need to update the documentation in the beehive submodule to reflect these changes.

The claim is confirmed — task `links-graph-enforcement` shows `session=bee-1782781988` with a fresh heartbeat. Let me set up my review plan and read the relevant docs.

**🔧 todowrite** `{"todos":[{"content":"Read task design doc docs/tasks/links-graph-enforcement.md","priority":"high","status":"in_progress"},{"content":"Read implementer change doc bee-links-graph-enforcement-links-gr…`

```
[
  {
    "content": "Read task design doc docs/tasks/links-graph-enforcement.md",
    "status": "in_progress",
    "priority": "high"
  },
  {
    "content": "Read implementer change doc bee-links-graph-enforcement-links-graph-enforcement.md",
    "status": "pending",
    "priority": "high"
  },
  {
    "content": "Inspect implementer branch bee-links-graph-enforcement code via git",
    "status": "pending",
    "priority": "high"
  },
  {
    "content": "Run tests/vet/build on implementer branch",
    "status": "pending",
    "priority": "high"
  },
  {
    "content": "Decide APPROVE or REJECT and commit on main",
    "status": "pending",
    "priority": "high"
  }
]
```


## assistant

> 💭 I'll start by examining the structure and then look into the relevant documentation.

**🔧 bash** `ls -la submodules/beehive/ && echo "---REPO---" && ls -la submodules/beehive/repo/ 2>/dev/null && echo "---DOCS---" && ls -la submodules/beehive/docs/ 2>/dev/null`

```
total 36
drwxr-xr-x 6 spencer spencer    92 Jun 29 20:13 .
drwxr-xr-x 3 spencer spencer    21 Jun 29 20:13 ..
drwxr-xr-x 4 spencer spencer   131 Jun 29 20:13 docs
-rw-r--r-- 1 spencer spencer 23531 Jun 29 20:13 PLAN.md
drwxr-xr-x 2 spencer spencer     6 Jun 29 20:13 repo
-rw-r--r-- 1 spencer spencer  6735 Jun 29 20:13 ROI.md
drwxr-xr-x 2 spencer spencer  4096 Jun 29 20:13 sessions
drwxr-xr-x 4 spencer spencer    62 Jun 29 20:13 worktrees
---REPO---
total 0
drwxr-xr-x 2 spencer spencer  6 Jun 29 20:13 .
drwxr-xr-x 6 spencer spencer 92 Jun 29 20:13 ..
---DOCS---
total 16
drwxr-xr-x 4 spencer spencer  131 Jun 29 20:13 .
drwxr-xr-x 6 spencer spencer   92 Jun 29 20:13 ..
-rw-r--r-- 1 spencer spencer 5373 Jun 29 20:13 bee-links-graph-enforcement-links-graph-enforcement.md
-rw-r--r-- 1 spencer spencer 2430 Jun 29 20:13 bee-reconcile-reconcile.md
drwxr-xr-x 2 spencer spencer   30 Jun 29 20:13 sessions
drwxr-xr-x 2 spencer spencer 4096 Jun 29 20:13 tasks

```


## assistant

> 💭 The submodule in `submodules/beehive/repo/` isn't initialized, so I need to check out the implementer's branch and get it set up. I'll start by reviewing the change doc and task design doc, then handle initializing the submodule.

The `repo/` checkout is empty (submodule not initialized). Let me read the docs first, then handle the submodule inspection.

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782781988/submodules/beehive/docs/bee-links-graph-enforcement-links-graph-enforcement.md`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782781988/submodules/beehive/docs/bee-links-graph-enforcement-links-graph-enforcement.md</path>
<type>file</type>
<content>
1: # links-graph-enforcement — cross-submodule dep gating + cycle guard
2: 
3: Beehive: links-graph-enforcement docs/bee-links-graph-enforcement-links-graph-enforcement.md
4: 
5: Branch `bee-links-graph-enforcement` (submodule beehive). Wires the previously
6: unused `internal/links` graph into task selection and the commit path: linked-
7: submodule dependencies now gate selection, and a PLAN.md dep-tag commit that
8: forms a wait cycle is rejected.
9: 
10: ## Why
11: `internal/links` (Edge, AddDep cycle-check, HasCycle) was only reachable from the
12: CLI and, in practice, dead: selection (`select.fromSubmodule` -> `plan.Candidates`
13: -> `plan.Selectable`) resolved only same-PLAN deps via `p.Task(d)`, so a dep on a
14: linked submodule's task was invisible and no wait-cycle check ran anywhere.
15: Honeybees that write dep tags to PLAN.md (never via the CLI's `links.AddDep`)
16: could deadlock the swarm with a cross-repo cycle and nothing would catch it.
17: 
18: ## What changed (how)
19: Qualified cross-submodule task id: `<submodule>:<taskid>`. A bare dep id is local.
20: 
21: - internal/links/links.go: exported `Cycle(edges)` (ordered cycle, reuses the
22:   existing DFS) and added `CyclicNodes(edges)` — Tarjan SCC returning every node
23:   on a cycle (SCC size > 1 or self-loop), deterministic over sorted adjacency.
24:   `AddDep`/`HasCycle`/`cycle` are unchanged.
25: - internal/select/graph.go (new): `Graph` is the combined, deterministic graph the
26:   selector owns. `LoadEdges(repo)` folds, across every submodule: PLAN.md dep tags
27:   (edges between qualified ids), SUBMODULE-LINKS.yaml `deps`, and the link
28:   adjacency (`submodules`). `Validate()` returns a cycle (for lint/guard);
29:   `InCycle(node)` flags cyclic nodes; `crossDepSatisfied(sm,dep)` requires a
30:   cross-submodule dep to be both authorized (sm links the target submodule) and
31:   DONE. Edge direction matches links: From depends on To.
32: - internal/select/select.go: `Select` loads the graph once and passes it down.
33:   New `graphGate` filters **main-tier (TODO)** candidates: drop tasks on a cycle,
34:   hold tasks whose cross-submodule prerequisite is unauthorized or not DONE.
35:   Recovery tiers (GC stale / arbitration / review) pass through untouched — they
36:   exist to unstick work, not start it.
37: - internal/plan/state.go: `Selectable` now skips deps containing ":" (cross-
38:   submodule) and defers them to the selector, so a TODO task with a cross-
39:   submodule dep reaches candidates instead of being silently unselectable. The
40:   plan layer stays links-free; local dep gating is unchanged.
41: - cmd/beehive/cmd_lint.go (new) + main.go: `beehive lint` loads the combined graph
42:   and exits non-zero with the cycle path on a wait cycle.
43: - internal/config/hook.go: the installed pre-commit guard now runs both checks —
44:   (1) ROI.md honeybee-protection (unchanged behavior) and (2) when a commit touches
45:   any PLAN.md it runs `beehive lint` and rejects a cycle. Restructured so the dep
46:   check runs for honeybee *and* frontend commits (the old script early-exited for
47:   non-honeybee). Best-effort: skipped if `beehive` is not on PATH. `InstallROIHook`
48:   keeps its name/signature.
49: 
50: ## Design choices / caveats
51: - select owns the combined graph; plan stays links-free (only a lexical ":" rule).
52: - Cross-submodule dep requires an explicit link (authorization). An unlinked
53:   qualified dep is treated as unsatisfiable and holds the task (surfaced by lint
54:   only if it also forms a cycle; pure unauthorized-but-acyclic deps just gate).
55: - Cycle exclusion uses on-cycle SCC membership. A task that merely depends
56:   (transitively) on a cycle is already held by normal dep gating (its prereq never
57:   goes DONE), so it is not separately flagged.
58: - The guard validates the working-tree graph at commit time (which reflects the
59:   staged PLAN.md edit); it shells to the `beehive` binary rather than re-embedding
60:   graph logic in shell. A server pre-receive (separate deferred task) still owns
61:   push-time ROI protection.
62: 
63: ## Tests
64: - internal/links: `TestCycleExported`, `TestCyclicNodes` (3-cycle membership,
65:   acyclic, self-loop).
66: - internal/plan: `TestSelectableDefersCrossSubmoduleDeps` (qualified dep deferred;
67:   local dep still gates).
68: - internal/select: `TestLinkedDepGatesSelection` (not-DONE linked dep holds the
69:   task), `TestLinkedDepDoneUnblocks` (DONE unblocks), `TestCrossDepRequiresLink`
70:   (unlinked dep gates even when DONE), `TestCyclicTasksNotSelected` (Validate +
71:   InCycle + nothing selected). e2e two-submodule hives.
72: - internal/config: `TestPreCommitHookGuards` (script wires both guards) and
73:   `TestPreCommitDepCycleGuardE2E` (real `git commit` rejected when stubbed lint
74:   fails, allowed when it passes; honeybee ROI.md commit still rejected).
75: - Full `go test ./...` green; `go vet ./...` clean; binaries build static
76:   (CGO_ENABLED=0, `not a dynamic executable`).
77: 
78: ## Follow-ups
79: - `beehive lint` validates the working tree, not the exact staged index; a partial
80:   stage that differs from the working tree could diverge. Acceptable for the dep-
81:   tag flow (honeybees stage the whole PLAN.md); tighten to `:0:`-index reads if a
82:   partial-stage path appears.
83: - SUBMODULE-LINKS.yaml `deps` (via `links.AddDep`) has no production writer yet; it
84:   is folded into the graph for completeness so a future writer is covered.
85: - Mirror the dep-cycle reject in the server-side pre-receive hook when
86:   roi-pre-receive-hook lands (pushes currently rely on the local pre-commit).

(End of file - total 86 lines)
</content>
```


## assistant

> 💭 Let me look at the task design doc.

Now the task design doc and the ROI:

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782781988/submodules/beehive/docs/tasks/links-graph-enforcement.md`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782781988/submodules/beehive/docs/tasks/links-graph-enforcement.md</path>
<type>file</type>
<content>
1: # links-graph-enforcement
2: 
3: ROI: "Cross-submodule deps + wait-cycle detection unused in select; resolve linked-submodule deps
4: and run links.HasCycle during candidate selection. Cycle check must run on honeybee dep-tag
5: commits, not only CLI."
6: 
7: ## Why
8: `internal/links` (Links, AddDep cycle-check, HasCycle) is only used by the CLI
9: (cmd_submodule.go submoduleLinkCmd). Selection (`select.go fromSubmodule` -> `plan.Candidates`
10: -> `plan.Selectable`, state.go:72) only resolves same-PLAN deps (`p.Task(d)`); a dep on a linked
11: submodule's task is invisible, and no wait-cycle check runs. Honeybees that add dep tags to
12: PLAN.md can introduce cycles with no guard.
13: 
14: ## Do
15: Two integration points:
16: 1. Selection: load `SUBMODULE-LINKS.yaml` for the submodule; when a dep id refers to a linked
17:    submodule's task, resolve its status there and gate selection on it being DONE. Run
18:    `links.HasCycle()` (over combined dep edges incl. PLAN dep tags) and skip/ýexclude tasks in a
19:    cycle rather than deadlock.
20: 2. Commit-time: validate dep-tag writes. When a honeybee commit changes PLAN.md deps, reject (or
21:    refuse to select into) a state whose dependency graph has a cycle — reuse `links.AddDep`/
22:    `cycle()` semantics. A pre-commit-style guard or a validation in the claim/plan write path.
23: 
24: ## Tests
25: - Linked submodule task not-DONE gates selection; DONE unblocks it.
26: - A cyclic dep set is detected by HasCycle and the cyclic task is not selected.
27: - A dep-tag commit forming a cycle is rejected.
28: 
29: ## Caveats
30: - Edge direction: Edge{From,To} means From depends on To (links.go:24). Match PLAN dep semantics
31:   (Task.Deps are prerequisites). 
32: - Cross-submodule task ids need a qualified form (e.g. `<submodule>:<taskid>`); define and document
33:   it. Keep deterministic, no LLM.

(End of file - total 33 lines)
</content>
```


## assistant

> 💭 I'm checking the ROI.md file to see what the original intent behind this project is.

Now let me read the ROI to validate against intent:

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782781988/submodules/beehive/ROI.md`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782781988/submodules/beehive/ROI.md</path>
<type>file</type>
<content>
1: # Record of Intent — beehive
2: 
3: Intent for beehive itself: a swarm-coordinated AI coding platform where state is a git
4: repo and honeybees reconcile code to intent via commit-race. P0-P4 scaffolded; this ROI
5: sets next-step intent. Honeybees rightsize these into PLAN.md tasks.
6: 
7: ## Goal
8: Make the swarm coordination real and safe, finish promised features, and bring the
9: frontend to a usable, attractive state. No placeholders, no fake locks.
10: 
11: ## Correctness blockers (highest priority)
12: The race-safe guarantees are currently no-ops; coordination must actually work.
13: 
14: - Git remote ops: internal/git lacks Fetch, Pull, Push, HardReset. Commit-race, pointer
15:   auto-advance, and tracked-branch tip sync cannot occur. Add them; everything below
16:   depends on them.
17: - opencode turn engine is fire-and-forget: Prompt returns on accept, all turns burn in ms.
18:   Poll session until the assistant turn is idle before the completion check.
19: - Worktrees branch off HEAD with no sync; must fetch+hard-reset tracked tip first, then
20:   branch off the synced tip. The honeybee Work setup now inits the submodule at its
21:   recorded pointer and branches off HEAD; wire the fetch + hard-reset to the tracked-branch
22:   tip at that insertion point so worktrees always start from latest.
23: - GC path orphans worktrees on cap: remove worktree (or record GC marker) at the cap, not
24:   only on DONE.
25: 
26: ## Completeness vs plan
27: - Reconcile completion never fires: compare ROI stamp by prefix, not exact (short vs full
28:   sha). Replace "ROOT" sentinel with empty-tree sha for the diff range.
29: - Cross-submodule deps + wait-cycle detection unused in select; resolve linked-submodule
30:   deps and run links.HasCycle during candidate selection. Cycle check must run on honeybee
31:   dep-tag commits, not only CLI.
32: - internal/artifacts: model ARTIFACTS.md / INFRASTRUCTURE.md; web reads raw today.
33: - ROI protection: add server/pre-receive hook; pre-commit alone leaves pushes unprotected.
34: - Frontend write paths must reuse CLI logic: submodule add must `git submodule add` (not
35:   bare mkdir); link must go through links.AddDep (cycle-checked) and write valid YAML.
36: - web/plan.go still parses a legacy `- STATUS id` bullet format, not the real H2 PLAN.md
37:   (`## id [STATUS] <!-- ... -->`) with session/heartbeat claim metadata. Reconcile it with
38:   internal/plan so the frontend derives active/stale from session+heartbeat, not a status.
39: 
40: ## Frontend aesthetics
41: Make beehived presentable and consistent.
42: - Replace ad-hoc style.css with a coherent design system: typography scale, spacing,
43:   status-color tokens (TODO/REVIEW/ARBITRATION/DONE/HUMAN) plus an "active" overlay derived
44:   from a fresh session+heartbeat (no IN-PROGRESS status exists), light+dark.
45: - Dashboard: submodule cards with live swarm status, env badge, NEEDS-HUMAN count.
46: - Plan view: clear state pills, dependency + heartbeat/TTL indicators, change-doc links.
47: - Branch graph: sectioned/paginated per submodule, commit-stamp linkage; no cross crawl.
48: - htmx interactions polished (loading states, inline edit, confirm on destructive merge).
49: - Poll refresh must preserve scroll: live updates reset the markdown pane and chat panel to
50:   top; restore scroll position (or patch in place) so reading isn't interrupted.
51: - Merge button is inert: wire the action so it actually publishes/merges the change instead
52:   of no-op'ing.
53: - Keep single-binary embed; no SPA.
54: 
55: ## Deferred features to complete
56: Shipped since this ROI: per-honeybee worktree isolation with publish-to-main (no write
57: lock), native worktree/submodule CLI subcommands, unique epoch session IDs, live
58: auto-refreshing session list, delete-plan button forcing rebootstrap from ROI, per-turn
59: guard that aborts a honeybee when its task leaves main, and an AI chat editor for
60: human-owned files. Unified claim model: IN-PROGRESS is no longer a status — "actively
61: worked" is derived from `session=<id>` + a fresh `heartbeat` carried by any status, with one
62: per-turn heartbeat across Work/Review/Arbitration. The claim race is now real: Claim/Heartbeat
63: pull main, verify the session survived, and a publish-to-main conflict yields ErrLost so the
64: loser reselects another task instead of double-working. Claimer.Reject is status-guarded.
65: 
66: Still pending:
67: - Frontend perf cache: parse-once, invalidate on commit; state supported submodule ceiling.
68: - Multi-beehive management UI: manage/merge multiple beehive repos, per-repo keyrings for
69:   strict secret isolation.
70: - Release: confirm CI cross-compile + cosign signing produce verifiable static artifacts.
71: - Editor diffs: pretty markdown->HTML rendering; raw text today.
72: - Editor sessions: persistence + startup prune of stale edit worktrees.
73: - Remote-host live session view: periodic commits + git pull to follow off-box runs.
74: - Token-by-token streaming of agent output.
75: - Fold publishMain into all beehived write paths.
76: - Pushed source-branch reclamation: honeybees now push the per-task `bee-<taskid>` source
77:   branch to the submodule remote so the bumped pointer resolves for peers; these branches
78:   accumulate. Reclaim them (with stale worktrees) in the GC path on DONE/cap.
79: 
80: ## Configuration
81: Layered config, most-specific wins: /etc/beehive (host) -> in-repo global defaults ->
82: per-submodule overrides. Tunable per scope: agent backend URL (opencode for now) and
83: model settings (model, temperature, token limits). Ship sane defaults and resolve the
84: effective value at runtime.
85: 
86: ## Generalized chat-diff editor
87: Generalize the human-file editor into one chat-diff surface over the whole beehive repo.
88: - Generic chat window: discuss and apply edits across any repo file; the agent renders
89:   worktree state inline (like today's editor) and proposes diffs for approval.
90: - Per-file edit links reuse the same chat-diff interface, injecting file-specific context
91:   (ROI conventions, honeybee protocol) so edits stay correctly formatted and protocol-safe;
92:   the generic window pulls the same context and rules per target file.
93: - Per-submodule RULES.md: beehive-owned rules + context, out-of-tree from the submodule's
94:   own source but kept in its dir, editable by user and agent via the chat-diff editor.
95:   Respect an existing AGENTS.md if present; RULES.md is additive.
96: - Agent skills invocable from the chat surface: cleanup of old/unused files after beehive
97:   updates, garbage collection, resource management, infrastructure conventions.
98: 
99: ## Constraints
100: - Pure Go, static binaries (CGO_ENABLED=0). Single binary per component, embedded assets.
101: - ROI.md is human-owned; honeybees never edit it. opencode is the provider-agnostic agent.
102: - Every fix ships with tests; no weakened tests, no swallowed errors, no stub values.

(End of file - total 102 lines)
</content>
```


## assistant

> 💭 Looking at the ROI notes about cross-submodule dependencies and cycle detection—it seems the current implementation isn't running the cycle check during candidate selection, only at the CLI level, and it's not being applied to honeybee dep-tag commits. I need to examine the submodule repository to understand the current state and figure out how to properly integrate the cycle detection into the selection process. Let me check the git configuration and submodule status to see what I'm working with.

Now let me inspect the git state of the submodule to access the implementer's branch.

**🔧 bash** `cat .gitmodules 2>/dev/null; echo "===GITMODULES IN SM==="; cat submodules/beehive/.gitmodules 2>/dev/null; echo "===STATUS==="; git -C submodules/beehive/repo status 2>&1 | head -5; echo "===SM CONFIG==="; git config -f .gitmodules --list 2>/dev/null`

```
[submodule "submodules/beehive/repo"]
	path = submodules/beehive/repo
	url = git@github.com:spencerharmon/beehive.git
	branch = main
===GITMODULES IN SM===
===STATUS===
On branch bee-1782781988
nothing to commit, working tree clean
===SM CONFIG===
submodule.submodules/beehive/repo.path=submodules/beehive/repo
submodule.submodules/beehive/repo.url=git@github.com:spencerharmon/beehive.git
submodule.submodules/beehive/repo.branch=main

```


## assistant

> 💭 I'm checking the submodule status and noticing that `git -C submodules/beehive/repo status` is returning the parent worktree's branch name (bee-1782781988), which suggests git is walking up to the parent directory since the repo subdirectory appears to be empty.
> 
> The submodule isn't initialized, so I need to initialize it first. Let me examine the worktrees directory and understand the overall git structure.

**🔧 bash** `echo "===WORKTREES DIR==="; ls -la submodules/beehive/worktrees/; echo "===ROOT GIT==="; git rev-parse --show-toplevel; echo "===SUBMODULE STATUS==="; git submodule status submodules/beehive/repo 2>&1; echo "===LS-FILES SM==="; git ls-files --stage submodules/beehive/repo 2>&1 | head`

```
===WORKTREES DIR===
total 0
drwxr-xr-x 4 spencer spencer 62 Jun 29 20:13 .
drwxr-xr-x 6 spencer spencer 92 Jun 29 20:13 ..
drwxr-xr-x 2 spencer spencer  6 Jun 29 20:13 bee-bootstrap
drwxr-xr-x 2 spencer spencer  6 Jun 29 20:13 bee-links-graph-enforcement
===ROOT GIT===
/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782781988
===SUBMODULE STATUS===
-03265b0db625ea2086a1d8c20948cc014d3ee980 submodules/beehive/repo
===LS-FILES SM===
160000 03265b0db625ea2086a1d8c20948cc014d3ee980 0	submodules/beehive/repo

```


## assistant

> 💭 The submodule pointer in this review worktree is already pointing to `03265b0`, which matches the commit mentioned in the review notes about bumping the beehive pointer. The `-` prefix indicates the submodule isn't initialized, but the recorded pointer is interesting because it's already at that specific commit.
> 
> I need to check the parent repo's git log and branch state to understand whether the pointer bump has already been merged into main or if that's still pending. Let me initialize the submodule first so I can examine the actual state of things.

The parent's submodule pointer already records `03265b0` (the implementer's commit). Let me understand the parent repo's branch/merge state and initialize the submodule to inspect the code.

**🔧 bash** `echo "===PARENT LOG==="; git log --oneline -15; echo "===PARENT BRANCHES==="; git branch -a 2>&1 | head -20; echo "===PARENT STATUS==="; git status -sb | head`

```
===PARENT LOG===
ae0853f plan: claim links-graph-enforcement
d909cad Merge branch 'main' into bee-1782781231
83bcce7 session: bee-reconcile-1782781231
e3fd921 session: bee-reconcile-1782781231
bd4a57f session: bee-reconcile-1782781231
e45cacf session: bee-reconcile-1782781231
34dd5d4 session: bee-reconcile-1782781231
5fb2cb2 plan: reconcile PLAN.md to ROI bcda44a (unified claim model)
e890de4 session: bee-reconcile-1782781231
e787778 session: bee-reconcile-1782781231
81f2590 session: bee-reconcile-1782781231
09f40f2 session: bee-reconcile-1782781231
b27aa7f session: bee-reconcile-1782781231
5ef78b8 session: bee-reconcile-1782781231
3798dd8 session: bee-reconcile-1782781231
===PARENT BRANCHES===
* bee-1782781988
+ bee-1782781988-session
  bee-links-graph-enforcement
+ edit-INFRASTRUCTURE-md-1782765563
+ edit-INFRASTRUCTURE-md-1782765744
+ edit-submodules-beehive-ROI-md-1782762882
+ main
  master
===PARENT STATUS===
## bee-1782781988

```


## assistant

> 💭 I'm looking at the branch structure here. My current worktree is on `bee-1782781988` with the latest commit being a plan for the links-graph-enforcement feature. I need to trace how this relates to the parent repo's `bee-links-graph-enforcement` branch and main to understand what's already been implemented versus what I'm working on.
> 
> The key thing is understanding the two-layer setup: the parent repo (infra-beehive) manages the submodule pointer and PLAN.md, while the actual Go code lives in the beehive submodule. The implementer created a worktree in the submodule, committed code on the `bee-links-graph-enforcement` branch there, and then bumped the parent pointer. Now I need to compare what's on the parent's `bee-links-graph-enforcement` branch against my review branch to see what's changed.

**🔧 bash** `echo "===PARENT: links branch log vs mine==="; git log --oneline -8 bee-links-graph-enforcement; echo "===DIFF main..bee-links-graph-enforcement (parent)==="; git diff --stat main..bee-links-graph-enforcement; echo "===DIFF mine..links branch==="; git diff --stat bee-1782781988..bee-links-graph-enforcement`

```
===PARENT: links branch log vs mine===
9eee2a0 plan: bump beehive pointer for links-graph-enforcement
d954fdf plan: claim links-graph-enforcement
fe6ab9f session: bee-bootstrap-1782766865
52f61c0 plan: rebootstrap beehive PLAN.md from expanded ROI (32 tasks)
3e56638 editor: INFRASTRUCTURE.md
af3f909 editor: submodules/beehive/ROI.md
b985ef3 editor: submodules/beehive/ROI.md
addd232 editor: submodules/beehive/ROI.md
===DIFF main..bee-links-graph-enforcement (parent)===
 AGENTS.md                                          |   56 +-
 submodules/beehive/PLAN.md                         |   87 +-
 submodules/beehive/ROI.md                          |   22 +-
 ...ks-graph-enforcement-links-graph-enforcement.md |   86 -
 submodules/beehive/docs/bee-reconcile-reconcile.md |   40 -
 submodules/beehive/docs/tasks/design-system-css.md |   10 +-
 .../beehive/docs/tasks/gc-source-branch-reclaim.md |   33 -
 .../beehive/docs/tasks/gc-worktree-reclaim.md      |    9 +-
 submodules/beehive/docs/tasks/plan-view-pills.md   |    9 +-
 .../beehive/docs/tasks/worktree-sync-base.md       |   28 +-
 submodules/beehive/repo                            |    2 +-
 .../bee-links-graph-enforcement-1782767318.md      | 6948 --------------------
 .../bee-links-graph-enforcement-1782772942.md      | 4203 ------------
 .../bee-links-graph-enforcement-1782781988.md      |  807 ---
 .../beehive/sessions/bee-reconcile-1782772649.md   | 1036 ---
 .../beehive/sessions/bee-reconcile-1782781231.md   | 1751 -----
 .../beehive/worktrees/bee-links-graph-enforcement  |    1 -
 17 files changed, 94 insertions(+), 15034 deletions(-)
===DIFF mine..links branch===
 AGENTS.md                                          |   56 +-
 submodules/beehive/PLAN.md                         |   87 +-
 submodules/beehive/ROI.md                          |   22 +-
 ...ks-graph-enforcement-links-graph-enforcement.md |   86 -
 submodules/beehive/docs/bee-reconcile-reconcile.md |   40 -
 submodules/beehive/docs/tasks/design-system-css.md |   10 +-
 .../beehive/docs/tasks/gc-source-branch-reclaim.md |   33 -
 .../beehive/docs/tasks/gc-worktree-reclaim.md      |    9 +-
 submodules/beehive/docs/tasks/plan-view-pills.md   |    9 +-
 .../beehive/docs/tasks/worktree-sync-base.md       |   28 +-
 submodules/beehive/repo                            |    2 +-
 .../bee-links-graph-enforcement-1782767318.md      | 6948 --------------------
 .../bee-links-graph-enforcement-1782772942.md      | 4203 ------------
 .../beehive/sessions/bee-reconcile-1782772649.md   | 1036 ---
 .../beehive/sessions/bee-reconcile-1782781231.md   | 1751 -----
 .../beehive/worktrees/bee-links-graph-enforcement  |    1 -
 16 files changed, 94 insertions(+), 14227 deletions(-)

```


## assistant

> 💭 I'm noticing the parent branch `bee-links-graph-enforcement` is based on an older version of main than my current review branch, which is why the diff shows it would delete newer files and revert recent changes to documentation and configuration files. The parent branch was created from an earlier state of main, while my branch is built on a more recent version.
> 
> For the review, what really matters is the submodule code change at commit 03265b0 and whether to update the parent's submodule pointer to that commit to mark the task complete. I'm checking whether my review branch already has the submodule pointer set to 03265b0 — the diff shows the submodule pointer differs between my branch and the links branch, so I need to verify exactly what each branch is pointing to. The key insight is that my review worktree was created off the current main tip, so if my branch already has the 03265b0 pointer, that tells me something important about the state of main.
> 
> Let me systematically check the exact pointer values across main, my branch, and the links branch.

The parent `bee-links-graph-enforcement` branch is based on an older main. Let me precisely compare the submodule pointer across refs since that's what the review hinges on.

**🔧 bash** `for ref in main bee-1782781988 bee-links-graph-enforcement; do echo "=== $ref ==="; git ls-tree $ref submodules/beehive/repo; done; echo "=== HEAD ==="; git ls-tree HEAD submodules/beehive/repo`

```
=== main ===
160000 commit 03265b0db625ea2086a1d8c20948cc014d3ee980	submodules/beehive/repo
=== bee-1782781988 ===
160000 commit 03265b0db625ea2086a1d8c20948cc014d3ee980	submodules/beehive/repo
=== bee-links-graph-enforcement ===
160000 commit 3266f493ce6db7879966e154450e188ae09e0718	submodules/beehive/repo
=== HEAD ===
160000 commit 03265b0db625ea2086a1d8c20948cc014d3ee980	submodules/beehive/repo

```


## assistant

> 💭 I'm noticing a discrepancy in the submodule pointers across branches. The main branch and my review branch both point to 03265b0, but the parent branch bee-links-graph-enforcement points to 3266f49. The review note mentions that 03265b0 is the commit that bumps the beehive pointer on bee-links-graph-enforcement, but that doesn't match what I'm seeing—the parent branch is actually pointing to a different commit. pointer 3266f49. I need to check if 3266f49 is an ancestor or descendant of 03265b0 in the submodule to understand the relationship. But stepping back, my actual job is to review the implementer's submodule code at 03265b0, verify it works correctly, and then either approve (set PLAN task to DONE and unlock dependents) or reject (set to NEEDS-ARBITRATION). The fact that main already has the pointer at 03265b0 and the change doc is present suggests the pointer bump and task flip to NEEDS-REVIEW may have already landed together on main when the implementer finished.
> 
> The parent branch `bee-links-graph-enforcement` points to 3266f49, which is a different commit — this might be an older artifact from before the reconcile, with 03265b0 being the final submodule commit the implementer actually wants reviewed. I should focus on inspecting the submodule at 03265b0 directly rather than getting sidetracked by the parent branch's pointer.

Key finding: `main` and my review branch already record submodule pointer `03265b0`. The parent `bee-links-graph-enforcement` branch records a different one (`3266f49`). I need to inspect the submodule code. Let me locate the submodule's git object store.

**🔧 bash** `echo "===GITDIR==="; git rev-parse --git-dir; echo "===COMMON==="; git rev-parse --git-common-dir; echo "===MODULES==="; ls -la "$(git rev-parse --git-common-dir)/modules/submodules/beehive/repo" 2>&1 | head; echo "===FIND MODULE DIRS==="; ls -la "$(git rev-parse --git-common-dir)/modules/submodules/beehive/" 2>&1 | head`

```
===GITDIR===
/home/spencer/git-repos/spencerharmon/infra-beehive/.git/worktrees/bee-1782781988
===COMMON===
/home/spencer/git-repos/spencerharmon/infra-beehive/.git
===MODULES===
total 36
drwxr-xr-x  8 spencer spencer  164 Jun 29 18:44 .
drwxr-xr-x  3 spencer spencer   18 Jun 29 02:22 ..
-rw-r--r--  1 spencer spencer  312 Jun 29 12:50 config
-rw-r--r--  1 spencer spencer   73 Jun 29 02:22 description
-rw-r--r--  1 spencer spencer   21 Jun 29 02:22 HEAD
drwxr-xr-x  2 spencer spencer 4096 Jun 29 02:22 hooks
-rw-r--r--  1 spencer spencer 8298 Jun 29 02:44 index
drwxr-xr-x  2 spencer spencer   21 Jun 29 02:22 info
drwxr-xr-x  3 spencer spencer   30 Jun 29 02:22 logs
===FIND MODULE DIRS===
total 0
drwxr-xr-x 3 spencer spencer  18 Jun 29 02:22 .
drwxr-xr-x 3 spencer spencer  21 Jun 29 02:22 ..
drwxr-xr-x 8 spencer spencer 164 Jun 29 18:44 repo

```


## assistant

> 💭 The submodule's git object store is accessible at `.git/modules/submodules/beehive/repo`, so I can use that as the git directory to examine the submodule's commit history. I'm checking what commits are available in there—looks like I have 03265b0 and 3266f49 to work with.

The submodule object store exists. Let me inspect the submodule commits directly using that git dir.

**🔧 bash** `SMG="/home/spencer/git-repos/spencerharmon/infra-beehive/.git/modules/submodules/beehive/repo"; echo "===BRANCHES==="; git --git-dir="$SMG" branch -a 2>&1; echo "===03265b0?==="; git --git-dir="$SMG" cat-file -t 03265b0 2>&1; echo "===3266f49?==="; git --git-dir="$SMG" cat-file -t 3266f49 2>&1; echo "===LOG 03265b0==="; git --git-dir="$SMG" log --oneline -10 03265b0 2>&1`

```
===BRANCHES===
+ bee-bootstrap
+ bee-links-graph-enforcement
* main
  remotes/origin/HEAD -> origin/main
  remotes/origin/main
===03265b0?===
fatal: Not a valid object name 03265b0
===3266f49?===
commit
===LOG 03265b0===
fatal: ambiguous argument '03265b0': unknown revision or path not in the working tree.
Use '--' to separate paths from revisions, like this:
'git <command> [<revision>...] -- [<file>...]'

```


## assistant

> 💭 The submodule's object store is missing the commit that main points to (03265b0), but it does have 3266f49, which is what the `bee-links-graph-enforcement` branch references instead. This is a mismatch — the parent repository's pointer doesn't correspond to anything actually stored in the submodule locally. I need to investigate what's on that branch to understand the discrepancy.

**🔧 bash** `SMG="/home/spencer/git-repos/spencerharmon/infra-beehive/.git/modules/submodules/beehive/repo"; echo "===SM branch tips==="; git --git-dir="$SMG" log --oneline -1 bee-links-graph-enforcement 2>&1; git --git-dir="$SMG" log --oneline -1 main 2>&1; git --git-dir="$SMG" log --oneline -1 origin/main 2>&1; echo "===LOG bee-links branch==="; git --git-dir="$SMG" log --oneline -12 bee-links-graph-enforcement 2>&1`

```
===SM branch tips===
3266f49 feat: enforce cross-submodule dep gating + wait-cycle guard in selection
082d38c fix: init writes full embedded AGENTS.md protocol, not placeholder
082d38c fix: init writes full embedded AGENTS.md protocol, not placeholder
===LOG bee-links branch===
3266f49 feat: enforce cross-submodule dep gating + wait-cycle guard in selection
082d38c fix: init writes full embedded AGENTS.md protocol, not placeholder
d5e2095 fix: build static binaries (CGO_ENABLED=0)
15c71de Merge branch 'cave/agent/implementer-mqytmztc-3'
9c9e5a8 Merge branch 'cave/agent/implementer-mqytmzt9-2'
eb9fb24 Merge branch 'cave/agent/implementer-mqytmzt6-1'
2745304 P2: wire honeybee runner
48139d0 P2: honeybee turn loop + opencode session client
52d366f P2: commit-race claim, re-verify, heartbeat, GC, reject counter
40d9728 P2: deterministic weighted task selection
032d20b P2: PLAN.md parser, status machine, deps/cycle, TTL
5f63fdc P1: CLI deterministic ops + plan/secrets/links

```


## assistant

> 💭 I'm seeing the git structure now — the submodule branch `bee-links-graph-enforcement` points to commit `3266f49`, which is the implementer's actual code, while the parent branch records the same pointer, confirming they're aligned. The submodule's `main` is at `082d38c`, a different commit.
> 
> But there's a puzzle: the parent's `main` and my review branch both record `03265b0` as the submodule pointer, yet that commit doesn't exist in the submodule's object store. I'm trying to figure out whether `03265b0` is actually a parent repo commit (like the "plan: bump beehive pointer" commit) that got confused with a submodule commit, or if it's a submodule commit that exists elsewhere in the object hierarchy.
> 
> Let me trace back through the parent main's log to see when `03265b0` was introduced and understand whether it's a stale value or a commit I haven't fetched yet.

Now I see a critical discrepancy. The submodule implementer commit is `3266f49`, but parent `main` records pointer `03265b0` which does **not exist** in the submodule object store. Let me trace how `03265b0` got onto main.

**🔧 bash** `echo "===main log touching submodule pointer==="; git log --oneline -8 main -- submodules/beehive/repo; echo "===show the commit that set 03265b0 on main==="; git log --oneline -5 main | cat; echo "=== when did 03265b0 appear? ==="; git log -p --oneline main -- submodules/beehive/repo 2>&1 | grep -n "Subproject commit" | head`

```
===main log touching submodule pointer===
11fe7df plan: links-graph-enforcement -> NEEDS-REVIEW; bump beehive pointer
8b9f405 frontend: edit ROI beehive
===show the commit that set 03265b0 on main===
d9d5a82 session: bee-links-graph-enforcement-1782781988
5cab6ba session: bee-links-graph-enforcement-1782781988
db5cc34 session: bee-links-graph-enforcement-1782781988
57d583e session: bee-links-graph-enforcement-1782781988
44f7737 session: bee-links-graph-enforcement-1782781988
=== when did 03265b0 appear? ===
7:-Subproject commit 082d38cbd1f52f07b154ebe82b57c2899afe7207
8:+Subproject commit 03265b0db625ea2086a1d8c20948cc014d3ee980
16:+Subproject commit 082d38cbd1f52f07b154ebe82b57c2899afe7207

```


## assistant

> 💭 Looking at the commit history, the main branch bumped the submodule pointer to `03265b0` with the NEEDS-REVIEW flip, but when I check the actual submodule object store on the bee-links-graph-enforcement branch, that commit `03265b0` doesn't exist there—only `3266f49` is present. This mismatch is the core issue I need to investigate.
> 
> I'm considering three possibilities: either `03265b0` is a newer commit that was pushed to origin but not fetched locally, or it's the implementer's actual commit and `3266f49` is something else, or they're the same tree but different commits due to rebasing. The protocol requires that the bumped commit be pushed to origin, so I should fetch from the submodule's origin to verify whether `03265b0` actually exists there. The key question for review is whether the recorded pointer points to a valid, pushed commit containing the implementer's work, or if it's a dangling reference that was never properly created.

On main, commit `11fe7df` bumped the pointer `082d38c -> 03265b0`. But the local submodule only has `3266f49`. I must determine whether `03265b0` exists anywhere (pushed to origin) or is a dangling pointer. Let me check reflogs and attempt a fetch from origin.

**🔧 bash** `SMG="/home/spencer/git-repos/spencerharmon/infra-beehive/.git/modules/submodules/beehive/repo"; echo "===search all refs/reflog for 03265b0==="; git --git-dir="$SMG" reflog 2>&1 | grep -i 03265b0 | head; git --git-dir="$SMG" for-each-ref 2>&1 | grep -i 03265b0 | head; echo "(none above means not referenced)"; echo "===remote config==="; git --git-dir="$SMG" remote -v 2>&1; echo "===try fetch origin (may fail offline)==="; timeout 25 git --git-dir="$SMG" fetch origin 2>&1 | head -20`

```
===search all refs/reflog for 03265b0===
(none above means not referenced)
===remote config===
origin	git@github.com:spencerharmon/beehive.git (fetch)
origin	git@github.com:spencerharmon/beehive.git (push)
===try fetch origin (may fail offline)===
From github.com:spencerharmon/beehive
 * [new branch]      bee-links-graph-enforcement -> origin/bee-links-graph-enforcement

```


## assistant

> 💭 The fetch pulled in `origin/bee-links-graph-enforcement`, so now I'm checking whether commit 03265b0 exists and what that remote branch is currently pointing to.

Fetch succeeded and brought in `origin/bee-links-graph-enforcement`. Let me check if `03265b0` now resolves and what origin points to.

**🔧 bash** `SMG="/home/spencer/git-repos/spencerharmon/infra-beehive/.git/modules/submodules/beehive/repo"; echo "===03265b0 now?==="; git --git-dir="$SMG" cat-file -t 03265b0 2>&1; echo "===origin/bee-links tip==="; git --git-dir="$SMG" log --oneline -3 origin/bee-links-graph-enforcement 2>&1; echo "===origin/main tip==="; git --git-dir="$SMG" log --oneline -1 origin/main 2>&1; echo "===is 3266f49 == 03265b0 tree?==="; git --git-dir="$SMG" rev-parse 3266f49^{tree} 2>&1`

```
===03265b0 now?===
commit
===origin/bee-links tip===
03265b0 links-graph-enforcement: cross-submodule dep gating + wait-cycle guard
082d38c fix: init writes full embedded AGENTS.md protocol, not placeholder
d5e2095 fix: build static binaries (CGO_ENABLED=0)
===origin/main tip===
082d38c fix: init writes full embedded AGENTS.md protocol, not placeholder
===is 3266f49 == 03265b0 tree?===
e329ec608cc29086794932acabf62e73afeda641

```


## assistant

> 💭 So the remote branch `origin/bee-links-graph-enforcement` is at commit `03265b0` with the message about cross-submodule dependency gating and wait-cycle guards, and its parent is the submodule's main tip at `082d38c`. Meanwhile, the local submodule branch `bee-links-graph-enforcement` was tracking a different commit `3266f49` that has a similar feature message about enforcing cross-submodule dependency gating.
> 
> Both commits share the same parent `082d38c`, but `03265b0` is the authoritative pushed version that main's pointer records. Now I need to examine the actual code changes in `03265b0` compared to its parent to understand what was implemented.

`03265b0` is the real pushed implementer commit on `origin/bee-links-graph-enforcement` (parent `082d38c` = submodule main tip). The pointer is valid and pushed. The local `3266f49` was an earlier divergent attempt. Let me now review the authoritative code at `03265b0`.

**🔧 bash** `SMG="/home/spencer/git-repos/spencerharmon/infra-beehive/.git/modules/submodules/beehive/repo"; echo "===diff stat 082d38c..03265b0==="; git --git-dir="$SMG" diff --stat 082d38c..03265b0 2>&1; echo "===full commit msg==="; git --git-dir="$SMG" log -1 03265b0 2>&1`

```
===diff stat 082d38c..03265b0===
 cmd/beehive/cmd_lint.go        |  39 ++++++++++++
 cmd/beehive/main.go            |   1 +
 internal/config/hook.go        |  43 +++++++++----
 internal/config/hook_test.go   |  96 +++++++++++++++++++++++++++++
 internal/links/links.go        |  86 ++++++++++++++++++++++++++
 internal/links/links_test.go   |  37 +++++++++++
 internal/plan/plan_test.go     |  22 +++++++
 internal/plan/state.go         |   9 ++-
 internal/select/graph.go       | 135 ++++++++++++++++++++++++++++++++++++++++
 internal/select/select.go      |  45 ++++++++++++--
 internal/select/select_test.go | 137 +++++++++++++++++++++++++++++++++++++++++
 11 files changed, 633 insertions(+), 17 deletions(-)
===full commit msg===
commit 03265b0db625ea2086a1d8c20948cc014d3ee980
Author: spencer <the.spencer.harmon@gmail.com>
Date:   Mon Jun 29 17:56:28 2026 -0500

    links-graph-enforcement: cross-submodule dep gating + wait-cycle guard
    
    Wire internal/links into task selection and the commit path. Selection now loads
    a combined cross-submodule dependency graph (select owns it; plan stays
    links-free): a TODO task whose dep names a linked submodule's task
    (<submodule>:<taskid>) is held until that task is DONE, and a task on a wait
    cycle is excluded rather than deadlocked. A PLAN.md dep-tag commit that forms a
    cycle is rejected by `beehive lint` and the pre-commit guard.
    
    Beehive: links-graph-enforcement docs/bee-links-graph-enforcement-links-graph-enforcement.md

```


## assistant

> 💭 Now I'm examining the key files that were modified—looking at how cycles are detected in the links module, how the new combined graph works in the select package, and checking the graphGate implementation along with the deferred dependency handling in the state module, plus the new linting command and the pre-commit hook guard.

The diff stat matches the change doc and the commit carries the `Beehive:` stamp. Let me review the actual implementation code in detail.

**🔧 bash** `SMG="/home/spencer/git-repos/spencerharmon/infra-beehive/.git/modules/submodules/beehive/repo"; git --git-dir="$SMG" show 03265b0 -- internal/links/links.go internal/plan/state.go internal/select/graph.go internal/select/select.go 2>&1`

```
commit 03265b0db625ea2086a1d8c20948cc014d3ee980
Author: spencer <the.spencer.harmon@gmail.com>
Date:   Mon Jun 29 17:56:28 2026 -0500

    links-graph-enforcement: cross-submodule dep gating + wait-cycle guard
    
    Wire internal/links into task selection and the commit path. Selection now loads
    a combined cross-submodule dependency graph (select owns it; plan stays
    links-free): a TODO task whose dep names a linked submodule's task
    (<submodule>:<taskid>) is held until that task is DONE, and a task on a wait
    cycle is excluded rather than deadlocked. A PLAN.md dep-tag commit that forms a
    cycle is rejected by `beehive lint` and the pre-commit guard.
    
    Beehive: links-graph-enforcement docs/bee-links-graph-enforcement-links-graph-enforcement.md

diff --git a/internal/links/links.go b/internal/links/links.go
index a5b0352..dc743f9 100644
--- a/internal/links/links.go
+++ b/internal/links/links.go
@@ -101,6 +101,92 @@ func (l *Links) AddDep(from, to string) error {
 // HasCycle reports whether the dependency graph contains a wait cycle.
 func (l *Links) HasCycle() bool { return cycle(l.Deps) != nil }
 
+// Cycle returns a node sequence forming a cycle in edges, or nil if the graph is
+// acyclic. Exported so callers that assemble edges from outside a Links value
+// (e.g. the selector's combined cross-submodule graph) can detect and report a
+// wait cycle without duplicating the traversal.
+func Cycle(edges []Edge) []string { return cycle(edges) }
+
+// CyclicNodes returns the set of nodes that lie on a dependency cycle: every node
+// in a strongly connected component of size > 1, plus any self-loop. A node that
+// merely depends (transitively) on a cycle is not included — only nodes that can
+// never settle. Deterministic (Tarjan SCC over sorted adjacency). The selector
+// uses this to exclude unschedulable tasks from candidate selection.
+func CyclicNodes(edges []Edge) map[string]bool {
+	adj := map[string][]string{}
+	self := map[string]bool{}
+	nodeset := map[string]bool{}
+	for _, e := range edges {
+		adj[e.From] = append(adj[e.From], e.To)
+		nodeset[e.From] = true
+		nodeset[e.To] = true
+		if e.From == e.To {
+			self[e.From] = true
+		}
+	}
+	nodes := make([]string, 0, len(nodeset))
+	for n := range nodeset {
+		nodes = append(nodes, n)
+	}
+	sort.Strings(nodes)
+	for _, n := range nodes {
+		sort.Strings(adj[n])
+	}
+
+	idx := map[string]int{}
+	low := map[string]int{}
+	onStack := map[string]bool{}
+	var stack []string
+	counter := 0
+	out := map[string]bool{}
+	var strongconnect func(string)
+	strongconnect = func(v string) {
+		idx[v] = counter
+		low[v] = counter
+		counter++
+		stack = append(stack, v)
+		onStack[v] = true
+		for _, w := range adj[v] {
+			if _, seen := idx[w]; !seen {
+				strongconnect(w)
+				if low[w] < low[v] {
+					low[v] = low[w]
+				}
+			} else if onStack[w] {
+				if idx[w] < low[v] {
+					low[v] = idx[w]
+				}
+			}
+		}
+		if low[v] != idx[v] {
+			return
+		}
+		var comp []string
+		for {
+			w := stack[len(stack)-1]
+			stack = stack[:len(stack)-1]
+			onStack[w] = false
+			comp = append(comp, w)
+			if w == v {
+				break
+			}
+		}
+		if len(comp) > 1 {
+			for _, w := range comp {
+				out[w] = true
+			}
+		} else if self[comp[0]] {
+			out[comp[0]] = true
+		}
+	}
+	for _, n := range nodes {
+		if _, seen := idx[n]; !seen {
+			strongconnect(n)
+		}
+	}
+	return out
+}
+
 // cycle returns a node sequence forming a cycle, or nil if acyclic (DFS).
 func cycle(edges []Edge) []string {
 	adj := map[string][]string{}
diff --git a/internal/plan/state.go b/internal/plan/state.go
index 2853851..3ba693b 100644
--- a/internal/plan/state.go
+++ b/internal/plan/state.go
@@ -2,6 +2,7 @@ package plan
 
 import (
 	"fmt"
+	"strings"
 	"time"
 )
 
@@ -68,12 +69,18 @@ func (t *Task) Reject(limit int, now time.Time) error {
 }
 
 // Selectable reports whether a task can be auto-selected: not terminal, not
-// NEEDS-HUMAN, and all deps DONE in p.
+// NEEDS-HUMAN, and all LOCAL deps DONE in p. A dep id containing ":" names a
+// task in another submodule (<submodule>:<taskid>); the plan layer stays
+// links-free and defers those to the selector, which owns the combined
+// cross-submodule graph (link authorization + DONE status + cycle exclusion).
 func (p *Plan) Selectable(t *Task) bool {
 	if t.Status == StatusDone || t.Status == StatusHuman {
 		return false
 	}
 	for _, d := range t.Deps {
+		if strings.Contains(d, ":") {
+			continue // cross-submodule; resolved by the selector, not here
+		}
 		dep := p.Task(d)
 		if dep == nil || dep.Status != StatusDone {
 			return false
diff --git a/internal/select/graph.go b/internal/select/graph.go
new file mode 100644
index 0000000..f5e57e2
--- /dev/null
+++ b/internal/select/graph.go
@@ -0,0 +1,135 @@
+package selectt
+
+import (
+	"os"
+	"path/filepath"
+	"strings"
+
+	"github.com/spencerharmon/beehive/internal/links"
+	"github.com/spencerharmon/beehive/internal/plan"
+	"github.com/spencerharmon/beehive/internal/repo"
+)
+
+// Graph is the combined, deterministic cross-submodule dependency graph that the
+// selector owns (the plan layer stays links-free). It folds, across every
+// submodule:
+//
+//   - PLAN.md dependency tags, as edges between qualified node ids
+//     "<submodule>:<taskid>" (a bare dep id is local and qualified with its own
+//     submodule);
+//   - any task edges declared in SUBMODULE-LINKS.yaml (links.Deps); and
+//   - the submodule link adjacency (links.Submodules) that authorizes one
+//     submodule to depend on another.
+//
+// From this it answers two selection questions: is a candidate task on a wait
+// cycle (exclude it), and are its cross-submodule prerequisites satisfied (linked
+// and DONE)?
+type Graph struct {
+	Edges  []links.Edge               // From depends on To; node ids are qualified
+	Status map[string]plan.Status     // qualified task id -> status from PLAN.md
+	Linked map[string]map[string]bool // submodule -> set of linked submodule names
+	cyclic map[string]bool            // qualified ids lying on a cycle
+}
+
+// qualifyID returns the graph node id for a dep id d referenced from submodule
+// sm. An id already carrying a "<submodule>:" prefix is cross-submodule and used
+// verbatim; a bare id is local and qualified with sm.
+func qualifyID(sm, d string) string {
+	if strings.Contains(d, ":") {
+		return d
+	}
+	return sm + ":" + d
+}
+
+// splitID splits a qualified node id into its submodule and task parts. ok is
+// false for an unqualified (local) id.
+func splitID(id string) (submodule, task string, ok bool) {
+	i := strings.Index(id, ":")
+	if i < 0 {
+		return "", id, false
+	}
+	return id[:i], id[i+1:], true
+}
+
+// LoadEdges builds the combined graph by reading every submodule's
+// SUBMODULE-LINKS.yaml and PLAN.md. Missing, dormant, or unparsed-absent plans
+// contribute nothing; a present-but-malformed PLAN.md surfaces its parse error.
+func LoadEdges(rp *repo.Repo) (*Graph, error) {
+	subs, err := rp.Submodules()
+	if err != nil {
+		return nil, err
+	}
+	g := &Graph{
+		Status: map[string]plan.Status{},
+		Linked: map[string]map[string]bool{},
+	}
+	for _, sm := range subs {
+		l, err := links.Load(filepath.Join(sm.Path, repo.LinksFile))
+		if err != nil {
+			return nil, err
+		}
+		for _, name := range l.Submodules {
+			if name == sm.Name {
+				continue
+			}
+			if g.Linked[sm.Name] == nil {
+				g.Linked[sm.Name] = map[string]bool{}
+			}
+			g.Linked[sm.Name][name] = true
+		}
+		for _, e := range l.Deps {
+			g.Edges = append(g.Edges, links.Edge{
+				From: qualifyID(sm.Name, e.From),
+				To:   qualifyID(sm.Name, e.To),
+			})
+		}
+
+		b, err := os.ReadFile(sm.PlanPath())
+		if err != nil {
+			if os.IsNotExist(err) {
+				continue
+			}
+			return nil, err
+		}
+		pl, err := plan.Parse(string(b))
+		if err != nil {
+			return nil, err
+		}
+		for _, t := range pl.Tasks {
+			node := sm.Name + ":" + t.ID
+			g.Status[node] = t.Status
+			for _, d := range t.Deps {
+				g.Edges = append(g.Edges, links.Edge{
+					From: node,
+					To:   qualifyID(sm.Name, d),
+				})
+			}
+		}
+	}
+	g.cyclic = links.CyclicNodes(g.Edges)
+	return g, nil
+}
+
+// Validate returns a node sequence forming a wait cycle in the combined graph, or
+// nil when the graph is acyclic. Used by `beehive lint` and the pre-commit guard
+// to reject a PLAN.md dep-tag commit that forms a cross-submodule cycle.
+func (g *Graph) Validate() []string { return links.Cycle(g.Edges) }
+
+// InCycle reports whether the qualified node id lies on a wait cycle.
+func (g *Graph) InCycle(node string) bool { return g.cyclic[node] }
+
+// crossDepSatisfied reports whether a dependency d of a task in submodule sm is
+// satisfied as far as cross-submodule resolution is concerned. Local deps (no
+// "<submodule>:" prefix) are the plan layer's responsibility and always pass
+// here. A cross-submodule dep is satisfied only when sm declares a link to the
+// target submodule (authorization) and the target task is DONE.
+func (g *Graph) crossDepSatisfied(sm, d string) bool {
+	tsm, _, ok := splitID(d)
+	if !ok {
+		return true
+	}
+	if !g.Linked[sm][tsm] {
+		return false
+	}
+	return g.Status[d] == plan.StatusDone
+}
diff --git a/internal/select/select.go b/internal/select/select.go
index b433b13..f18a99f 100644
--- a/internal/select/select.go
+++ b/internal/select/select.go
@@ -2,7 +2,11 @@
 // yields a workable task: weighted-random over submodules, ROI-reconcile as
 // priority 0 (PLAN.md stamp vs ROI.md commit), bootstrap when PLAN absent, then
 // GC > arbitration > review > main by priority, dependency-gated, cycle-skipped,
-// NEEDS-HUMAN excluded. The package name avoids the "select" keyword.
+// NEEDS-HUMAN excluded. Dependency gating spans submodules: a TODO task whose dep
+// names a linked submodule's task ("<submodule>:<taskid>") is held until that
+// task is DONE, and a task on a wait cycle is excluded rather than deadlocked
+// (this package owns the combined cross-submodule graph; see graph.go). The
+// package name avoids the "select" keyword.
 package selectt
 
 import (
@@ -52,10 +56,14 @@ func (s *Selector) Select(ctx context.Context) (*Selection, error) {
 	if err != nil {
 		return nil, err
 	}
+	graph, err := LoadEdges(s.Repo)
+	if err != nil {
+		return nil, err
+	}
 	order := s.weightedOrder(subs)
 	now := time.Now().UTC()
 	for _, sm := range order {
-		sel, err := s.fromSubmodule(ctx, sm, now)
+		sel, err := s.fromSubmodule(ctx, sm, now, graph)
 		if err != nil {
 			return nil, err
 		}
@@ -66,7 +74,7 @@ func (s *Selector) Select(ctx context.Context) (*Selection, error) {
 	return nil, nil
 }
 
-func (s *Selector) fromSubmodule(ctx context.Context, sm repo.Submodule, now time.Time) (*Selection, error) {
+func (s *Selector) fromSubmodule(ctx context.Context, sm repo.Submodule, now time.Time, graph *Graph) (*Selection, error) {
 	if sm.Dormant() {
 		return nil, nil
 	}
@@ -91,7 +99,7 @@ func (s *Selector) fromSubmodule(ctx context.Context, sm repo.Submodule, now tim
 	if err != nil {
 		return nil, err
 	}
-	cands := pl.Candidates(now, s.TTL)
+	cands := graphGate(sm, pl.Candidates(now, s.TTL), graph)
 	if len(cands) == 0 {
 		return nil, nil
 	}
@@ -99,6 +107,35 @@ func (s *Selector) fromSubmodule(ctx context.Context, sm repo.Submodule, now tim
 	return &Selection{Kind: Work, Submodule: sm, Task: t}, nil
 }
 
+// graphGate filters main-tier (TODO) candidates through the combined
+// cross-submodule graph: a task on a wait cycle is excluded, and a task whose
+// cross-submodule prerequisite is unauthorized or not DONE is held. Recovery
+// tiers (GC stale / arbitration / review) pass through untouched — they exist to
+// unstick work, not to start it, so they are never dependency- or cycle-gated.
+func graphGate(sm repo.Submodule, cands []plan.Task, graph *Graph) []plan.Task {
+	out := make([]plan.Task, 0, len(cands))
+	for _, t := range cands {
+		if t.Status == plan.StatusTODO {
+			node := sm.Name + ":" + t.ID
+			if graph.InCycle(node) {
+				continue
+			}
+			blocked := false
+			for _, d := range t.Deps {
+				if !graph.crossDepSatisfied(sm.Name, d) {
+					blocked = true
+					break
+				}
+			}
+			if blocked {
+				continue
+			}
+		}
+		out = append(out, t)
+	}
+	return out
+}
+
 // reconcileRange returns "<stamp>..<roiHead>" when ROI.md drifted, else "".
 func (s *Selector) reconcileRange(ctx context.Context, sm repo.Submodule) (string, error) {
 	if _, err := os.Stat(sm.ROIPath()); err != nil {

```


## assistant

> 💭 Looking at the implementation in the links package, the exported `Cycle` function wraps the internal `cycle` logic, and `CyclicNodes` uses Tarjan's strongly connected components algorithm to identify nodes that are part of cycles—correctly handling both multi-node cycles and self-loops by flagging components with size greater than one.
> 
> In the plan state, `Selectable` now filters out dependencies containing ":" to defer cross-submodule resolution to the selector, while local dependencies continue to gate selection. The new graph module builds a comprehensive view by folding together links and plan dependencies across submodules, constructing status and linked maps, and computing cyclic nodes. The `Validate` method exposes cycles, `InCycle` checks membership, and `crossDepSatisfied` enforces that cross-submodule dependencies require both link authorization and a DONE status.
> 
> In the select module, the graph loads once and passes through to the submodule selection logic, where `graphGate` filters TODO candidates by excluding cyclic nodes and holding on unsatisfied cross-dependencies—though I'm noticing the gate only applies these checks when status is TODO, so I need to verify whether recovery tiers should bypass these constraints. I should verify the implementation against the acceptance criteria by running the test suite and checking the lint and hook code. Let me start by examining the command and hook implementations, then run the full test suite to confirm everything builds and passes.

Solid core. Let me review the lint command and the pre-commit hook guard, then the tests.

**🔧 bash** `SMG="/home/spencer/git-repos/spencerharmon/infra-beehive/.git/modules/submodules/beehive/repo"; git --git-dir="$SMG" show 03265b0 -- cmd/beehive/cmd_lint.go cmd/beehive/main.go internal/config/hook.go 2>&1`

```
commit 03265b0db625ea2086a1d8c20948cc014d3ee980
Author: spencer <the.spencer.harmon@gmail.com>
Date:   Mon Jun 29 17:56:28 2026 -0500

    links-graph-enforcement: cross-submodule dep gating + wait-cycle guard
    
    Wire internal/links into task selection and the commit path. Selection now loads
    a combined cross-submodule dependency graph (select owns it; plan stays
    links-free): a TODO task whose dep names a linked submodule's task
    (<submodule>:<taskid>) is held until that task is DONE, and a task on a wait
    cycle is excluded rather than deadlocked. A PLAN.md dep-tag commit that forms a
    cycle is rejected by `beehive lint` and the pre-commit guard.
    
    Beehive: links-graph-enforcement docs/bee-links-graph-enforcement-links-graph-enforcement.md

diff --git a/cmd/beehive/cmd_lint.go b/cmd/beehive/cmd_lint.go
new file mode 100644
index 0000000..dd09669
--- /dev/null
+++ b/cmd/beehive/cmd_lint.go
@@ -0,0 +1,39 @@
+package main
+
+import (
+	"fmt"
+	"strings"
+
+	"github.com/spencerharmon/beehive/internal/repo"
+	selectt "github.com/spencerharmon/beehive/internal/select"
+	"github.com/spf13/cobra"
+)
+
+// lintCmd validates the combined cross-submodule dependency graph. It powers the
+// pre-commit guard: a PLAN.md dep-tag commit that forms a wait cycle is rejected.
+func lintCmd() *cobra.Command {
+	return &cobra.Command{
+		Use:   "lint",
+		Short: "validate the combined cross-submodule dependency graph is acyclic",
+		Args:  cobra.NoArgs,
+		RunE: func(_ *cobra.Command, _ []string) error {
+			root, err := findRoot()
+			if err != nil {
+				return err
+			}
+			rp, err := repo.Open(root)
+			if err != nil {
+				return err
+			}
+			g, err := selectt.LoadEdges(rp)
+			if err != nil {
+				return err
+			}
+			if c := g.Validate(); c != nil {
+				return fmt.Errorf("dependency cycle: %s", strings.Join(c, " -> "))
+			}
+			fmt.Println("beehive: dependency graph OK")
+			return nil
+		},
+	}
+}
diff --git a/cmd/beehive/main.go b/cmd/beehive/main.go
index 449c33e..9022a8f 100644
--- a/cmd/beehive/main.go
+++ b/cmd/beehive/main.go
@@ -25,6 +25,7 @@ func main() {
 		worktreeCmd(),
 		honeybeeCmd(),
 		hookCmd(),
+		lintCmd(),
 	)
 	if err := root.Execute(); err != nil {
 		fmt.Fprintln(os.Stderr, "beehive:", err)
diff --git a/internal/config/hook.go b/internal/config/hook.go
index 4ad20ad..3deaed3 100644
--- a/internal/config/hook.go
+++ b/internal/config/hook.go
@@ -6,21 +6,40 @@ import (
 	"path/filepath"
 )
 
-// roiHook rejects commits that touch ROI.md when authored by a honeybee identity.
-// Honeybees export BEEHIVE_HONEYBEE=1; only the frontend (unset) may change ROI.md.
-const roiHook = `#!/usr/bin/env sh
-# beehive ROI-protect hook (installed by CLI). ROI.md is human-owned; honeybees
-# must never change it. Enforced here for local commits; a server pre-receive
-# mirrors this for pushes.
-[ "${BEEHIVE_HONEYBEE:-0}" = "1" ] || exit 0
-if git diff --cached --name-only | grep -E '(^|/)ROI\.md$' >/dev/null; then
-  echo "beehive: honeybee identity may not modify ROI.md" >&2
-  exit 1
+// preCommitHook is the beehive pre-commit guard. It enforces two repo invariants:
+//
+//  1. ROI.md is human-owned: a honeybee identity (BEEHIVE_HONEYBEE=1) may never
+//     change it. The frontend (env unset) is allowed. A server pre-receive
+//     mirrors this for pushes.
+//  2. PLAN.md dependency tags stay acyclic: when a commit touches any PLAN.md it
+//     runs `beehive lint`, which loads the combined cross-submodule graph and
+//     exits non-zero on a wait cycle. This rejects a honeybee (or human) dep-tag
+//     commit that would deadlock the swarm, mirroring links.AddDep's CLI-time
+//     cycle check for plan writes that never go through the CLI. The check is
+//     best-effort: if the beehive binary is not on PATH it is skipped (the lint
+//     still runs in CI and on the next CLI invocation).
+const preCommitHook = `#!/usr/bin/env sh
+# beehive pre-commit guard (installed by CLI). See internal/config/hook.go.
+if [ "${BEEHIVE_HONEYBEE:-0}" = "1" ]; then
+  if git diff --cached --name-only | grep -E '(^|/)ROI\.md$' >/dev/null; then
+    echo "beehive: honeybee identity may not modify ROI.md" >&2
+    exit 1
+  fi
+fi
+if git diff --cached --name-only | grep -E '(^|/)PLAN\.md$' >/dev/null; then
+  if command -v beehive >/dev/null 2>&1; then
+    if ! beehive lint; then
+      echo "beehive: PLAN.md dependency tags form a wait cycle (rejected)" >&2
+      exit 1
+    fi
+  fi
 fi
 exit 0
 `
 
-// InstallROIHook writes the ROI-protect pre-commit hook into the repo's .git dir.
+// InstallROIHook writes the beehive pre-commit guard into the repo's .git dir.
+// (Named for its original ROI-only role; it now also installs the dep-cycle
+// guard. See preCommitHook.)
 func InstallROIHook(repoRoot string) error {
 	dir := filepath.Join(repoRoot, ".git", "hooks")
 	if _, err := os.Stat(filepath.Join(repoRoot, ".git")); err != nil {
@@ -30,5 +49,5 @@ func InstallROIHook(repoRoot string) error {
 		return err
 	}
 	p := filepath.Join(dir, "pre-commit")
-	return os.WriteFile(p, []byte(roiHook), 0o755)
+	return os.WriteFile(p, []byte(preCommitHook), 0o755)
 }

```


## assistant

> 💭 The pre-commit hook now handles both ROI protection for honeybee and a best-effort PLAN.md cycle check via `beehive lint`, with the key improvement being that the dependency check now runs for both honeybee and frontend commits instead of early-exiting for non-honeybee changes.
> 
> There's a caveat worth noting: since the hook validates the working tree graph rather than the staged index, it's checking what's actually on disk, not what's being committed—the change doc flags this as a follow-up item, which seems reasonable for now.
> 
> Let me check the test files to see how this is being validated.

Now the tests. Let me view them, then actually run the full suite.

**🔧 bash** `SMG="/home/spencer/git-repos/spencerharmon/infra-beehive/.git/modules/submodules/beehive/repo"; git --git-dir="$SMG" show 03265b0 -- internal/links/links_test.go internal/plan/plan_test.go internal/select/select_test.go 2>&1`

```
commit 03265b0db625ea2086a1d8c20948cc014d3ee980
Author: spencer <the.spencer.harmon@gmail.com>
Date:   Mon Jun 29 17:56:28 2026 -0500

    links-graph-enforcement: cross-submodule dep gating + wait-cycle guard
    
    Wire internal/links into task selection and the commit path. Selection now loads
    a combined cross-submodule dependency graph (select owns it; plan stays
    links-free): a TODO task whose dep names a linked submodule's task
    (<submodule>:<taskid>) is held until that task is DONE, and a task on a wait
    cycle is excluded rather than deadlocked. A PLAN.md dep-tag commit that forms a
    cycle is rejected by `beehive lint` and the pre-commit guard.
    
    Beehive: links-graph-enforcement docs/bee-links-graph-enforcement-links-graph-enforcement.md

diff --git a/internal/links/links_test.go b/internal/links/links_test.go
index b1008a6..b4a4097 100644
--- a/internal/links/links_test.go
+++ b/internal/links/links_test.go
@@ -57,3 +57,40 @@ func TestRoundTrip(t *testing.T) {
 }
 
 func LinksName() string { return "SUBMODULE-LINKS.yaml" }
+
+func TestCycleExported(t *testing.T) {
+	if c := Cycle([]Edge{{From: "a", To: "b"}, {From: "b", To: "c"}, {From: "c", To: "a"}}); c == nil {
+		t.Fatal("Cycle: want a cycle, got nil")
+	}
+	if c := Cycle([]Edge{{From: "a", To: "b"}, {From: "b", To: "c"}}); c != nil {
+		t.Fatalf("Cycle: acyclic reported %v", c)
+	}
+}
+
+func TestCyclicNodes(t *testing.T) {
+	// a<->b<->c form a 3-cycle; d only depends on the cycle (not on it); e is free.
+	edges := []Edge{
+		{From: "a", To: "b"},
+		{From: "b", To: "c"},
+		{From: "c", To: "a"},
+		{From: "d", To: "a"},
+		{From: "e", To: "f"},
+	}
+	cn := CyclicNodes(edges)
+	for _, n := range []string{"a", "b", "c"} {
+		if !cn[n] {
+			t.Fatalf("node %s must be on a cycle: %v", n, cn)
+		}
+	}
+	for _, n := range []string{"d", "e", "f"} {
+		if cn[n] {
+			t.Fatalf("node %s must not be on a cycle: %v", n, cn)
+		}
+	}
+	if len(CyclicNodes([]Edge{{From: "a", To: "b"}, {From: "b", To: "c"}})) != 0 {
+		t.Fatal("acyclic graph reported cyclic nodes")
+	}
+	if !CyclicNodes([]Edge{{From: "x", To: "x"}})["x"] {
+		t.Fatal("self-loop x must be cyclic")
+	}
+}
diff --git a/internal/plan/plan_test.go b/internal/plan/plan_test.go
index 39c3ed2..6990e6f 100644
--- a/internal/plan/plan_test.go
+++ b/internal/plan/plan_test.go
@@ -118,6 +118,28 @@ func TestSelectable(t *testing.T) {
 	}
 }
 
+func TestSelectableDefersCrossSubmoduleDeps(t *testing.T) {
+	// A "<submodule>:<taskid>" dep is cross-submodule: the plan layer stays
+	// links-free and leaves resolution to the selector, so it must not block
+	// local selectability even though no local task by that id exists.
+	p, err := Parse("## t1 [TODO] <!-- attempts=0 deps=other:dep -->\nbody\n")
+	if err != nil {
+		t.Fatal(err)
+	}
+	if !p.Selectable(p.Task("t1")) {
+		t.Fatal("cross-submodule dep must be deferred to the selector, not block locally")
+	}
+	// A local unmet dep alongside the cross-submodule one still blocks.
+	p2, _ := Parse("## a [TODO] <!-- attempts=0 deps=b,other:dep -->\n## b [TODO] <!-- attempts=0 deps= -->\n")
+	if p2.Selectable(p2.Task("a")) {
+		t.Fatal("local unmet dep b must still block")
+	}
+	p2.Task("b").Status = StatusDone
+	if !p2.Selectable(p2.Task("a")) {
+		t.Fatal("local dep done -> selectable (cross-submodule dep deferred)")
+	}
+}
+
 func TestGolden(t *testing.T) {
 	now := time.Date(2026, 6, 29, 10, 0, 0, 0, time.UTC)
 	p, _ := Parse(sample)
diff --git a/internal/select/select_test.go b/internal/select/select_test.go
index 0e73206..6935606 100644
--- a/internal/select/select_test.go
+++ b/internal/select/select_test.go
@@ -84,3 +84,140 @@ func TestReconcilePriority0(t *testing.T) {
 		t.Fatalf("want reconcile, got %+v", s)
 	}
 }
+
+// stampAll seeds each submodule's PLAN.md with the ROI commit sha so the selector
+// sees no reconcile drift and proceeds to Work selection. Call after committing.
+func stampAll(t *testing.T, g *git.Repo, root string, names ...string) {
+	t.Helper()
+	ctx := context.Background()
+	for _, n := range names {
+		head, err := g.LastCommit(ctx, "submodules/"+n+"/ROI.md")
+		if err != nil || head == "" {
+			t.Fatalf("ROI head for %s: %q %v", n, head, err)
+		}
+		p := filepath.Join(root, "submodules", n, "PLAN.md")
+		b, err := os.ReadFile(p)
+		if err != nil {
+			t.Fatal(err)
+		}
+		if err := os.WriteFile(p, []byte("<!-- Beehive-ROI: "+head+" -->\n"+string(b)), 0o644); err != nil {
+			t.Fatal(err)
+		}
+	}
+	if err := g.Commit(ctx, "stamp"); err != nil {
+		t.Fatal(err)
+	}
+}
+
+const linkAB = "submodules:\n  - a\n  - b\n"
+
+func TestLinkedDepGatesSelection(t *testing.T) {
+	_, g, root := hive(t)
+	// a:A1 depends on linked b:B1, which is not DONE -> A1 is held. Only b:B1 is
+	// selectable, so it must be the pick regardless of submodule order.
+	sub(root, "a", map[string]string{
+		"ROI.md":               "x",
+		"SUBMODULE-LINKS.yaml": linkAB,
+		"PLAN.md":              "## A1 [TODO] <!-- attempts=0 deps=b:B1 -->\ngo\n",
+	})
+	sub(root, "b", map[string]string{
+		"ROI.md":               "x",
+		"SUBMODULE-LINKS.yaml": linkAB,
+		"PLAN.md":              "## B1 [TODO] <!-- attempts=0 deps= -->\ngo\n",
+	})
+	g.Commit(context.Background(), "seed")
+	stampAll(t, g, root, "a", "b")
+	s, err := sel(root, g).Select(context.Background())
+	if err != nil {
+		t.Fatal(err)
+	}
+	if s == nil || s.Submodule.Name != "b" || s.Task.ID != "B1" {
+		t.Fatalf("cross-submodule dep should gate A1; want B1 in b, got %+v", s)
+	}
+}
+
+func TestLinkedDepDoneUnblocks(t *testing.T) {
+	_, g, root := hive(t)
+	// b:B1 is DONE, so a:A1's cross-submodule prerequisite is satisfied. b has no
+	// other selectable task, so A1 in a is the only pick.
+	sub(root, "a", map[string]string{
+		"ROI.md":               "x",
+		"SUBMODULE-LINKS.yaml": linkAB,
+		"PLAN.md":              "## A1 [TODO] <!-- attempts=0 deps=b:B1 -->\ngo\n",
+	})
+	sub(root, "b", map[string]string{
+		"ROI.md":               "x",
+		"SUBMODULE-LINKS.yaml": linkAB,
+		"PLAN.md":              "## B1 [DONE] <!-- attempts=0 deps= -->\ngo\n",
+	})
+	g.Commit(context.Background(), "seed")
+	stampAll(t, g, root, "a", "b")
+	s, err := sel(root, g).Select(context.Background())
+	if err != nil {
+		t.Fatal(err)
+	}
+	if s == nil || s.Submodule.Name != "a" || s.Task.ID != "A1" {
+		t.Fatalf("DONE dep should unblock A1; got %+v", s)
+	}
+}
+
+func TestCrossDepRequiresLink(t *testing.T) {
+	_, g, root := hive(t)
+	// a:A1 depends on b:B1 but a declares NO link to b. Even though B1 is DONE the
+	// dependency is unauthorized, so A1 is held; b has no other task -> nothing
+	// selectable.
+	sub(root, "a", map[string]string{
+		"ROI.md":  "x",
+		"PLAN.md": "## A1 [TODO] <!-- attempts=0 deps=b:B1 -->\ngo\n",
+	})
+	sub(root, "b", map[string]string{
+		"ROI.md":  "x",
+		"PLAN.md": "## B1 [DONE] <!-- attempts=0 deps= -->\ngo\n",
+	})
+	g.Commit(context.Background(), "seed")
+	stampAll(t, g, root, "a", "b")
+	s, err := sel(root, g).Select(context.Background())
+	if err != nil {
+		t.Fatal(err)
+	}
+	if s != nil {
+		t.Fatalf("unlinked cross-submodule dep must gate selection, got %+v", s)
+	}
+}
+
+func TestCyclicTasksNotSelected(t *testing.T) {
+	_, g, root := hive(t)
+	// a:A1 -> b:B1 -> a:A1 forms a cross-submodule wait cycle. Both tasks are on
+	// the cycle and must be excluded from selection.
+	sub(root, "a", map[string]string{
+		"ROI.md":               "x",
+		"SUBMODULE-LINKS.yaml": linkAB,
+		"PLAN.md":              "## A1 [TODO] <!-- attempts=0 deps=b:B1 -->\ngo\n",
+	})
+	sub(root, "b", map[string]string{
+		"ROI.md":               "x",
+		"SUBMODULE-LINKS.yaml": linkAB,
+		"PLAN.md":              "## B1 [TODO] <!-- attempts=0 deps=a:A1 -->\ngo\n",
+	})
+	g.Commit(context.Background(), "seed")
+	stampAll(t, g, root, "a", "b")
+
+	rp, _ := repo.Open(root)
+	graph, err := LoadEdges(rp)
+	if err != nil {
+		t.Fatal(err)
+	}
+	if graph.Validate() == nil {
+		t.Fatal("Validate: want a cycle in a:A1 <-> b:B1")
+	}
+	if !graph.InCycle("a:A1") || !graph.InCycle("b:B1") {
+		t.Fatalf("both cycle nodes must be flagged: %+v", graph.cyclic)
+	}
+	s, err := sel(root, g).Select(context.Background())
+	if err != nil {
+		t.Fatal(err)
+	}
+	if s != nil {
+		t.Fatalf("cyclic tasks must not be selected, got %+v", s)
+	}
+}

```


## assistant

> 💭 The tests are thorough, covering real hive setups with multiple submodules, actual git commits, and key behaviors like selection gating and cycle detection. Now I'm moving on to examine the config hook test in hook_test.go before running the full test suite.

Tests look comprehensive. Let me see the config hook test (the e2e commit-boundary one), then run the suite.

**🔧 bash** `SMG="/home/spencer/git-repos/spencerharmon/infra-beehive/.git/modules/submodules/beehive/repo"; git --git-dir="$SMG" show 03265b0 -- internal/config/hook_test.go 2>&1`

```
commit 03265b0db625ea2086a1d8c20948cc014d3ee980
Author: spencer <the.spencer.harmon@gmail.com>
Date:   Mon Jun 29 17:56:28 2026 -0500

    links-graph-enforcement: cross-submodule dep gating + wait-cycle guard
    
    Wire internal/links into task selection and the commit path. Selection now loads
    a combined cross-submodule dependency graph (select owns it; plan stays
    links-free): a TODO task whose dep names a linked submodule's task
    (<submodule>:<taskid>) is held until that task is DONE, and a task on a wait
    cycle is excluded rather than deadlocked. A PLAN.md dep-tag commit that forms a
    cycle is rejected by `beehive lint` and the pre-commit guard.
    
    Beehive: links-graph-enforcement docs/bee-links-graph-enforcement-links-graph-enforcement.md

diff --git a/internal/config/hook_test.go b/internal/config/hook_test.go
index d3f9104..c7741c4 100644
--- a/internal/config/hook_test.go
+++ b/internal/config/hook_test.go
@@ -2,7 +2,9 @@ package config
 
 import (
 	"os"
+	"os/exec"
 	"path/filepath"
+	"strings"
 	"testing"
 )
 
@@ -26,3 +28,97 @@ func TestInstallROIHook(t *testing.T) {
 		t.Fatal("hook not executable")
 	}
 }
+
+func TestPreCommitHookGuards(t *testing.T) {
+	root := t.TempDir()
+	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
+		t.Fatal(err)
+	}
+	if err := InstallROIHook(root); err != nil {
+		t.Fatal(err)
+	}
+	b, err := os.ReadFile(filepath.Join(root, ".git", "hooks", "pre-commit"))
+	if err != nil {
+		t.Fatal(err)
+	}
+	s := string(b)
+	for _, want := range []string{"ROI.md", "PLAN.md", "beehive lint", "BEEHIVE_HONEYBEE"} {
+		if !strings.Contains(s, want) {
+			t.Fatalf("pre-commit hook missing %q:\n%s", want, s)
+		}
+	}
+}
+
+// TestPreCommitDepCycleGuardE2E drives the installed hook through a real commit,
+// stubbing the beehive binary on PATH to stand in for `beehive lint`. It proves
+// the wiring: a PLAN.md commit is rejected when lint fails (a cycle) and allowed
+// when lint passes; the real cycle detection that lint runs is covered in
+// internal/select and internal/links.
+func TestPreCommitDepCycleGuardE2E(t *testing.T) {
+	if _, err := exec.LookPath("git"); err != nil {
+		t.Skip("git not available")
+	}
+	root := t.TempDir()
+	gitRun := func(env []string, args ...string) (string, error) {
+		cmd := exec.Command("git", args...)
+		cmd.Dir = root
+		cmd.Env = append(os.Environ(), env...)
+		out, err := cmd.CombinedOutput()
+		return string(out), err
+	}
+	for _, a := range [][]string{
+		{"init", "-q", "-b", "main"},
+		{"config", "user.email", "t@t"},
+		{"config", "user.name", "t"},
+	} {
+		if out, err := gitRun(nil, a...); err != nil {
+			t.Fatalf("git %v: %v\n%s", a, err, out)
+		}
+	}
+	if err := InstallROIHook(root); err != nil {
+		t.Fatal(err)
+	}
+
+	// Stub beehive on PATH; mode "1" => lint fails (cycle), "0" => lint passes.
+	bin := t.TempDir()
+	writeStub := func(exit string) {
+		sh := "#!/bin/sh\nexit " + exit + "\n"
+		if err := os.WriteFile(filepath.Join(bin, "beehive"), []byte(sh), 0o755); err != nil {
+			t.Fatal(err)
+		}
+	}
+	pathEnv := "PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")
+
+	if err := os.WriteFile(filepath.Join(root, "PLAN.md"), []byte("## a [TODO] <!-- attempts=0 deps=b -->\n"), 0o644); err != nil {
+		t.Fatal(err)
+	}
+	if out, err := gitRun(nil, "add", "PLAN.md"); err != nil {
+		t.Fatalf("add: %v\n%s", err, out)
+	}
+
+	// lint fails -> commit rejected, nothing recorded.
+	writeStub("1")
+	if out, err := gitRun([]string{pathEnv}, "commit", "-m", "cycle"); err == nil {
+		t.Fatalf("commit must be rejected when lint fails:\n%s", out)
+	}
+	if _, err := gitRun(nil, "rev-parse", "--verify", "HEAD"); err == nil {
+		t.Fatal("a commit landed despite lint failure")
+	}
+
+	// lint passes -> commit succeeds.
+	writeStub("0")
+	if out, err := gitRun([]string{pathEnv}, "commit", "-m", "ok"); err != nil {
+		t.Fatalf("commit must succeed when lint passes: %v\n%s", err, out)
+	}
+
+	// ROI protection still holds for a honeybee identity.
+	if err := os.WriteFile(filepath.Join(root, "ROI.md"), []byte("x\n"), 0o644); err != nil {
+		t.Fatal(err)
+	}
+	if out, err := gitRun(nil, "add", "ROI.md"); err != nil {
+		t.Fatalf("add ROI: %v\n%s", err, out)
+	}
+	if out, err := gitRun([]string{pathEnv, "BEEHIVE_HONEYBEE=1"}, "commit", "-m", "roi"); err == nil {
+		t.Fatalf("honeybee ROI.md commit must be rejected:\n%s", out)
+	}
+}

```


## assistant

> 💭 The tests look solid with no artificial shortcuts except for the intentional lint stub in the e2e wiring test, which makes sense since the real cycle detection gets tested elsewhere. Now I need to actually verify this works by checking out the submodule code and running the full test suite, linting, and a static build to make sure everything compiles correctly. I'll need to be careful not to write into the shared submodule checkout, so I'll set up a temporary worktree or fresh clone instead.
> 
> Let me first verify Go is available, then create an isolated temp checkout of that commit to run the tests safely.

The tests are real and well-targeted. Now the critical step: actually build and run the suite. Let me check out `03265b0` into an isolated temp dir (never touching the shared checkout) and run tests/vet/build.

**🔧 bash** `go version 2>&1; echo "---"; SMG="/home/spencer/git-repos/spencerharmon/infra-beehive/.git/modules/submodules/beehive/repo"; rm -rf /tmp/opencode/bee-review && git clone -q --no-local "$SMG" /tmp/opencode/bee-review 2>&1 && git -C /tmp/opencode/bee-review fetch -q "$SMG" 03265b0 2>&1; git -C /tmp/opencode/bee-review checkout -q 03265b0 2>&1 && echo "checked out:" && git -C /tmp/opencode/bee-review log --oneline -1 2>&1`

```
go version go1.26.3-X:nodwarf5 linux/amd64
---
error: copy-fd: write returned: Disk quota exceeded
fatal: cannot copy '/usr/share/git-core/templates/hooks/applypatch-msg.sample' to '/tmp/opencode/bee-review/.git/hooks/applypatch-msg.sample': Disk quota exceeded
fatal: cannot change to '/tmp/opencode/bee-review': No such file or directory

```


## assistant

> 💭 I'm hitting disk space constraints in /tmp/opencode, so I need to explore alternatives that don't require copying the full repository. Using git worktree with the submodule's git directory could work since it leverages hardlinks and shared objects, or I could try git archive to extract just the tree. Even with worktree, though, the checkout and Go build cache will still consume disk space, so I'm running into the same limitation.

**🔧 bash** `df -h /tmp /home 2>&1; echo "---quota---"; quota -s 2>/dev/null | head; echo "---du tmp/opencode---"; du -sh /tmp/opencode 2>/dev/null; rm -rf /tmp/opencode/bee-review; echo "---home worktrees space---"; du -sh /home/spencer/git-repos/spencerharmon/infra-beehive/.git 2>/dev/null`

```
Filesystem             Size  Used Avail Use% Mounted on
tmpfs                   16G   13G  3.1G  81% /tmp
/dev/mapper/cryptroot  477G  308G  170G  65% /
---quota---
---du tmp/opencode---
0	/tmp/opencode
---home worktrees space---
14M	/home/spencer/git-repos/spencerharmon/infra-beehive/.git

```


## assistant

> 💭 The /tmp filesystem has plenty of space, but the user's disk quota is being exceeded. Rather than cloning the entire repository which copies the pack file, I should use a git worktree approach that shares the object database, avoiding the quota issue altogether. I should figure out the exact quota constraints first—whether it's a global user limit or filesystem-specific—before deciding whether to use a worktree with a separate cache directory or build elsewhere entirely.

Disk quota is constrained. Let me check the actual quota limits and find a workable build location.

**🔧 bash** `echo "=== repquota / quota ==="; quota 2>&1 | head; echo "=== id ==="; id -u; echo "=== go env cache ==="; go env GOCACHE GOPATH GOMODCACHE 2>&1; echo "=== du of go caches ==="; du -sh "$(go env GOCACHE)" "$(go env GOMODCACHE)" 2>/dev/null; echo "=== home avail for user ==="; df -h "$HOME" 2>&1`

```
=== repquota / quota ===
/usr/bin/bash: line 1: quota: command not found
=== id ===
1000
=== go env cache ===
/home/spencer/.cache/go-build
/home/spencer/go
/home/spencer/go/pkg/mod
=== du of go caches ===
296M	/home/spencer/.cache/go-build
1.3G	/home/spencer/go/pkg/mod
=== home avail for user ===
Filesystem             Size  Used Avail Use% Mounted on
/dev/mapper/cryptroot  477G  308G  170G  65% /

```


## assistant

> 💭 The Go caches are already on the root filesystem with plenty of space available, so the disk quota issue is specific to /tmp. I should build in /home instead, creating a separate worktree or checkout outside the main beehive repo to keep things clean—something like a dedicated cache directory on the root fs.
> 
> Actually, the cleanest approach is to use `git archive` to export the tree at a specific commit directly to /home without any git metadata or worktree registration. This extracts just the files I need to run tests, avoiding any modifications to the shared submodule's git directory. Since the tests don't require the outer .git (except one e2e test that creates its own temp repo), this should work fine. Let me create a directory on /home, extract the archive there, and run the tests.

Go caches live on the root fs (plenty of space); only `/tmp` has the tight quota. Let me export the tree via `git archive` (no object copy, no worktree registration) to a build dir on the root fs and run the suite.

**🔧 bash** `SMG="/home/spencer/git-repos/spencerharmon/infra-beehive/.git/modules/submodules/beehive/repo"; BD=/home/spencer/.cache/bee-review; rm -rf "$BD"; mkdir -p "$BD"; git --git-dir="$SMG" archive 03265b0 | tar -x -C "$BD" && echo "extracted" && ls "$BD" && echo "--- go.mod ---" && head -3 "$BD/go.mod"`

```
extracted
cmd
CONTRIBUTING.md
docs
go.mod
go.sum
IMPLEMENTATION.org
internal
packaging
plan.org
prompts
scripts
--- go.mod ---
module github.com/spencerharmon/beehive

go 1.22

```


## assistant

Tree exported. Now let me run vet, the full test suite, and a static build.

**🔧 bash** `cd /home/spencer/.cache/bee-review && echo "=== go vet ./... ===" && go vet ./... 2>&1 | tail -30; echo "EXIT_VET=$?"`

```
=== go vet ./... ===
github.com/spencerharmon/beehive/prompts: write /tmp/go-build363245003/b080/importcfg: disk quota exceeded
github.com/spencerharmon/beehive/internal/git: write /tmp/go-build363245003/b073/importcfg: disk quota exceeded
github.com/spencerharmon/beehive/internal/links: write /tmp/go-build363245003/b076/importcfg: disk quota exceeded
github.com/spencerharmon/beehive/internal/config: write /tmp/go-build363245003/b063/importcfg: disk quota exceeded
github.com/spencerharmon/beehive/internal/git: write /tmp/go-build363245003/b221/vet.cfg: disk quota exceeded
github.com/spencerharmon/beehive/internal/config: write /tmp/go-build363245003/b220/vet.cfg: disk quota exceeded
github.com/spencerharmon/beehive/internal/links: write /tmp/go-build363245003/b222/vet.cfg: disk quota exceeded
# runtime/cgo
cgo: write /tmp/cgo-gcc-input-650500324.c: disk quota exceeded
EXIT_VET=0

```


## assistant

> 💭 The Go build is using /tmp for its temporary directory, which has limited space, so I need to redirect GOTMPDIR and TMPDIR to a location with more room like /home/spencer/.cache/bee-tmp. I'm also disabling cgo by setting CGO_ENABLED=0 since we want a static build, and I'll apply this setting to the vet and test commands as well.

The Go toolchain writes temp files to `/tmp` (quota-limited). Let me redirect `TMPDIR`/`GOTMPDIR` to the root fs and re-run.

**🔧 bash** `cd /home/spencer/.cache/bee-review && mkdir -p /home/spencer/.cache/bee-tmp && export TMPDIR=/home/spencer/.cache/bee-tmp GOTMPDIR=/home/spencer/.cache/bee-tmp GOFLAGS=-mod=mod && echo "=== go vet ./... (CGO off) ===" && CGO_ENABLED=0 go vet ./... 2>&1 | tail -30; echo "EXIT_VET=${PIPESTATUS[0]}"`

```
=== go vet ./... (CGO off) ===
EXIT_VET=0

```


## assistant

`go vet` is clean. Now the full test suite:

**🔧 bash** `cd /home/spencer/.cache/bee-review && export TMPDIR=/home/spencer/.cache/bee-tmp GOTMPDIR=/home/spencer/.cache/bee-tmp GOFLAGS=-mod=mod && echo "=== go test ./... ===" && CGO_ENABLED=0 go test ./... 2>&1 | tail -50; echo "EXIT_TEST=${PIPESTATUS[0]}"`

```
=== go test ./... ===
?   	github.com/spencerharmon/beehive/cmd/beehive	[no test files]
?   	github.com/spencerharmon/beehive/cmd/beehived	[no test files]
?   	github.com/spencerharmon/beehive/cmd/honeybee	[no test files]
ok  	github.com/spencerharmon/beehive/internal/claim	0.223s
ok  	github.com/spencerharmon/beehive/internal/config	0.052s
ok  	github.com/spencerharmon/beehive/internal/git	0.086s
ok  	github.com/spencerharmon/beehive/internal/links	0.005s
ok  	github.com/spencerharmon/beehive/internal/plan	0.003s
ok  	github.com/spencerharmon/beehive/internal/repo	0.007s
ok  	github.com/spencerharmon/beehive/internal/secrets	0.374s
ok  	github.com/spencerharmon/beehive/internal/select	0.294s
ok  	github.com/spencerharmon/beehive/internal/swarm	0.202s
ok  	github.com/spencerharmon/beehive/internal/web	0.109s
?   	github.com/spencerharmon/beehive/prompts	[no test files]
EXIT_TEST=0

```


## assistant

> 💭 The tests are passing, so now I'm running them with verbose output to verify the new test functions actually executed, then I'll do a static build to confirm everything works end-to-end.

All packages pass. Let me confirm the new test functions actually executed (verbose) and verify the static build.

**🔧 bash** `cd /home/spencer/.cache/bee-review && export TMPDIR=/home/spencer/.cache/bee-tmp GOTMPDIR=/home/spencer/.cache/bee-tmp GOFLAGS=-mod=mod && echo "=== targeted new tests ===" && CGO_ENABLED=0 go test -v -run 'Cyclic|CycleExported|SelectableDefersCross|LinkedDep|CrossDepRequiresLink|CyclicTasksNotSelected|PreCommit' ./internal/links/... ./internal/plan/... ./internal/select/... ./internal/config/... 2>&1 | grep -E '^(=== RUN|--- (PASS|FAIL)|ok|FAIL|PASS)' | head -60; echo "EXIT=${PIPESTATUS[0]}"`

```
=== targeted new tests ===
=== RUN   TestCycleExported
--- PASS: TestCycleExported (0.00s)
=== RUN   TestCyclicNodes
--- PASS: TestCyclicNodes (0.00s)
PASS
ok  	github.com/spencerharmon/beehive/internal/links	0.003s
=== RUN   TestSelectableDefersCrossSubmoduleDeps
--- PASS: TestSelectableDefersCrossSubmoduleDeps (0.00s)
PASS
ok  	github.com/spencerharmon/beehive/internal/plan	0.003s
=== RUN   TestLinkedDepGatesSelection
--- PASS: TestLinkedDepGatesSelection (0.04s)
=== RUN   TestLinkedDepDoneUnblocks
--- PASS: TestLinkedDepDoneUnblocks (0.03s)
=== RUN   TestCrossDepRequiresLink
--- PASS: TestCrossDepRequiresLink (0.03s)
=== RUN   TestCyclicTasksNotSelected
--- PASS: TestCyclicTasksNotSelected (0.04s)
PASS
ok  	github.com/spencerharmon/beehive/internal/select	0.145s
=== RUN   TestPreCommitHookGuards
--- PASS: TestPreCommitHookGuards (0.00s)
=== RUN   TestPreCommitDepCycleGuardE2E
--- PASS: TestPreCommitDepCycleGuardE2E (0.03s)
PASS
ok  	github.com/spencerharmon/beehive/internal/config	0.037s
EXIT=0

```


## assistant

All 8 new tests pass. Now the static build check (the project's `CGO_ENABLED=0` constraint):

**🔧 bash** `cd /home/spencer/.cache/bee-review && export TMPDIR=/home/spencer/.cache/bee-tmp GOTMPDIR=/home/spencer/.cache/bee-tmp GOFLAGS=-mod=mod GOBIN=/home/spencer/.cache/bee-tmp/bin && mkdir -p "$GOBIN" && echo "=== static build all cmds ===" && CGO_ENABLED=0 go build -o "$GOBIN/" ./cmd/... 2>&1 | tail -20; echo "EXIT_BUILD=${PIPESTATUS[0]}"; echo "=== file type ==="; file "$GOBIN/beehive" 2>&1; echo "=== lint cmd present ==="; "$GOBIN/beehive" lint --help 2>&1 | head -3`

```
=== static build all cmds ===
EXIT_BUILD=0
=== file type ===
/home/spencer/.cache/bee-tmp/bin/beehive: ELF 64-bit LSB executable, x86-64, version 1 (SYSV), statically linked, Go BuildID=tXyWUWXqNe286y7_QI-L/fL9phIwerJK3cPwn875B/3DuzVoOSG_6sk12L7lI6/nWlc2SbA4gxDV_FLBTRe, BuildID[sha1]=9733af811cdcb5dae3f02ece8bc008858dc8c47a, with debug_info, not stripped
=== lint cmd present ===
validate the combined cross-submodule dependency graph is acyclic

Usage:

```


## assistant

> 💭 The build checks out—static binary, all tests passing, and the linter working as expected. Now I'm reviewing the core logic to catch any potential issues, particularly around how the `graphGate` filters candidates. I need to verify whether gating only `StatusTODO` candidates is the right approach and understand what other candidate tiers might be affected.
> 
> Looking at the candidate tiers, review-tier work gets marked as `NEEDS-REVIEW` and arbitration tier as `NEEDS-ARBITRATION`—these should pass through untouched per the recovery design. Gating only TODO is correct. There's a subtle edge case with stale TODO tasks undergoing GC reclaim: they'd still have TODO status, so `graphGate` would apply cycle and cross-dependency checks to them. But that's actually the right behavior—cyclic or cross-dep-blocked tasks shouldn't be started regardless of whether they're fresh or stale, so this isn't a correctness issue.
> 
> For cross-dependency checking, local dependencies return true from `crossDepSatisfied` (deferring to the plan layer), which means they're not re-gated here—they're already handled by the Selectable logic upstream. No double-checking happening there.
> 
> Verifying the cycle detection: `CyclicNodes` sorts nodes and adjacency, and the DFS-based cycle finder is deterministic enough for lint reporting. Edge direction is correct—From is the task, To is the qualified dependency, matching the semantics. All nodes use the qualified namespace consistently (submodule-prefixed), so cycle detection works across the unified graph. Status map keys align with this qualified naming scheme.
> 
> Now I need to confirm that `Candidates` applies `Selectable` filtering so local dependency gating isn't bypassed.

Build is static and `beehive lint` works. Let me do a final correctness sanity check: confirm local-dep gating is still applied via `Candidates`/`Selectable` (so `graphGate` correctly only adds cross-submodule + cycle gating, no double/missing gating).

**🔧 bash** `cd /home/spencer/.cache/bee-review && echo "=== Candidates ==="; grep -n "func (p \*Plan) Candidates" -A 30 internal/plan/*.go | head -45`

```
=== Candidates ===
internal/plan/compat.go:23:func (p *Plan) Candidates(now time.Time, ttl time.Duration) []Task {
internal/plan/compat.go-24-	var gc, arb, rev, main []Task
internal/plan/compat.go-25-	for _, t := range p.Tasks {
internal/plan/compat.go-26-		switch {
internal/plan/compat.go-27-		case t.Stale(now, ttl):
internal/plan/compat.go-28-			gc = append(gc, *t)
internal/plan/compat.go-29-		case t.Status == StatusArb:
internal/plan/compat.go-30-			arb = append(arb, *t)
internal/plan/compat.go-31-		case t.Status == StatusReview:
internal/plan/compat.go-32-			rev = append(rev, *t)
internal/plan/compat.go-33-		case t.Status == StatusTODO && p.Selectable(t):
internal/plan/compat.go-34-			main = append(main, *t)
internal/plan/compat.go-35-		}
internal/plan/compat.go-36-	}
internal/plan/compat.go-37-	for _, tier := range [][]Task{gc, arb, rev, main} {
internal/plan/compat.go-38-		if len(tier) > 0 {
internal/plan/compat.go-39-			return tier
internal/plan/compat.go-40-		}
internal/plan/compat.go-41-	}
internal/plan/compat.go-42-	return nil
internal/plan/compat.go-43-}

```


## assistant

> 💭 So the tiering system returns candidates in priority order: garbage collection tier for stale tasks, then arbitration, revision, and finally the main tier which only includes TODO tasks that satisfy the selectability criteria—meaning their local dependencies are done and cross-submodule dependencies are deferred. The `graphGate` function then applies additional filtering on TODO candidates to exclude cyclic dependencies and tasks with unsatisfied cross-dependencies. I'm realizing that stale TODO tasks get classified into the garbage collection tier based on staleness checks.
> 
> But here's the tension: `graphGate` filters based on task status being TODO, not based on which tier the candidate came from. So a stale TODO task in the garbage collection tier would still get gated by `graphGate`—it would be excluded if it's on a cycle or has unsatisfied cross-dependencies. This seems like it could be a bug since the design intent is for recovery tiers to pass through untouched. I'm thinking through whether this actually causes problems: if a TODO task on a cycle gets claimed and then its heartbeat goes stale, it ends up in the garbage collection tier but gets excluded by `graphGate`, so its stale claim never gets reclaimed. But that task shouldn't have been selectable in the first place if it was cyclic, so how would it have gotten a claim? Only if the cycle was introduced after the claim was made—an edge case. The same logic applies to stale TODO tasks with unsatisfied cross-submodule dependencies.
> 
> So there's a subtle mismatch between the stated design and the actual implementation, but I'm weighing whether this blocks the review: the acceptance criteria are met and tested, the ROI intent is satisfied, and this edge case only shows up in rare race conditions. Plus, excluding cyclic tasks from selection—even in the garbage collection tier—is arguably desirable since you don't want to start working on cyclic tasks anyway. This is a minor deviation from the change doc's intent, but it's not a correctness regression since there was no cross-submodule gating before this change at all.
> 
> Let me reconsider whether it's even a real deviation. The graphGate comment says it "filters main-tier (TODO) candidates," and the author's model is that TODO equals main tier—they didn't account for stale TODO tasks landing in the GC tier. But practically, for a TODO task to end up stale in the GC tier, it must have been claimed and worked, which means it passed graphGate's filter earlier when it wasn't cyclic or blocked. For it to become cyclic now, the graph changed after the claim—a narrow race. Even then, the consequence is just that the stale claim on a now-cyclic task doesn't get auto-reclaimed via selection; the task sits stuck, which isn't a swarm deadlock and would be flagged by beehive lint anyway.
> 
> This is minor and acceptable, arguably even correct behavior. Not a reject. Let me also verify one more potential issue with how graphGate returns its filtered candidates, The implementation satisfies all acceptance criteria, has solid test coverage with no stubs, passes all checks, and includes proper documentation with the commit already pushed to origin. The only minor edge case around graphGate gating stale TODOs in GC tier is defensible and doesn't block approval. Now I need to execute the APPROVE actions: merge the submodule pointer bump into the tracked branch, mark the PLAN.md task as DONE, and unlock any dependents.
> 
> The submodule's tracked branch is main, currently at 082d38c, while the implementer's commit 03265b0 (parent 082d38c) is on origin/bee-links-graph-enforcement. The parent repo's main already points to 03265b0, but I need to fast-forward the submodule's own main branch to 03265b0 and push it to origin to complete the merge, then verify the parent pointer is recorded correctly and update the PLAN task status. 1 requires actually pushing to the submodule's origin/main, which means I need network access and proper auth to the submodule repo — that's a blocker if I can't reach it. Let me think through whether the protocol expects the reviewer to handle this step or if there's another way to mark it as complete.
> 
> Looking at the local state, the submodule has a divergent local bee-links-graph-enforcement branch (3266f49) that's different from the real one on origin (03265b0), both stemming from 082d38c. To properly merge the implementer's work into main, I'd fast-forward main from 082d38c to 03265b0 and push it, since 03265b0's parent is indeed 082d38c, making it a clean fast-forward. But whether I can actually push depends on network connectivity and whether this is even my responsibility in the workflow.
> 
> The real question is what "completion" means here — the task just needs to leave NEEDS-REVIEW status, but ideally I should do a proper APPROVE that makes the submodule work durable. Looking at prior completed tasks, they were already shipped in the codebase, so they didn't need fresh merges. This links-graph-enforcement work is genuinely new and needs to land on the tracked branch. Let me check what beehive CLI commands are available and test whether I can actually reach and push to the submodule origin.
> 
> I'm realizing I might be overthinking this. The instruction says to "merge the implementer's submodule pointer bump into the submodule's tracked branch" — which means taking the parent-repo gitlink change and ensuring the submodule's main branch advances to that commit. I can do the local merge and flip the task to DONE, but if push fails due to sandbox auth limitations, I'll document that constraint while completing what I can. I need to dig into the actual implementation of `beehive submodule sync` to see what it does and whether advancing the tracked branch is necessary for durability. Let me check the CLI code and understand the intended merge behavior.

All checks pass: vet clean, full suite green, static `CGO_ENABLED=0` binary, `beehive lint` present. 

Let me understand the merge mechanics for APPROVE — specifically what "merge into the tracked branch" requires here and whether the submodule's tracked branch needs advancing. Let me check the `beehive` CLI submodule/merge behavior and dependents.

**🔧 bash** `cd /home/spencer/.cache/bee-review && echo "=== submodule subcommands ==="; grep -rn "Use:" cmd/beehive/cmd_submodule.go 2>&1 | head; echo "=== sync impl ==="; grep -n "func.*[Ss]ync" internal/git/*.go internal/repo/*.go cmd/beehive/*.go 2>&1 | head; echo "=== dependents of links-graph-enforcement in PLAN ==="; grep -n "links-graph-enforcement" /home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782781988/submodules/beehive/PLAN.md`

```
=== submodule subcommands ===
35:	c := &cobra.Command{Use: "submodule", Short: "manage beehive submodules"}
43:		Use:   "add <repo-url>",
75:		Use:   "link <submodule-a> <submodule-b>",
102:	c := &cobra.Command{Use: "plan", Short: "submodule plan operations"}
104:		Use:   "rollback <submodule> <commit>",
=== sync impl ===
=== dependents of links-graph-enforcement in PLAN ===
95:## links-graph-enforcement [NEEDS-REVIEW] <!-- attempts=0 deps= weight=3 session=bee-1782781988 heartbeat=2026-06-30T01:13:08Z -->
101:Doc: docs/tasks/links-graph-enforcement.md
103:Review: branch bee-links-graph-enforcement commit 03265b0 bumps the beehive pointer; change doc
104:submodules/beehive/docs/bee-links-graph-enforcement-links-graph-enforcement.md. Impl: select owns

```


## assistant

> 💭 I'm checking the PLAN.md file and finding that no tasks have `deps=...links-graph-enforcement...` as a dependency — the only references are within the task's own definition, so there's nothing blocking this. I'm also looking at the `beehive submodule` CLI commands available in cmd_submodule.go and noticing it has `add`, `link`, and `plan rollback`, but there's no `sync` or `merge` command, even though the protocol documentation mentions `beehive submodule sync` and worktree operations that may not be implemented in this version.
> 
> Now I'm reconsidering what the reviewer merge actually requires in this repo. The key insight is that the recorded pointer (03265b0) is reachable via origin/bee-links-graph-enforcement on the submodule remote, so it's durable as long as nobody force-resets the submodule main. But to properly follow the protocol and ensure durability, I should advance the submodule main to 03265b0 and push it. Let me check if I have push access to the submodule origin and test connectivity with a dry-run.
> 
> Re-reading the review instruction more carefully: the parent repo is the beehive repo with `main` as its tracked branch, the "submodule pointer bump" is the gitlink change that the implementer made on their branch and it's already on parent `main` (commit 11fe7df). My review is on branch `bee-1782781988` and the runner will merge my branch into parent main.
> 
> The instruction to "merge the submodule pointer bump into the tracked branch (main)" is already satisfied since parent main has the pointer at 03265b0. My job is to flip the task to DONE on my branch and commit, then the runner merges to main. But I'm uncertain whether I should also advance the beehive submodule's own main branch to 03265b0 and push to its origin—"the submodule's tracked branch" could mean the tracked branch OF the submodule itself. I need to check how a previously-approved task handled this or look at the merge logic in the codebase to understand the canonical merge operation.

No dependents to unlock (no task lists `deps=links-graph-enforcement`). Let me examine the canonical "merge" operation in this codebase (web mergePost / git merge) to land the implementer branch on the submodule's tracked branch correctly.

**🔧 bash** `cd /home/spencer/.cache/bee-review && echo "=== mergePost ==="; grep -n "mergePost\|func.*[Mm]erge\|FastForward\|Merge(" internal/web/web.go internal/git/git.go 2>&1 | head -20; echo "=== git.go merge/ff funcs ==="; grep -n "^func " internal/git/git.go 2>&1 | head -40`

```
=== mergePost ===
internal/web/web.go:56:	mux.HandleFunc("POST /merge", s.mergePost)
internal/web/web.go:227:func (s *Server) mergeGet(w http.ResponseWriter, r *http.Request) {
internal/web/web.go:232:func (s *Server) mergePost(w http.ResponseWriter, r *http.Request) {
internal/web/web.go:244:	if err := g.Merge(r.Context(), branch); err != nil {
internal/git/git.go:76:func (r *Repo) Merge(ctx context.Context, ref string) error {
=== git.go merge/ff funcs ===
22:func New(dir string) *Repo { return &Repo{Dir: dir} }
25:func (r *Repo) Run(ctx context.Context, args ...string) (string, error) {
39:func (r *Repo) Head(ctx context.Context) (string, error) {
44:func (r *Repo) CurrentBranch(ctx context.Context) (string, error) {
51:func (r *Repo) Commit(ctx context.Context, msg string) error {
67:func (r *Repo) Clean(ctx context.Context) (bool, error) {
76:func (r *Repo) Merge(ctx context.Context, ref string) error {
87:func (r *Repo) HasConflict(ctx context.Context) (bool, error) {
96:func (r *Repo) LastCommit(ctx context.Context, path string) (string, error) {
101:func (r *Repo) WorktreeAdd(ctx context.Context, dir, branch, base string) error {
107:func (r *Repo) WorktreeRemove(ctx context.Context, dir string) error {

```


## assistant

> 💭 I'm examining the Merge function in git.go to understand how it works. Let me think through the safe approach here: the submodule's main branch needs to incorporate commit 03265b0, which is a direct child of 082d38c (the current tip), so this should be a straightforward fast-forward merge. My plan is to start by checking the submodule and proceeding from there.
> 
> Now I'm realizing the constraint: I can't write to the shared submodule working tree at `submodules/beehive/repo`. But I can still advance the submodule's origin/main by pushing directly using git operations on the git directory without touching the working tree—something like pushing 03265b0 to origin's main branch. The question is whether the sandbox has the auth needed for that push, so I should test it. But before I do that, let me reconsider what happens if I don't push: the pointer would still resolve since 03265b0 exists on the bee-links-graph-enforcement branch, and other hosts can fetch it. However, if a future task like worktree-sync-base or `beehive submodule sync` hard-resets the submodule back to origin/main (which would still be at 082d38c), then the next pointer bump would be based on the wrong commit, and the links-graph code wouldn't be in the base. That's a risk for later, but the immediate concern is whether this change itself is problematic. I need to verify whether I actually have push access to the submodule's GitHub remote before attempting anything consequential. The implementer managed to push their branch, and SSH auth worked for fetching, so it's worth testing. But before I push the submodule main to a real GitHub remote—which is a visible, permanent action—I should double-check this is genuinely the expected merge protocol. Let me look at the reconcile task's merge precedent and review the Merge function and any documentation in the repo about how reviewers handle submodule advancement.

Let me read the `Merge` implementation and check the bundled protocol/prompts to confirm the reviewer's expected merge action.

**🔧 read** `/home/spencer/.cache/bee-review/internal/git/git.go`

```
<path>/home/spencer/.cache/bee-review/internal/git/git.go</path>
<type>file</type>
<content>
76: func (r *Repo) Merge(ctx context.Context, ref string) error {
77: 	if _, err := r.Run(ctx, "merge", "--no-edit", ref); err != nil {
78: 		if c, _ := r.HasConflict(ctx); c {
79: 			return ErrConflict
80: 		}
81: 		return err
82: 	}
83: 	return nil
84: }
85: 
86: // HasConflict reports whether the tree has unmerged paths.
87: func (r *Repo) HasConflict(ctx context.Context) (bool, error) {
88: 	out, err := r.Run(ctx, "ls-files", "-u")
89: 	if err != nil {
90: 		return false, err
91: 	}
92: 	return out != "", nil
93: }
94: 
95: // LastCommit returns the full SHA of the last commit touching path.
96: func (r *Repo) LastCommit(ctx context.Context, path string) (string, error) {
97: 	return r.Run(ctx, "log", "-1", "--format=%H", "--", path)
98: }
99: 
100: // WorktreeAdd creates a worktree at dir on a new branch off base.
101: func (r *Repo) WorktreeAdd(ctx context.Context, dir, branch, base string) error {
102: 	_, err := r.Run(ctx, "worktree", "add", "-b", branch, dir, base)
103: 	return err
104: }
105: 

(Showing lines 76-105 of 110. Use offset=106 to continue.)
</content>
```


## assistant

> 💭 Let me check the parent repo's git history on main to see if there's an established pattern for how submodule branches get merged through reviews. Actually, I should step back and focus on what the review actually requires—the task needs to leave the NEEDS-REVIEW state to be considered complete.
> 
> The concrete actions are: merge the submodule pointer bump into the tracked branch, mark the task as DONE, unlock any dependents, and commit. The pointer 03265b0 is already recorded in the parent on main, it's pushed to origin, so the durability guarantee is satisfied. But I should also advance the submodule's own main branch to 03265b0 with a fast-forward push to keep future syncs clean—that's the proper "merge into tracked branch" pattern. Let me verify it's a clean fast-forward before attempting the push. The honeybee protocol explicitly expects pushing to submodule origins as part of the normal review workflow, and since the implementer already pushed their branch, this is a standard operation. The merge button is currently inert and won't automatically advance the submodule's main, so if I don't do the fast-forward push now, the work won't actually land on the tracked branch. This makes pushing the right call—it's not overstepping, it's completing the review process as designed.
> 
> Now I need to map out the exact sequence: update the parent repo's PLAN.md to mark links-graph-enforcement as [DONE] (changing only the status token, leaving session and heartbeat metadata for the runner to manage), commit that on main with a stamp, then fast-forward the submodule's origin/main from the old commit to the new one and push it. I should also figure out whether the parent commit needs a Beehive stamp. Let me check the branch relationship and working tree status to confirm whether bee-1782781988 is ahead of main or if they're at the same state, then I'll decide on the submodule push and proceed with the final edits.

No dependents to unlock, merge is `--no-edit` (FF-capable). Note the ROI flags the frontend merge as still inert (`merge-button-wire` is TODO), so advancing the submodule's tracked branch is the reviewer's job here. The implementer commit `03265b0` is exactly one commit ahead of submodule `main` (`082d38c`), so it's a clean fast-forward. Let me verify branch state and test push connectivity (non-destructive dry-run).

**🔧 bash** `cd /home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782781988 && echo "=== parent status ==="; git status -sb 2>&1 | head; echo "=== main vs my branch ==="; git rev-list --left-right --count main...bee-1782781988 2>&1; echo "=== is 082d38c ancestor of 03265b0 (clean FF)? ==="; SMG="/home/spencer/git-repos/spencerharmon/infra-beehive/.git/modules/submodules/beehive/repo"; git --git-dir="$SMG" merge-base --is-ancestor 082d38c 03265b0 && echo "YES clean FF" || echo "NO"; echo "=== submodule push dry-run (non-destructive) ==="; timeout 30 git --git-dir="$SMG" push --dry-run origin 03265b0:refs/heads/main 2>&1 | head`

```
=== parent status ===
## bee-1782781988
=== main vs my branch ===
51	0
=== is 082d38c ancestor of 03265b0 (clean FF)? ===
YES clean FF
=== submodule push dry-run (non-destructive) ===
To github.com:spencerharmon/beehive.git
   082d38c..03265b0  03265b0 -> main

```


## assistant

> 💭 I'm checking the git commit relationship between my branch and main using the rev-list command. The output shows main is 51 commits ahead while my branch has 0 new commits, meaning my branch is actually an ancestor of main rather than a diverged branch. I'm reconsidering what this means for the branch state. So the working tree is clean and matches HEAD at ae0853f, which already has NEEDS-REVIEW status and the session stamp. But main is 51 commits ahead of my branch — those are likely session log commits from the implementer's review session (bee-links-graph-enforcement-1782781988), which shares the same epoch timestamp as my current session ID.
> 
> The real concern is that when I flip PLAN.md to DONE on my branch and it merges to main, there could be conflicts if the runner has been re-stamping the heartbeat on main. I need to check whether main's PLAN.md has a newer heartbeat than what's in my worktree. I need to diff the PLAN.md files between main and my branch to see what's actually changed and make sure I'm not clobbering anything when I commit my status update. Let me check the branch topology and whether heartbeats are being stamped on my worktree, then base my edit on the latest main to keep the merge clean.

My review branch is behind `main` by 51 commits (likely session/heartbeat noise). I must base my edit on current `main` to avoid clobbering. Let me inspect the topology and any PLAN.md differences between `main` and my branch.

**🔧 bash** `cd /home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782781988 && echo "=== HEAD ==="; git rev-parse HEAD; echo "=== main ==="; git rev-parse main; echo "=== merge-base ==="; git merge-base HEAD main; echo "=== what are the 51 main-only commits (names) ==="; git log --oneline main ^HEAD | head -55 | sed 's/^[0-9a-f]* //' | sort | uniq -c | sort -rn; echo "=== PLAN.md diff main vs HEAD (task line only) ==="; git diff HEAD main -- submodules/beehive/PLAN.md | grep -E "links-graph-enforcement \["`

```
=== HEAD ===
ae0853f8cedea9cf09302ff28dfaa3e2c7ce74b4
=== main ===
b90681d2b0fb81c0dab21e3c641fc8a32417a3f6
=== merge-base ===
ae0853f8cedea9cf09302ff28dfaa3e2c7ce74b4
=== what are the 51 main-only commits (names) ===
     52 session: bee-links-graph-enforcement-1782781988
      1 Merge branch 'main' into bee-1782781988-session
=== PLAN.md diff main vs HEAD (task line only) ===

```


## assistant

> 💭 So HEAD is an ancestor of main, which means my branch is already fully merged in. The 51 commits showing as main-only are mostly session log commits from that merge, plus the merge commit itself—they're not actual feature work, just session tracking.
> 
> I'm noticing that PLAN.md is identical between HEAD and main (the grep diff came back empty), and both point to the same commit 03265b0. The only real difference is those session journal entries. The session commits are labeled with the implementer's session name "bee-links-graph-enforcement-1782781988" rather than my own "bee-1782781988", but they share the same epoch timestamp, so these are likely leftover session logs from the review process.
> 
> Since ae0853f is an ancestor of main and only session journals differ, I need to decide how to commit my DONE flip—either edit in my worktree and commit to bee-1782781988, or handle it differently given that main has already advanced.
> 
> Option A is the standard honeybee flow: I edit PLAN.md in my worktree, commit on my branch, and let the runner merge. The merge should be clean since session logs don't touch PLAN.md, and the grep confirmed the entire task header line (including heartbeat) is identical between HEAD and main, so there's no conflict risk.
> 
> I'll flip the status from NEEDS-REVIEW to DONE and commit that change on bee-1782781988. I'll also push the submodule's origin/main to 03265b0 since the dry-run succeeded — this makes the implementer's work durable on the tracked branch, which is the right move as the approving reviewer.
> 
> Let me think through the submodule push once more to be sure it's safe and within scope: it's a clean fast-forward with no history rewrite, it's the exact reviewed commit, and the protocol explicitly covers pushing to submodule origins. Without this push, the work stays on a branch and could be lost on a force-sync. The shared submodule checkout is uninitialized, so there's no active worktree to worry about — I can safely advance origin/main, and the local remote-tracking ref will update automatically.
> 
> Now I need to verify there aren't other submodules that depend on beehive with the links-graph-enforcement constraint. Let me check the full submodule list and cross-reference the submodule links to make sure I'm not missing any dependents.

Now the picture is clear: `ae0853f` (my HEAD) is an ancestor of `main`; the 51 main-only commits are all session-journal noise, and the `links-graph-enforcement` task line is identical between my branch and main. No PLAN.md conflict risk.

Let me confirm there are no other submodules or cross-submodule dependents to unlock before I act.

**🔧 bash** `cd /home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782781988 && echo "=== submodules dir ==="; ls submodules/; echo "=== any SUBMODULE-LINKS.yaml ==="; find submodules -name "SUBMODULE-LINKS.yaml" 2>/dev/null; echo "=== any task in any PLAN depending on links-graph-enforcement ==="; grep -rn "deps=[^>]*links-graph-enforcement" submodules/*/PLAN.md 2>/dev/null || echo "(none)"; echo "=== confirm task current line on disk ==="; grep -n "## links-graph-enforcement" submodules/beehive/PLAN.md`

```
=== submodules dir ===
beehive
=== any SUBMODULE-LINKS.yaml ===
=== any task in any PLAN depending on links-graph-enforcement ===
(none)
=== confirm task current line on disk ===
95:## links-graph-enforcement [NEEDS-REVIEW] <!-- attempts=0 deps= weight=3 session=bee-1782781988 heartbeat=2026-06-30T01:13:08Z -->

```


## assistant

> 💭 So there's just the one `beehive` submodule with no cross-submodule links defined anywhere, which means the links-graph-enforcement task has no dependencies blocking it and can proceed directly.
> 
> My review is to approve this. The implementation correctly integrates the internal links system into both the selection logic and the commit validation pipeline through the beehive lint command and pre-commit hook. All the acceptance criteria are met: linked-submodule dependencies gate task selection, cyclic dependencies are rejected at commit time, and the ROI is realized with cycle detection now running on every honeybee dep-tag commit. Tests pass, the build is clean, documentation is in place, and the commit is properly pushed to origin. There's a minor edge case where the graphGate catches stale-TODO tasks in the garbage collection tier due to the status check, but that's actually defensible since blocked or cyclic tasks shouldn't be started anyway.
> 
> Now I'm executing the approval by fast-forwarding the submodule push.

Verified: single submodule, no cross-submodule links, no dependents. 

My verdict is **APPROVE**. Evidence: code correctly wires `internal/links` into selection (`graphGate`) and the commit path (`beehive lint` + pre-commit guard); acceptance criteria met and tested; `go test ./...` green, `go vet` clean, static `CGO_ENABLED=0` build verified; change doc present at the correct path; commit `03265b0` carries the `Beehive:` stamp and is pushed to origin (pointer doesn't dangle).

Let me execute the approve. First, fast-forward the submodule's tracked branch (`main`) to the approved commit and push it (the durable "merge into tracked branch" step, since the frontend merge is still inert).

**🔧 todowrite** `{"todos":[{"content":"Read task design doc docs/tasks/links-graph-enforcement.md","priority":"high","status":"completed"},{"content":"Read implementer change doc bee-links-graph-enforcement-links-grap…`

```
[
  {
    "content": "Read task design doc docs/tasks/links-graph-enforcement.md",
    "status": "completed",
    "priority": "high"
  },
  {
    "content": "Read implementer change doc bee-links-graph-enforcement-links-graph-enforcement.md",
    "status": "completed",
    "priority": "high"
  },
  {
    "content": "Inspect implementer branch bee-links-graph-enforcement code via git",
    "status": "completed",
    "priority": "high"
  },
  {
    "content": "Run tests/vet/build on implementer branch",
    "status": "completed",
    "priority": "high"
  },
  {
    "content": "APPROVE: FF submodule main to 03265b0 + push (merge into tracked branch)",
    "status": "in_progress",
    "priority": "high"
  },
  {
    "content": "APPROVE: flip PLAN.md task to DONE and commit on main",
    "status": "pending",
    "priority": "high"
  }
]
```


## assistant

**🔧 bash** `SMG="/home/spencer/git-repos/spencerharmon/infra-beehive/.git/modules/submodules/beehive/repo"; echo "=== push submodule main FF 082d38c..03265b0 ==="; timeout 60 git --git-dir="$SMG" push origin 03265b0:refs/heads/main 2>&1 | head; echo "EXIT_PUSH=${PIPESTATUS[0]}"; echo "=== update local submodule main ref (CAS from 082d38c) ==="; git --git-dir="$SMG" update-ref refs/heads/main 03265b0 082d38c 2>&1 && echo "local main -> 03265b0"; echo "=== verify ==="; git --git-dir="$SMG" log --oneline -1 origin/main 2>&1; git --git-dir="$SMG" log --oneline -1 main 2>&1`

```
=== push submodule main FF 082d38c..03265b0 ===
To github.com:spencerharmon/beehive.git
   082d38c..03265b0  03265b0 -> main
EXIT_PUSH=0
=== update local submodule main ref (CAS from 082d38c) ===
local main -> 03265b0
=== verify ===
03265b0 links-graph-enforcement: cross-submodule dep gating + wait-cycle guard
03265b0 links-graph-enforcement: cross-submodule dep gating + wait-cycle guard

```


## assistant

> 💭 The submodule's main branch is now fast-forwarded to the latest commit on both origin and locally, so the work is safely persisted. Next I need to mark the task as done in PLAN.md and commit that change to my review branch, though the parent pointer won't need updating since it's already at the right commit.
> 
> I'm changing the status token from NEEDS-REVIEW to DONE in the task header, keeping the session and heartbeat metadata intact for the runner. I should also add a brief approval annotation to the task body—something like "Review (approved):" to document that the review was successful and maintain consistency with how other completed tasks are annotated in the repo. Let me edit PLAN.md now. Now I'm adding test coverage across the links, plan, select, and config modules with end-to-end tests at the commit boundary, then I'll flip the status from NEEDS-REVIEW to DONE and add an approval note to wrap this up.

The submodule's tracked branch `main` is now fast-forwarded to `03265b0` (pushed to origin + local ref updated). The implementer's work is durably on the tracked branch.

Now flip the PLAN.md task to DONE and annotate the approval. The parent pointer is already `03265b0`, so only the status and an approval note change.

**🔧 edit** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782781988/submodules/beehive/PLAN.md`

```
Edit applied successfully.
```


## assistant

**🔧 edit** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782781988/submodules/beehive/PLAN.md`

```
Edit applied successfully.
```


## assistant

> 💭 Let me verify the changes to PLAN.md and check the working tree status before committing to the parent branch.

Let me verify the edit and the working tree state before committing.

**🔧 bash** `cd /home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782781988 && echo "=== status ==="; git status -s; echo "=== task header now ==="; grep -n "## links-graph-enforcement" submodules/beehive/PLAN.md; echo "=== pointer unchanged? ==="; git ls-files --stage submodules/beehive/repo; echo "=== diff is only PLAN.md ==="; git diff --stat`

```
=== status ===
 M submodules/beehive/PLAN.md
=== task header now ===
95:## links-graph-enforcement [DONE] <!-- attempts=0 deps= weight=3 session=bee-1782781988 heartbeat=2026-06-30T01:13:08Z -->
=== pointer unchanged? ===
160000 03265b0db625ea2086a1d8c20948cc014d3ee980 0	submodules/beehive/repo
=== diff is only PLAN.md ===
 submodules/beehive/PLAN.md | 12 +++++++++++-
 1 file changed, 11 insertions(+), 1 deletion(-)

```


## assistant

> 💭 I'm updating PLAN.md to mark the task as DONE with an approval note, and the submodule pointer is already at the correct commit. Now I need to commit this on the parent branch with a message following the repo style. I'm checking whether this parent commit needs a Beehive stamp—the protocol specifies stamps for submodule commits, but I'm working on the parent branch, so I need to clarify if that applies here.
> 
> Looking at prior parent commits, they don't carry Beehive stamps, so I'll use a plain descriptive message. I'm committing on my current branch (bee-1782781988) since the runner handles merging to main, and I'll stage only PLAN.md since it's the only change.

Only PLAN.md changed (task → DONE + approval note); pointer correctly stays at `03265b0`. Now commit on the parent branch.

**🔧 bash** `cd /home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782781988 && git add submodules/beehive/PLAN.md && git commit -q -m "plan: links-graph-enforcement -> DONE (review approved)

Reviewed submodule commit 03265b0 vs task + ROI: cross-submodule dep gating +
wait-cycle guard wired into selection (select/graph.go + graphGate) and the
commit path (beehive lint + pre-commit guard). Re-verified in isolation:
go test ./... green, go vet clean, static CGO_ENABLED=0 build OK, 8 new tests
pass. Fast-forwarded beehive submodule main 082d38c->03265b0 and pushed; parent
pointer already 03265b0; no dependents to unlock." && echo "=== committed ===" && git log --oneline -1`

```
=== committed ===
8c6a609 plan: links-graph-enforcement -> DONE (review approved)

```


## assistant

> 💭 The commit went through successfully. Let me verify everything is in order: the PLAN task is marked complete, the submodule is at the right commit hash and pushed, and my branch has the changes. I should also confirm the ROI file wasn't modified and that the heartbeat/session line still reflects my claim, then do a final check that nothing else was accidentally changed.

Commit made. Let me do a final verification: confirm ROI.md was never touched, the task is DONE, the submodule tracked branch is pushed, and the claim is still mine.

**🔧 bash** `cd /home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782781988 && echo "=== working tree clean ==="; git status -s || true; echo "=== ROI untouched in this commit ==="; git show --stat HEAD | grep -i "ROI.md" && echo "ROI CHANGED!! (bad)" || echo "ROI.md NOT in commit (good)"; echo "=== task status + claim ==="; grep -n "## links-graph-enforcement" submodules/beehive/PLAN.md; echo "=== submodule origin/main tip (pushed) ==="; SMG="/home/spencer/git-repos/spencerharmon/infra-beehive/.git/modules/submodules/beehive/repo"; git --git-dir="$SMG" log --oneline -1 origin/main; echo "=== parent pointer ==="; git ls-files --stage submodules/beehive/repo`

```
=== working tree clean ===
=== ROI untouched in this commit ===
ROI.md NOT in commit (good)
=== task status + claim ===
95:## links-graph-enforcement [DONE] <!-- attempts=0 deps= weight=3 session=bee-1782781988 heartbeat=2026-06-30T01:13:08Z -->
=== submodule origin/main tip (pushed) ===
03265b0 links-graph-enforcement: cross-submodule dep gating + wait-cycle guard
=== parent pointer ===
160000 03265b0db625ea2086a1d8c20948cc014d3ee980 0	submodules/beehive/repo

```


## assistant

> 💭 Everything checks out—the working tree is clean, the task has transitioned from NEEDS-REVIEW to DONE, and the submodule's been pushed to the right commit. Now I need to clean up that temporary build directory I created.

All approve actions verified:
- Working tree clean; **ROI.md untouched**.
- Task `links-graph-enforcement` → **DONE** (claim still `session=bee-1782781988`).
- Submodule tracked branch `origin/main` fast-forwarded to `03265b0` (pushed).
- Parent pointer at `03265b0`; no dependents to unlock.

Let me clean up the temporary build/checkout dirs I created outside the repo.

**🔧 bash** `rm -rf /home/spencer/.cache/bee-review /home/spencer/.cache/bee-tmp && echo "temp build dirs removed"; SMG="/home/spencer/git-repos/spencerharmon/infra-beehive/.git/modules/submodules/beehive/repo"; echo "=== leftover review worktrees? ==="; git --git-dir="$SMG" worktree list 2>&1 | head`

```
temp build dirs removed
=== leftover review worktrees? ===
/home/spencer/git-repos/spencerharmon/infra-beehive/.git/modules/submodules/beehive/repo                                                                       03265b0 [main]
/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo 3266f49 [bee-links-graph-enforcement] prunable
/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap                                                                 082d38c [bee-bootstrap]

```


## assistant

> 💭 I used `git archive` to extract the code, so I didn't create any worktrees myself. The existing worktrees listed are from previous sessions on other hosts and aren't my responsibility to clean up as a reviewer — particularly the `bee-links-graph-enforcement` one pointing to an old commit, which is just a divergent local attempt that garbage collection will handle eventually. I'll leave them as they are.
> 
> The local submodule branch still points to an older commit while origin has moved ahead, but that's a pre-existing divergence I didn't introduce and falls outside the review scope. The implementer's authoritative version is now on main and pushed. Stale local branches will be cleaned up through deferred garbage collection tasks.
> 
> My review work is complete, so I'm wrapping up and marking everything done.

The remaining worktrees are pre-existing (other sessions/the implementer's old local attempt at `3266f49`, marked prunable) — I created none of them (I used `git archive`, not a worktree). Branch/worktree reclamation is owned by the deferred GC tasks, not this review.

**🔧 todowrite** `{"todos":[{"content":"Read task design doc docs/tasks/links-graph-enforcement.md","priority":"high","status":"completed"},{"content":"Read implementer change doc bee-links-graph-enforcement-links-grap…`

```
[
  {
    "content": "Read task design doc docs/tasks/links-graph-enforcement.md",
    "status": "completed",
    "priority": "high"
  },
  {
    "content": "Read implementer change doc bee-links-graph-enforcement-links-graph-enforcement.md",
    "status": "completed",
    "priority": "high"
  },
  {
    "content": "Inspect implementer branch bee-links-graph-enforcement code via git",
    "status": "completed",
    "priority": "high"
  },
  {
    "content": "Run tests/vet/build on implementer branch",
    "status": "completed",
    "priority": "high"
  },
  {
    "content": "APPROVE: FF submodule main to 03265b0 + push (merge into tracked branch)",
    "status": "completed",
    "priority": "high"
  },
  {
    "content": "APPROVE: flip PLAN.md task to DONE and commit on main",
    "status": "completed",
    "priority": "high"
  }
]
```


## assistant

Review complete — **APPROVED**.

## Verdict: APPROVE

The implementer's work on `bee-links-graph-enforcement` (submodule commit `03265b0`) satisfies the task and ROI.

**What I verified (read-only):**
- **Code** wires the previously-dead `internal/links` graph into both integration points the task requires:
  - *Selection*: `select/graph.go` (`LoadEdges`/`Validate`/`InCycle`/`crossDepSatisfied`) builds a combined cross-submodule graph; `graphGate` in `select.go:110` holds TODO tasks whose `<sm>:<id>` dep is unauthorized/not-DONE and excludes cyclic tasks. `plan/state.go` `Selectable` defers `":"`-qualified deps so the plan layer stays links-free.
  - *Commit path*: new `beehive lint` + a restructured pre-commit guard (`config/hook.go`) reject a cyclic PLAN.md dep-tag commit for honeybee **and** frontend identities — meeting "cycle check must run on dep-tag commits, not only CLI."
- **Acceptance criteria met & tested**: `TestLinkedDepGatesSelection`, `TestLinkedDepDoneUnblocks`, `TestCrossDepRequiresLink`, `TestCyclicTasksNotSelected`, `TestPreCommitDepCycleGuardE2E` (+ links/plan unit tests) — all 8 new tests pass.
- **Quality gates** (re-ran in an isolated checkout): `go test ./...` green, `go vet ./...` clean, static `CGO_ENABLED=0` binary confirmed (`statically linked`), `beehive lint` works.
- Change doc present at the exact path; commit carries the `Beehive:` stamp and is pushed (`origin/bee-links-graph-enforcement`), so the pointer doesn't dangle.

**Merge actions taken (on main / `bee-1782781988`):**
- Fast-forwarded the beehive submodule's tracked branch `main` `082d38c → 03265b0` and pushed to origin (the frontend merge is still inert per ROI, so this is the reviewer's job).
- Flipped PLAN.md `links-graph-enforcement` → **DONE** with an approval note; committed (`8c6a609`). Parent pointer already `03265b0`; no dependents to unlock (no task deps on it, no SUBMODULE-LINKS). ROI.md untouched.

**Non-blocking follow-up** (noted in the plan): `graphGate` keys off `Status==TODO`, so a stale-TODO task that lands in the GC tier is also cycle/dep-gated — a narrow post-claim race, but conservative and acceptable (a cyclic task shouldn't be started anyway).

The task has left NEEDS-REVIEW, so the run is complete.

