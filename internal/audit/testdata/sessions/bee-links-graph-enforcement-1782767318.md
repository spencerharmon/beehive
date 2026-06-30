# session bee-links-graph-enforcement-1782767318

submodule: beehive · kind: work · branch: bee-links-graph-enforcement

## user

# Context
You are working from the beehive repo root (cwd). Submodule: beehive.
Coordination files: submodules/beehive/ROI.md (read-only), submodules/beehive/PLAN.md, submodules/beehive/docs/.
Code worktree (for Work tasks): submodules/beehive/worktrees/bee-links-graph-enforcement.
Act autonomously: do not ask for confirmation; make the edits and commits the protocol requires.

select a task from submodules/<submodule>/PLAN.md and begin



## assistant

> 💭 I'm starting by exploring the repository structure and reviewing the key coordination files to understand what's already been planned and documented.

I'll start by reading the coordination files to understand the current state and select a task.

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/PLAN.md`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/PLAN.md</path>
<type>file</type>
<content>
1: <!-- Beehive-ROI: af3f9092ab48fc2e624d39c67c41353ab4c9aa74 -->
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
31: ## claim-repull-verify [TODO] <!-- attempts=0 deps=git-remote-ops weight=4 -->
32: Make the claim lock real. claim.go verify (claim.go:79) re-reads the LOCAL PLAN.md and never
33: re-pulls, so two bees both "win". After Commit in Claim (claim.go:57) and Heartbeat
34: (claim.go:95): pull beehive main, reload PLAN.md, verify our stamp owns it, ErrLost on
35: mismatch (abandon + reselect).
36: Files: internal/claim/claim.go, internal/claim/claim_test.go.
37: Doc: docs/tasks/claim-repull-verify.md
38: Accept: two-claimer race test yields exactly one winner; loser gets ErrLost.
39: 
40: ## claimer-reject-guard [TODO] <!-- attempts=0 deps= weight=3 -->
41: Claimer.Reject (claim.go:133) bumps Attempts unconditionally. Guard status first: only
42: NEEDS-REVIEW or NEEDS-ARBITRATION may be rejected (mirror plan.Task.Reject, state.go:56).
43: Reject from any other status must error and leave Attempts/Status untouched.
44: Files: internal/claim/claim.go, internal/claim/claim_test.go.
45: Doc: docs/tasks/claimer-reject-guard.md
46: Accept: reject on TODO/IN-PROGRESS/DONE errors; attempts unchanged; review/arb still work.
47: 
48: ## opencode-turn-poll [TODO] <!-- attempts=0 deps= weight=4 -->
49: opencode turn engine is fire-and-forget: ocSession.Prompt (opencode.go:49) returns on accept,
50: so the runner's completion check runs before the assistant acts; all turns burn in ms.
51: Poll the session (GET session messages/state) until the assistant turn is idle before
52: returning from a turn / running complete().
53: Files: internal/swarm/opencode.go, internal/swarm/swarm.go (Run loop), swarm_test.go.
54: Doc: docs/tasks/opencode-turn-poll.md
55: Accept: Prompt blocks until turn idle; fake-server test proves no premature completion.
56: 
57: ## worktree-sync-base [TODO] <!-- attempts=0 deps=git-remote-ops weight=4 -->
58: Worktrees branch off HEAD with no sync (swarm.go:78), so bees work stale code. Fetch +
59: hard-reset the tracked tip first, then branch off the synced tip. Wire
60: scripts/submodule-sync.sh into the runner (auto-advances the beehive pointer, no review).
61: Files: internal/swarm/swarm.go, scripts/submodule-sync.sh, swarm_test.go.
62: Doc: docs/tasks/worktree-sync-base.md
63: Accept: runner syncs tracked branch before WorktreeAdd; test asserts base == remote tip.
64: 
65: ## gc-worktree-reclaim [TODO] <!-- attempts=0 deps= weight=3 -->
66: GC orphans worktrees on cap: Run sets GCMarked but never removes the worktree at the
67: turn/wall cap (swarm.go:116) — only the DONE path (swarm.go:108) reclaims. Remove the
68: worktree (or record a GC marker) at the cap too, leaving the IN-PROGRESS heartbeat for GC.
69: Files: internal/swarm/swarm.go, swarm_test.go.
70: Doc: docs/tasks/gc-worktree-reclaim.md
71: Accept: cap path reclaims worktree/records marker; task stays IN-PROGRESS for stale GC.
72: 
73: ## reconcile-prefix-match [TODO] <!-- attempts=0 deps= weight=3 -->
74: Reconcile completion never fires and the diff base is bogus. swarm.go reconciled()
75: (swarm.go:133) does exact `stamp == head` but stamp is short and head is full sha. Compare
76: by prefix. select.go reconcileRange (select.go:121) uses a "ROOT" sentinel for the empty
77: base; replace with the git empty-tree sha 4b825dc642cb6eb9a060e54bf8d69288fbee4904.
78: Files: internal/swarm/swarm.go, internal/select/select.go, *_test.go.
79: Doc: docs/tasks/reconcile-prefix-match.md
80: Accept: short/full-sha prefix match fires once and clears; empty-base diff range is valid.
81: 
82: ## links-graph-enforcement [IN-PROGRESS] <!-- attempts=0 deps= weight=3 heartbeat=2026-06-29T21:08:38Z -->
83: Cross-submodule deps and wait-cycle detection are unused in selection. Resolve linked-
84: submodule deps and run links.HasCycle during candidate selection (select.go fromSubmodule /
85: plan.Candidates). The cycle check must also run on honeybee dep-tag commits (PLAN.md writes),
86: not only the CLI (links.AddDep).
87: Files: internal/select/select.go, internal/links/links.go, internal/plan, commit-path guard.
88: Doc: docs/tasks/links-graph-enforcement.md
89: Accept: linked-submodule dep gates selection; a dep-tag commit forming a cycle is rejected.
90: 
91: ## artifacts-package [TODO] <!-- attempts=0 deps= weight=2 -->
92: internal/artifacts is missing; the web reads ARTIFACTS.md / INFRASTRUCTURE.md raw
93: (web.go:129 explorer, env.go for INFRA). Add a typed model + parser, wire web reads through it.
94: Files: new internal/artifacts/*, internal/web/web.go, internal/web/env.go.
95: Doc: docs/tasks/artifacts-package.md
96: Accept: structured parse/render of both files; web stops reading raw; round-trip tests.
97: 
98: ## roi-pre-receive-hook [TODO] <!-- attempts=0 deps= weight=2 -->
99: ROI protection is incomplete: only a pre-commit hook exists (config/hook.go); pushes are
100: unprotected. Add a server-side pre-receive hook (mirror the honeybee-identity check) plus an
101: install path.
102: Files: internal/config/hook.go, internal/config/hook_test.go.
103: Doc: docs/tasks/roi-pre-receive-hook.md
104: Accept: a push touching ROI.md under honeybee identity is rejected; frontend push allowed.
105: 
106: ## frontend-write-paths-cli [TODO] <!-- attempts=0 deps= weight=2 -->
107: Frontend write paths bypass CLI logic. submoduleAdd does a bare mkdir (web.go:259) instead of
108: `git submodule add`; submoduleLink appends raw `from: [to]` YAML (web.go:276) instead of
109: links.AddDep (cycle-checked). Extract a shared helper from cmd/beehive/cmd_submodule.go and
110: call it from both CLI and web.
111: Files: internal/web/web.go, cmd/beehive/cmd_submodule.go, shared helper pkg, tests.
112: Doc: docs/tasks/frontend-write-paths-cli.md
113: Accept: web add creates a real tracked submodule; web link is cycle-checked valid YAML.
114: 
115: ## web-plan-parser-unify [TODO] <!-- attempts=0 deps= weight=3 -->
116: The web has its own PLAN.md parser (web/plan.go) expecting a bullet format that does NOT match
117: the real header format (internal/plan/plan.go). Dashboard pending/NEEDS-HUMAN counts, plan
118: view, and human view all misparse real plans. Delegate web parsing to internal/plan; unify the
119: duplicate roiStamp regex. Enabler for dashboard-cards and plan-view-pills.
120: Files: internal/web/plan.go, internal/web/web.go, internal/web/env.go, web_test.go.
121: Doc: docs/tasks/web-plan-parser-unify.md
122: Accept: a real header-format PLAN.md parses; counts/pills correct; tests use real format.
123: 
124: ## publish-main-writes [TODO] <!-- attempts=0 deps=git-remote-ops weight=2 -->
125: beehived writes commit locally but never push, so edits don't reach origin and other
126: hosts/honeybees never see them. Every handler uses s.commit (web.go:341): roiPost (web.go:182),
127: secretsPost (209), mergePost (232), submoduleAdd (259), submoduleLink (276), envDeploy (306).
128: Add publishMain (commit + push origin main, ff-retry on non-ff) and route all writes through it.
129: Files: internal/web/web.go, web_test.go.
130: Doc: docs/tasks/publish-main-writes.md
131: Accept: each write lands on origin main (temp-origin test); concurrent advance retries, no lost
132: write; no-origin install still commits locally.
133: 
134: ## config-layered [TODO] <!-- attempts=0 deps= weight=2 -->
135: config.Load (config.go:53) reads one flat /etc/beehive/config.yaml; no layering, no per-submodule
136: scope, no temperature/token-limit knobs. Add Resolve(root, submodule) merging layers most-
137: specific-wins: Defaults -> host file -> in-repo global -> per-submodule override; add
138: Temperature/MaxTokens and thread them into opencode (opencode.go:51). Callers resolve the
139: effective config per submodule at runtime.
140: Files: internal/config/config.go, config_test.go, internal/swarm/opencode.go.
141: Doc: docs/tasks/config-layered.md
142: Accept: per-scope precedence verified field-by-field (submodule>global>host>default); unset
143: fields fall through; bare single-host install still works.
144: 
145: ## design-system-css [TODO] <!-- attempts=0 deps= weight=2 -->
146: Replace the ad-hoc 4-line style.css with a coherent design system: typography scale, spacing
147: scale, and status-color tokens for TODO / IN-PROGRESS / NEEDS-REVIEW / NEEDS-ARBITRATION /
148: DONE / NEEDS-HUMAN, in light + dark (prefers-color-scheme). Foundation for all frontend views.
149: Keep the single-binary embed (no SPA, no CDN).
150: Files: internal/web/assets/style.css, internal/web/templates/layout.html.
151: Doc: docs/tasks/design-system-css.md
152: Accept: tokens cover all six statuses; dark mode works; assets stay embedded.
153: 
154: ## dashboard-cards [TODO] <!-- attempts=0 deps=design-system-css,web-plan-parser-unify -->
155: Dashboard as submodule cards: live swarm status (active/dormant/bootstrap), env badge
156: (blue/green), and a NEEDS-HUMAN count. Counts must come from the unified parser.
157: Files: internal/web/templates/dashboard.html, internal/web/web.go (subView).
158: Doc: docs/tasks/dashboard-cards.md
159: Accept: one card per submodule with correct state, env badge, and human count.
160: 
161: ## plan-view-pills [TODO] <!-- attempts=0 deps=design-system-css,web-plan-parser-unify -->
162: Plan view: clear status pills (tokenized), dependency indicators, heartbeat/TTL freshness
163: indicators for IN-PROGRESS tasks, and change-doc links from the commit stamp.
164: Files: internal/web/templates/plan_items.html, internal/web/web.go.
165: Doc: docs/tasks/plan-view-pills.md
166: Accept: pills per status; deps shown; stale heartbeat flagged; doc links resolve.
167: 
168: ## branch-graph-sectioned [TODO] <!-- attempts=0 deps=design-system-css -->
169: Branch graph sectioned/paginated per submodule with commit-stamp linkage and no cross-
170: submodule crawl (branches.go already paginates one repo; add sectioning + stamp links UI).
171: Files: internal/web/templates/branch_view.html, internal/web/branches.go.
172: Doc: docs/tasks/branch-graph-sectioned.md
173: Accept: per-submodule sections, paginated, each commit links its Beehive change doc.
174: 
175: ## htmx-polish [TODO] <!-- attempts=0 deps=design-system-css -->
176: Polish htmx interactions: loading states/indicators, inline edit affordances, and a confirm
177: prompt on destructive merge. Keep single-binary embed; no SPA.
178: Files: internal/web/templates/*.html.
179: Doc: docs/tasks/htmx-polish.md
180: Accept: visible loading state on swaps; inline edit on ROI/secrets; merge asks confirm.
181: 
182: ## poll-scroll-preserve [TODO] <!-- attempts=0 deps=design-system-css weight=2 -->
183: htmx poll refresh replaces scrollable panes (markdown explorer, chat panel, session list),
184: resetting scrollTop to 0 mid-read. Patch-in-place (hx-preserve/morph or OOB row swaps) or
185: save/restore scrollTop around htmx:beforeSwap/afterSwap via embedded JS in the layout; keep
186: chat/stream bottom-pinned. (Polling panes live on the synced tip, ahead of 082d38c.)
187: Files: internal/web/templates/*.html, internal/web/assets/*, web_test.go.
188: Doc: docs/tasks/poll-scroll-preserve.md
189: Accept: polling fragments carry preserve/pane-id wiring and the restore script is embedded; live
190: updates not dropped; manual scroll check documented.
191: 
192: ## merge-button-wire [TODO] <!-- attempts=0 deps=git-remote-ops weight=2 -->
193: The merge action must publish a change, not no-op. mergePost (web.go:232) merges + commits LOCALLY
194: but never pushes origin nor advances/commits the beehive pointer; the branch-view publish control
195: is inert. Route merge through publishMain: Merge -> Push origin tracked-branch -> stage+commit the
196: pointer; wire the UI control with destructive-confirm.
197: Files: internal/web/web.go, internal/web/templates/merge_panel.html, branch_view.html, web_test.go.
198: Doc: docs/tasks/merge-button-wire.md
199: Accept: POST /merge ff's + pushes origin + advances pointer (temp-origin test); conflict returns
200: 409, origin untouched; idempotent on already-merged.
201: 
202: ## chat-diff-editor-core [TODO] <!-- attempts=0 deps=opencode-turn-poll weight=2 -->
203: Generalize the per-file editor into one chat-diff surface over any repo file. 082d38c has only a
204: textarea (roiGet/roiPost web.go:172-198); the shipped chat editor is on the synced tip. Add a
205: generic /edit?path= handler backed by an opencode session in a per-edit ROOT worktree: render
206: worktree state inline, propose a unified diff, apply+commit only on human approval. Reuse
207: opencode-turn-poll to await the assistant turn.
208: Files: internal/web/web.go, internal/web/templates/*, internal/swarm, web_test.go.
209: Doc: docs/tasks/chat-diff-editor-core.md
210: Accept: a chat turn for an arbitrary path yields a proposed diff; approve writes+commits in the
211: edit worktree, reject is a no-op; path traversal rejected. FOUNDATION for the chat-diff tasks.
212: 
213: ## chat-diff-file-context [TODO] <!-- attempts=0 deps=chat-diff-editor-core -->
214: Per-file edits need injected rules so they stay formatted/protocol-safe. Add a path->context
215: resolver (ROI.md conventions+FORBIDDEN, PLAN.md format/state machine per internal/plan, RULES.md +
216: AGENTS.md, generic default) and seed it into the opencode session (NewSession system arg,
217: opencode.go:25) for both per-file links and the generic window. Per-file links route into the
218: generic handler with path preset, replacing bare textareas.
219: Files: internal/web (context resolver + handlers), templates, web_test.go.
220: Doc: docs/tasks/chat-diff-file-context.md
221: Accept: distinct preambles per path; seeded prompt contains the target's rules; editing PLAN.md
222: still round-trips plan.Parse.
223: 
224: ## submodule-rules-md [TODO] <!-- attempts=0 deps=chat-diff-editor-core weight=2 -->
225: Add a per-submodule beehive-owned RULES.md at submodules/<sm>/RULES.md (outside repo/), additive
226: to any AGENTS.md, read into honeybee + chat-diff editor context and shown in the explorer
227: (web.go:129 docs map). Add the path constant alongside ROIFile/PlanFile/InfraFile/Artifacts
228: (internal/repo). Absence is a safe no-op.
229: Files: internal/repo (constants), internal/web/web.go, context resolver, web_test.go.
230: Doc: docs/tasks/submodule-rules-md.md
231: Accept: present RULES.md appears in explorer + agent/edit context; AGENTS.md+RULES.md both present,
232: order AGENTS-then-RULES; absence no-op; never auto-edits ROI.md.
233: 
234: ## chat-skills [TODO] <!-- attempts=0 deps=chat-diff-editor-core -->
235: Expose named, invocable maintenance skills from the chat surface: cleanup-stale, gc (reuse
236: gc-worktree-reclaim/editor-session-persist), resources (INFRASTRUCTURE.md rigs), infra-conventions.
237: Skills propose diffs via the approval path or report-only; destructive ones require confirm.
238: Files: internal/web (skill registry + chat wiring), templates, tests.
239: Doc: docs/tasks/chat-skills.md
240: Accept: registry lookup + dry-run returns a deterministic plan without mutating; applying performs
241: exactly the proposed change; unknown skill errors; no destructive action without approval.
242: 
243: ## frontend-perf-cache [TODO] <!-- attempts=0 deps=web-plan-parser-unify -->
244: Deferred. Parse-once frontend cache: cache derived views, invalidate on commit (HEAD change);
245: document the supported-submodule ceiling the cache assumes.
246: Files: internal/web (cache layer), web_test.go.
247: Doc: docs/tasks/frontend-perf-cache.md
248: Accept: repeated reads hit cache; a commit invalidates; ceiling documented + tested.
249: 
250: ## multi-beehive-ui [TODO] <!-- attempts=0 deps= -->
251: Deferred + coarse (re-decompose when prioritized). Manage/merge multiple beehive repos from
252: one frontend, with per-repo gpg keyrings for strict secret isolation.
253: Files: internal/web, internal/config, internal/secrets.
254: Doc: docs/tasks/multi-beehive-ui.md
255: Accept: register/switch repos; secrets never cross repo keyrings; design doc enumerates subtasks.
256: 
257: ## release-verify [TODO] <!-- attempts=0 deps= -->
258: Deferred. Confirm CI cross-compiles static binaries (CGO_ENABLED=0) and cosign-signs them, and
259: that the artifacts verify. Fix the pipeline if not.
260: Files: .github/workflows/*, packaging/*, docs/RELEASE-NOTES-TEMPLATE.md.
261: Doc: docs/tasks/release-verify.md
262: Accept: CI produces signed static artifacts; `cosign verify-blob` passes in a clean checkout.
263: 
264: ## editor-markdown-render [TODO] <!-- attempts=0 deps=design-system-css -->
265: Deferred. Editor/explorer render markdown as raw text (roiGet web.go:172 textarea; explorer
266: web.go:123 dumps strings; chat-diff shows raw diffs). Render markdown->HTML for VIEW panes with a
267: pure-Go CGO-free renderer (goldmark/gomarkdown), sanitized; keep an editable raw source view; style
268: diffs with design tokens.
269: Files: internal/web (render helper), templates, assets, web_test.go.
270: Doc: docs/tasks/editor-markdown-render.md
271: Accept: markdown renders to expected sanitized HTML; raw edit path returns verbatim; diff renders
272: with add/del classes; dependency is CGO-free.
273: 
274: ## editor-session-persist [TODO] <!-- attempts=0 deps= -->
275: Deferred. The shipped chat editor leaks ROOT edit worktrees (live: edit-* branches in git worktree
276: list) and loses in-flight sessions on restart. Persist session state (path, session id, branch,
277: activity); at startup prune stale edit-* worktrees + branches (TTL/no live session) via internal/git
278: Worktree* (mirror gc-worktree-reclaim), never touching bee-* or the main checkout.
279: Files: internal/web or internal/swarm (session store + startup prune), internal/git, tests.
280: Doc: docs/tasks/editor-session-persist.md
281: Accept: startup prune removes exactly stale edit worktrees+branches, keeps active/bee-*; session
282: state survives a simulated restart; never removes worktrees with pending approved changes.
283: 
284: ## remote-host-session-view [TODO] <!-- attempts=0 deps=git-remote-ops -->
285: Deferred. Follow off-box honeybee runs: sessions are markdown under docs/sessions/<branch>.md.
286: Producer periodically commits+pushes session appends (remote-flagged cadence); viewer periodically
287: pulls --ff-only (git-remote-ops Pull) and re-renders the session pane, surfacing last-pulled
288: staleness. Combine with poll-scroll-preserve.
289: Files: internal/swarm (session writer cadence), internal/web (viewer pull+render), tests.
290: Doc: docs/tasks/remote-host-session-view.md
291: Accept: fake-origin periodic session commits are picked up by the viewer pull and rendered;
292: ff-only divergence handled; commits coalesced per interval.
293: 
294: ## agent-output-streaming [TODO] <!-- attempts=0 deps=opencode-turn-poll -->
295: Deferred. opencode is request/response (Prompt opencode.go:49 returns on accept). Consume opencode's
296: streaming/event channel to emit assistant tokens live; add a streaming read path in internal/swarm
297: beside the poll path (stream-end => turn-idle, consistent with opencode-turn-poll). Surface via
298: embedded SSE to the chat/session pane (bottom-pinned); fall back to poll-then-render if unsupported.
299: Files: internal/swarm/opencode.go, swarm_test.go, internal/web (SSE pane), templates.
300: Doc: docs/tasks/agent-output-streaming.md
301: Accept: fake streaming server tokens assemble incrementally; stream-end maps to turn-idle;
302: non-streaming server falls back; ctx-cancel leaks no goroutines.

(End of file - total 302 lines)
</content>
```

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/ROI.md`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/ROI.md</path>
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
17: - Claim lock is fake: claim.go verify re-reads the local file, never re-pulls. Two bees
18:   both win. Pull main after commit, reload, abandon on stamp mismatch. Same for Heartbeat.
19: - opencode turn engine is fire-and-forget: Prompt returns on accept, all turns burn in ms.
20:   Poll session until the assistant turn is idle before the completion check.
21: - Worktrees branch off HEAD with no sync; must fetch+hard-reset tracked tip first, then
22:   branch off the synced tip. Wire scripts/submodule-sync.sh into the runner.
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
36: - Claimer.Reject must guard status (only NEEDS-REVIEW/ARBITRATION) before bumping attempts.
37: 
38: ## Frontend aesthetics
39: Make beehived presentable and consistent.
40: - Replace ad-hoc style.css with a coherent design system: typography scale, spacing,
41:   status-color tokens (TODO/IN-PROGRESS/REVIEW/ARBITRATION/DONE/HUMAN), light+dark.
42: - Dashboard: submodule cards with live swarm status, env badge, NEEDS-HUMAN count.
43: - Plan view: clear state pills, dependency + heartbeat/TTL indicators, change-doc links.
44: - Branch graph: sectioned/paginated per submodule, commit-stamp linkage; no cross crawl.
45: - htmx interactions polished (loading states, inline edit, confirm on destructive merge).
46: - Poll refresh must preserve scroll: live updates reset the markdown pane and chat panel to
47:   top; restore scroll position (or patch in place) so reading isn't interrupted.
48: - Merge button is inert: wire the action so it actually publishes/merges the change instead
49:   of no-op'ing.
50: - Keep single-binary embed; no SPA.
51: 
52: ## Deferred features to complete
53: Shipped since this ROI: per-honeybee worktree isolation with publish-to-main (no write
54: lock), native worktree/submodule CLI subcommands, unique epoch session IDs, live
55: auto-refreshing session list, delete-plan button forcing rebootstrap from ROI, per-turn
56: guard that aborts a honeybee when its task leaves main, and an AI chat editor for
57: human-owned files.
58: 
59: Still pending:
60: - Frontend perf cache: parse-once, invalidate on commit; state supported submodule ceiling.
61: - Multi-beehive management UI: manage/merge multiple beehive repos, per-repo keyrings for
62:   strict secret isolation.
63: - Release: confirm CI cross-compile + cosign signing produce verifiable static artifacts.
64: - Editor diffs: pretty markdown->HTML rendering; raw text today.
65: - Editor sessions: persistence + startup prune of stale edit worktrees.
66: - Remote-host live session view: periodic commits + git pull to follow off-box runs.
67: - Token-by-token streaming of agent output.
68: - Fold publishMain into all beehived write paths.
69: 
70: ## Configuration
71: Layered config, most-specific wins: /etc/beehive (host) -> in-repo global defaults ->
72: per-submodule overrides. Tunable per scope: agent backend URL (opencode for now) and
73: model settings (model, temperature, token limits). Ship sane defaults and resolve the
74: effective value at runtime.
75: 
76: ## Generalized chat-diff editor
77: Generalize the human-file editor into one chat-diff surface over the whole beehive repo.
78: - Generic chat window: discuss and apply edits across any repo file; the agent renders
79:   worktree state inline (like today's editor) and proposes diffs for approval.
80: - Per-file edit links reuse the same chat-diff interface, injecting file-specific context
81:   (ROI conventions, honeybee protocol) so edits stay correctly formatted and protocol-safe;
82:   the generic window pulls the same context and rules per target file.
83: - Per-submodule RULES.md: beehive-owned rules + context, out-of-tree from the submodule's
84:   own source but kept in its dir, editable by user and agent via the chat-diff editor.
85:   Respect an existing AGENTS.md if present; RULES.md is additive.
86: - Agent skills invocable from the chat surface: cleanup of old/unused files after beehive
87:   updates, garbage collection, resource management, infrastructure conventions.
88: 
89: ## Constraints
90: - Pure Go, static binaries (CGO_ENABLED=0). Single binary per component, embedded assets.
91: - ROI.md is human-owned; honeybees never edit it. opencode is the provider-agnostic agent.
92: - Every fix ships with tests; no weakened tests, no swallowed errors, no stub values.

(End of file - total 92 lines)
</content>
```


## assistant

> 💭 I'm looking at the `links-graph-enforcement` task that's already in progress. It's about resolving unused cross-submodule dependencies and wait-cycle detection in the selection logic.
> 
> The work involves integrating cycle detection into the candidate selection process in `select.go`, ensuring that the cycle check runs not just during CLI operations but also when processing dep-tag commits in the plan. I need to follow the protocol: first reconcile ROI changes, then re-stamp the heartbeat since this task is already assigned to me, then handle garbage collection, arbitration, and review before tackling the main implementation.
> 
> Let me start by checking the repository structure and understanding what tools are available—whether it's the shell scripts or the beehive CLI—and verify the ROI reconciliation status.

Now let me understand the current state of the repository and the task I've been assigned (`links-graph-enforcement`, already IN-PROGRESS with my worktree).

**🔧 bash** `git -C /home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318 status && echo "=== LOG ===" && git -C /home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318 log --oneline -10`

```
On branch bee-1782767318
Untracked files:
  (use "git add <file>..." to include in what will be committed)
	submodules/beehive/sessions/bee-links-graph-enforcement-1782767318.md

nothing added to commit but untracked files present (use "git add" to track)
=== LOG ===
fdfceff plan: heartbeat links-graph-enforcement
d954fdf plan: claim links-graph-enforcement
fe6ab9f session: bee-bootstrap-1782766865
52f61c0 plan: rebootstrap beehive PLAN.md from expanded ROI (32 tasks)
3e56638 editor: INFRASTRUCTURE.md
af3f909 editor: submodules/beehive/ROI.md
b985ef3 editor: submodules/beehive/ROI.md
addd232 editor: submodules/beehive/ROI.md
d85d72b editor: submodules/beehive/ROI.md
722b3ba editor: submodules/beehive/ROI.md

```

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/docs/tasks/links-graph-enforcement.md`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/docs/tasks/links-graph-enforcement.md</path>
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

> 💭 I'm checking the ROI reconciliation protocol by comparing the ROI.md file against the PLAN.md timestamp to see if there have been any changes since the last bootstrap. I need to verify the worktree exists and examine the beehive submodule structure, along with checking ROI.md's history and reviewing the key source files like select.go, links.go, and the plan package to understand the current state.

I have my assigned task: `links-graph-enforcement`. Let me verify ROI reconcile status, check my worktree, and explore the relevant code.

**🔧 bash** `cd /home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318 && echo "=== ROI.md history (outer repo) ===" && git log --oneline -5 -- submodules/beehive/ROI.md && echo "=== Is af3f909 the latest ROI change? ===" && git log --oneline af3f909..HEAD -- submodules/beehive/ROI.md && echo "(empty above = no ROI change since stamp)" && echo "=== worktree listing ===" && ls -la submodules/beehive/worktrees/ 2>/dev/null || echo "no worktrees dir"`

```
=== ROI.md history (outer repo) ===
af3f909 editor: submodules/beehive/ROI.md
b985ef3 editor: submodules/beehive/ROI.md
addd232 editor: submodules/beehive/ROI.md
d85d72b editor: submodules/beehive/ROI.md
722b3ba editor: submodules/beehive/ROI.md
=== Is af3f909 the latest ROI change? ===
(empty above = no ROI change since stamp)
=== worktree listing ===
total 0
drwxr-xr-x 4 spencer spencer 62 Jun 29 16:08 .
drwxr-xr-x 6 spencer spencer 92 Jun 29 16:08 ..
drwxr-xr-x 2 spencer spencer  6 Jun 29 16:08 bee-bootstrap
drwxr-xr-x 3 spencer spencer 97 Jun 29 16:08 bee-links-graph-enforcement

```

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive</path>
<type>directory</type>
<entries>
docs/
PLAN.md
repo/
ROI.md
sessions/
worktrees/

(6 entries)
</entries>
```


## assistant

> 💭 ROI reconciliation checks out since the stamp hasn't changed. Now I'm examining the directory structure—there's the shared repo checkout in `submodules/beehive/repo/` where I shouldn't make changes, and my worktree at `submodules/beehive/worktrees/bee-links-graph-enforcement/` where I can write. Let me explore both and refresh my heartbeat timestamp as the next protocol step.

ROI reconcile not needed (no ROI change since stamp). My worktree exists. Let me explore the code structure and available tooling.

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement</path>
<type>directory</type>
<entries>
.git
.gitmodules
AGENTS.md
INFRASTRUCTURE.md
submodules/

(5 entries)
</entries>
```

**🔧 bash** `cd /home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318 && echo "=== scripts ===" && ls scripts/ 2>/dev/null && echo "=== beehive CLI? ===" && which beehive 2>/dev/null; ls -la submodules/beehive/repo/ 2>/dev/null | head -30`

```
=== scripts ===
total 0
drwxr-xr-x 2 spencer spencer  6 Jun 29 16:08 .
drwxr-xr-x 6 spencer spencer 92 Jun 29 16:08 ..

```


## assistant

> 💭 I'm realizing there's some confusion here—the worktree `bee-links-graph-enforcement` appears to be a full checkout of the outer beehive repository with all its structure intact (including .gitmodules and submodules), but the actual task is focused on code within the beehive submodule itself, specifically the internal packages like select, links, and plan.
> 
> Let me dig into the actual beehive source code location. I need to check what's actually in `submodules/beehive/repo/` to find where the Go code lives.

The worktree appears to mirror the outer repo structure. Let me explore more carefully to locate the actual Go source code.

**🔧 bash** `cd /home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318 && echo "=== repo/ checkout ===" && ls -la submodules/beehive/repo/ && echo "=== worktree contents (recursive, 2 levels) ===" && find submodules/beehive/worktrees/bee-links-graph-enforcement -maxdepth 2 -not -path '*/.git/*' | head -40`

```
=== repo/ checkout ===
total 0
drwxr-xr-x 2 spencer spencer  6 Jun 29 16:08 .
drwxr-xr-x 6 spencer spencer 92 Jun 29 16:08 ..
=== worktree contents (recursive, 2 levels) ===
submodules/beehive/worktrees/bee-links-graph-enforcement
submodules/beehive/worktrees/bee-links-graph-enforcement/.git
submodules/beehive/worktrees/bee-links-graph-enforcement/.gitmodules
submodules/beehive/worktrees/bee-links-graph-enforcement/AGENTS.md
submodules/beehive/worktrees/bee-links-graph-enforcement/INFRASTRUCTURE.md
submodules/beehive/worktrees/bee-links-graph-enforcement/submodules
submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive

```


## assistant

> 💭 The submodule directory is empty because it hasn't been initialized, but the worktree itself is a checkout of the outer beehive repo. Let me reconsider the structure based on the AGENTS.md documentation—the worktree should actually be located at `submodules/beehive/worktrees/bee-links-graph-enforcement`, not in the submodule root.
> 
> But I'm seeing the outer repo structure (`.gitmodules`, `submodules/beehive/`) inside the worktree, which suggests either the beehive source repo itself is structured this way, or I need to check what the worktree actually points to by examining its `.git` file and the git worktree configuration.

**🔧 bash** `cd /home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318 && echo "=== worktree .git ===" && cat submodules/beehive/worktrees/bee-links-graph-enforcement/.git && echo "=== worktree .gitmodules ===" && cat submodules/beehive/worktrees/bee-links-graph-enforcement/.gitmodules && echo "=== worktree submodules/beehive ===" && ls -la submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/`

```
=== worktree .git ===
gitdir: /home/spencer/git-repos/spencerharmon/infra-beehive/.git/worktrees/bee-links-graph-enforcement
=== worktree .gitmodules ===
[submodule "submodules/beehive/repo"]
	path = submodules/beehive/repo
	url = git@github.com:spencerharmon/beehive.git
	branch = main
=== worktree submodules/beehive ===
total 28
drwxr-xr-x 6 spencer spencer    92 Jun 29 16:08 .
drwxr-xr-x 3 spencer spencer    21 Jun 29 16:08 ..
drwxr-xr-x 4 spencer spencer    35 Jun 29 16:08 docs
-rw-r--r-- 1 spencer spencer 19974 Jun 29 16:08 PLAN.md
drwxr-xr-x 2 spencer spencer     6 Jun 29 16:08 repo
-rw-r--r-- 1 spencer spencer  5774 Jun 29 16:08 ROI.md
drwxr-xr-x 2 spencer spencer    41 Jun 29 16:08 sessions
drwxr-xr-x 3 spencer spencer    27 Jun 29 16:08 worktrees

```


## assistant

> 💭 So the setup is that `infra-beehive` is a coordination repo with a worktree, and it references the actual beehive Go source code as a git submodule at `submodules/beehive/repo/`, but that submodule directory is currently empty and hasn't been checked out yet.
> 
> The protocol in AGENTS.md says I should sync the submodule's tracked branch first using commands like `beehive submodule sync`, but I'm realizing there's no `beehive` CLI available and no `scripts/` directory in the repo to do this. I'm trying to figure out whether I should be working inside the worktree and syncing the submodule there, or if there's a different approach I'm missing. I should check whether the beehive submodule is already initialized and accessible. Let me verify the submodule status, see if there's a cached version in `.git/modules`, and confirm I have network access to fetch it from GitHub if needed.

Now I understand the structure: the actual beehive Go source is a nested submodule at `submodules/beehive/repo` (→ `git@github.com:spencerharmon/beehive.git`), currently not checked out. Let me check submodule state and whether the source is fetchable.

**🔧 bash** `cd /home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement && echo "=== submodule status ===" && git submodule status 2>&1 && echo "=== .git/modules cache (outer) ===" && ls -la /home/spencer/git-repos/spencerharmon/infra-beehive/.git/modules/ 2>&1 && echo "=== find any .go files anywhere in tree ===" && find /home/spencer/git-repos/spencerharmon/infra-beehive -name '*.go' 2>/dev/null | head -20`

```
=== submodule status ===
fatal: no submodule mapping found in .gitmodules for path 'submodules/beehive/worktrees/bee-bootstrap'
-082d38cbd1f52f07b154ebe82b57c2899afe7207 submodules/beehive/repo

```


## assistant

> 💭 The submodule at `submodules/beehive/repo` is uninitialized and pointing to commit `082d38c`, while there's a stale gitlink causing issues in the worktrees directory. I'm checking the git modules configuration and network connectivity to understand what's happening with that commit.

**🔧 bash** `cd /home/spencer/git-repos/spencerharmon/infra-beehive && echo "=== .git/modules ===" && find .git/modules -maxdepth 3 -type d 2>/dev/null | head -20; echo "=== any go files in whole tree ===" && find . -name '*.go' 2>/dev/null | head; echo "=== test github ssh (timeout 15s) ===" && timeout 15 git ls-remote git@github.com:spencerharmon/beehive.git 2>&1 | head -10`

```
=== .git/modules ===
.git/modules
.git/modules/submodules
.git/modules/submodules/beehive
.git/modules/submodules/beehive/repo
=== any go files in whole tree ===
./submodules/beehive/worktrees/bee-bootstrap/cmd/beehive/cmd_basic.go
./submodules/beehive/worktrees/bee-bootstrap/cmd/beehive/cmd_secret.go
./submodules/beehive/worktrees/bee-bootstrap/cmd/beehive/cmd_submodule.go
./submodules/beehive/worktrees/bee-bootstrap/cmd/beehive/cmd_worktree.go
./submodules/beehive/worktrees/bee-bootstrap/cmd/beehive/main.go
./submodules/beehive/worktrees/bee-bootstrap/cmd/beehived/main.go
./submodules/beehive/worktrees/bee-bootstrap/cmd/honeybee/main.go
./submodules/beehive/worktrees/bee-bootstrap/internal/claim/claim.go
./submodules/beehive/worktrees/bee-bootstrap/internal/claim/claim_test.go
./submodules/beehive/worktrees/bee-bootstrap/internal/config/config.go
=== test github ssh (timeout 15s) ===
082d38cbd1f52f07b154ebe82b57c2899afe7207	HEAD
082d38cbd1f52f07b154ebe82b57c2899afe7207	refs/heads/main

```


## assistant

> 💭 So the submodule cache exists at `.git/modules/submodules/beehive/repo`, and the actual Go source files are checked out in a different worktree under `submodules/beehive/worktrees/bee-bootstrap/` — meaning `bee-bootstrap` is the one that pulled down the submodule source. Network connectivity is confirmed working since I can reach the remote repository and see the latest commit hash. The source code in the `bee-bootstrap` worktree is at the root level (cmd/, internal/, etc.), which means it's a worktree of the beehive source repo itself, not the outer repo. But my `bee-links-graph-enforcement` worktree is a worktree of the outer repo with .gitmodules and submodules. I need to understand how the previous setup worked and check the git worktree configuration for the submodule to figure out the right structure.

Key findings: network works (remote tip = `082d38c` = recorded pointer), and the beehive Go source is checked out in the sibling `bee-bootstrap` worktree. Let me understand both worktree structures.

**🔧 bash** `cd /home/spencer/git-repos/spencerharmon/infra-beehive && echo "=== bee-bootstrap .git ===" && cat submodules/beehive/worktrees/bee-bootstrap/.git 2>&1 && echo "" && echo "=== bee-bootstrap top-level ===" && ls -la submodules/beehive/worktrees/bee-bootstrap/ && echo "=== git worktree list (submodule module) ===" && git -C .git/modules/submodules/beehive/repo worktree list 2>&1`

```
=== bee-bootstrap .git ===
gitdir: /home/spencer/git-repos/spencerharmon/infra-beehive/.git/modules/submodules/beehive/repo/worktrees/bee-bootstrap

=== bee-bootstrap top-level ===
total 56
drwxr-xr-x  9 spencer spencer  4096 Jun 29 12:50 .
drwxr-xr-x  4 spencer spencer    62 Jun 29 16:08 ..
drwxr-xr-x  5 spencer spencer    53 Jun 29 13:14 cmd
-rw-r--r--  1 spencer spencer  1134 Jun 29 12:50 CONTRIBUTING.md
drwxr-xr-x  2 spencer spencer  4096 Jun 29 12:50 docs
-rw-r--r--  1 spencer spencer   121 Jun 29 12:50 .git
drwxr-xr-x  3 spencer spencer    23 Jun 29 12:50 .github
-rw-r--r--  1 spencer spencer    22 Jun 29 12:50 .gitignore
-rw-r--r--  1 spencer spencer   232 Jun 29 12:50 go.mod
-rw-r--r--  1 spencer spencer  1152 Jun 29 12:50 go.sum
-rw-r--r--  1 spencer spencer  6282 Jun 29 12:50 IMPLEMENTATION.org
drwxr-xr-x 12 spencer spencer   134 Jun 29 12:50 internal
drwxr-xr-x  2 spencer spencer    64 Jun 29 12:50 packaging
-rw-r--r--  1 spencer spencer 16693 Jun 29 12:50 plan.org
drwxr-xr-x  2 spencer spencer   134 Jun 29 12:50 prompts
drwxr-xr-x  2 spencer spencer   131 Jun 29 12:50 scripts
=== git worktree list (submodule module) ===
/home/spencer/git-repos/spencerharmon/infra-beehive/.git/modules/submodules/beehive/repo       082d38c [main]
/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap 082d38c [bee-bootstrap]

```


## assistant

> 💭 The beehive submodule has its git directory at `.git/modules/submodules/beehive/repo` with two worktrees set up — the main checkout and a separate `bee-bootstrap` worktree on its own branch. I'm trying to understand how honeybee interacts with this structure...
> 
> Actually, I'm noticing a problem: my `bee-links-graph-enforcement` worktree appears to be set up as an outer-repo worktree (with `.gitmodules` pointing to the outer structure), but `bee-bootstrap` is a worktree of the beehive source submodule itself. They were created differently, which suggests I might have set mine up wrong. I need to check the outer repo's worktree list and look at what AGENTS.md says about the canonical way to create these worktrees. So the code worktree at `submodules/beehive/worktrees/bee-links-graph-enforcement` should be a source worktree like bee-bootstrap, but it's currently set up as an outer-repo worktree instead. I need to understand how worktrees are properly created in this beehive system by examining the swarm logic, the worktree CLI command, and the scripts to figure out the right setup. Then I'll need to read through the core modules—select, links, plan, swarm, and claim—to understand what changes are needed for the task.

Now the structure is clear. The beehive **source** repo (`.git/modules/submodules/beehive/repo`) has worktrees under `submodules/beehive/worktrees/`. The sibling `bee-bootstrap` is a source worktree at tip `082d38c`. Let me read the core source files for my task and understand worktree conventions.

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/internal/select/select.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/internal/select/select.go</path>
<type>file</type>
<content>
1: // Package selectt performs deterministic, no-LLM task selection that always
2: // yields a workable task: weighted-random over submodules, ROI-reconcile as
3: // priority 0 (PLAN.md stamp vs ROI.md commit), bootstrap when PLAN absent, then
4: // GC > arbitration > review > main by priority, dependency-gated, cycle-skipped,
5: // NEEDS-HUMAN excluded. The package name avoids the "select" keyword.
6: package selectt
7: 
8: import (
9: 	"context"
10: 	"math/rand"
11: 	"os"
12: 	"path/filepath"
13: 	"strconv"
14: 	"strings"
15: 	"time"
16: 
17: 	"github.com/spencerharmon/beehive/internal/git"
18: 	"github.com/spencerharmon/beehive/internal/plan"
19: 	"github.com/spencerharmon/beehive/internal/repo"
20: )
21: 
22: // Kind names the work a selection yields.
23: type Kind string
24: 
25: const (
26: 	Reconcile Kind = "reconcile" // priority 0: ROI.md drifted from PLAN stamp
27: 	Bootstrap Kind = "bootstrap" // ROI present, PLAN absent
28: 	Work      Kind = "work"      // a concrete PLAN task
29: )
30: 
31: // Selection is the deterministic result handed to the swarm before launch.
32: type Selection struct {
33: 	Kind      Kind
34: 	Submodule repo.Submodule
35: 	Task      plan.Task // valid only when Kind == Work
36: 	DiffRange string    // <stamp>..<head> for reconcile, "" otherwise
37: }
38: 
39: // Selector picks a submodule and task. Rand makes results reproducible for tests
40: // while still random per-process; TTL drives GC detection.
41: type Selector struct {
42: 	Repo *repo.Repo
43: 	Git  *git.Repo // beehive repo root, for ROI commit lookup
44: 	Rand *rand.Rand
45: 	TTL  time.Duration
46: }
47: 
48: // Select walks weighted-random submodules and returns the first workable item.
49: // nil is returned only when no submodule has any workable task.
50: func (s *Selector) Select(ctx context.Context) (*Selection, error) {
51: 	subs, err := s.Repo.Submodules()
52: 	if err != nil {
53: 		return nil, err
54: 	}
55: 	order := s.weightedOrder(subs)
56: 	now := time.Now().UTC()
57: 	for _, sm := range order {
58: 		sel, err := s.fromSubmodule(ctx, sm, now)
59: 		if err != nil {
60: 			return nil, err
61: 		}
62: 		if sel != nil {
63: 			return sel, nil
64: 		}
65: 	}
66: 	return nil, nil
67: }
68: 
69: func (s *Selector) fromSubmodule(ctx context.Context, sm repo.Submodule, now time.Time) (*Selection, error) {
70: 	if sm.Dormant() {
71: 		return nil, nil
72: 	}
73: 	if sm.NeedsBootstrap() {
74: 		return &Selection{Kind: Bootstrap, Submodule: sm}, nil
75: 	}
76: 	rng, err := s.reconcileRange(ctx, sm)
77: 	if err != nil {
78: 		return nil, err
79: 	}
80: 	if rng != "" {
81: 		return &Selection{Kind: Reconcile, Submodule: sm, DiffRange: rng}, nil
82: 	}
83: 	b, err := os.ReadFile(sm.PlanPath())
84: 	if err != nil {
85: 		if os.IsNotExist(err) {
86: 			return nil, nil
87: 		}
88: 		return nil, err
89: 	}
90: 	pl, err := plan.Parse(string(b))
91: 	if err != nil {
92: 		return nil, err
93: 	}
94: 	cands := pl.Candidates(now, s.TTL)
95: 	if len(cands) == 0 {
96: 		return nil, nil
97: 	}
98: 	t := s.pickTask(cands)
99: 	return &Selection{Kind: Work, Submodule: sm, Task: t}, nil
100: }
101: 
102: // reconcileRange returns "<stamp>..<roiHead>" when ROI.md drifted, else "".
103: func (s *Selector) reconcileRange(ctx context.Context, sm repo.Submodule) (string, error) {
104: 	if _, err := os.Stat(sm.ROIPath()); err != nil {
105: 		return "", nil
106: 	}
107: 	roiPath := "submodules/" + sm.Name + "/" + repo.ROIFile
108: 	head, err := s.Git.LastCommit(ctx, roiPath)
109: 	if err != nil || head == "" {
110: 		return "", err
111: 	}
112: 	stamp, err := sm.ROIStamp()
113: 	if err != nil {
114: 		return "", err
115: 	}
116: 	if stamp == head || strings.HasPrefix(head, stamp) && stamp != "" {
117: 		return "", nil
118: 	}
119: 	from := stamp
120: 	if from == "" {
121: 		from = "ROOT"
122: 	}
123: 	return from + ".." + head, nil
124: }
125: 
126: // weightedOrder returns submodules shuffled, each repeated by its weight, so
127: // higher-weighted submodules are tried first on average. Deterministic per Rand.
128: func (s *Selector) weightedOrder(subs []repo.Submodule) []repo.Submodule {
129: 	pool := make([]repo.Submodule, 0, len(subs))
130: 	for _, sm := range subs {
131: 		w := s.weight(sm)
132: 		for i := 0; i < w; i++ {
133: 			pool = append(pool, sm)
134: 		}
135: 	}
136: 	s.Rand.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
137: 	seen := map[string]bool{}
138: 	out := make([]repo.Submodule, 0, len(subs))
139: 	for _, sm := range pool {
140: 		if !seen[sm.Name] {
141: 			seen[sm.Name] = true
142: 			out = append(out, sm)
143: 		}
144: 	}
145: 	return out
146: }
147: 
148: // weight reads submodules/<name>/weight (positive int), default 1.
149: func (s *Selector) weight(sm repo.Submodule) int {
150: 	b, err := os.ReadFile(filepath.Join(sm.Path, "weight"))
151: 	if err != nil {
152: 		return 1
153: 	}
154: 	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
155: 	if err != nil || n < 1 {
156: 		return 1
157: 	}
158: 	return n
159: }
160: 
161: // pickTask weighted-randomly chooses one candidate by Task.Weight.
162: func (s *Selector) pickTask(cands []plan.Task) plan.Task {
163: 	total := 0
164: 	for _, t := range cands {
165: 		w := t.Weight
166: 		if w < 1 {
167: 			w = 1
168: 		}
169: 		total += w
170: 	}
171: 	r := s.Rand.Intn(total)
172: 	for _, t := range cands {
173: 		w := t.Weight
174: 		if w < 1 {
175: 			w = 1
176: 		}
177: 		if r < w {
178: 			return t
179: 		}
180: 		r -= w
181: 	}
182: 	return cands[len(cands)-1]
183: }

(End of file - total 183 lines)
</content>
```

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/internal/links/links.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/internal/links/links.go</path>
<type>file</type>
<content>
1: // Package links models SUBMODULE-LINKS.yaml: declared links between submodules
2: // plus task-level dependency edges, and enforces an acyclic graph. A link lets a
3: // honeybee on either submodule reference and depend on the other's PLAN.md;
4: // stepwise cross-repo chains are sequenced by dependency tags. Every tag write
5: // runs a cycle check so a wait cycle is rejected. Deterministic; no LLM.
6: package links
7: 
8: import (
9: 	"fmt"
10: 	"os"
11: 	"sort"
12: 
13: 	"gopkg.in/yaml.v3"
14: )
15: 
16: // Links is the parsed SUBMODULE-LINKS.yaml: bidirectional submodule links and
17: // directed task dependencies (Edge: from depends on to).
18: type Links struct {
19: 	Submodules []string `yaml:"submodules,omitempty"` // linked submodule names
20: 	Deps       []Edge   `yaml:"deps,omitempty"`       // directed dependency edges
21: }
22: 
23: // Edge means From depends on To (To must complete before From).
24: type Edge struct {
25: 	From string `yaml:"from"`
26: 	To   string `yaml:"to"`
27: }
28: 
29: // Parse reads SUBMODULE-LINKS.yaml text. Empty input is an empty Links.
30: func Parse(b []byte) (*Links, error) {
31: 	l := &Links{}
32: 	if len(b) == 0 {
33: 		return l, nil
34: 	}
35: 	if err := yaml.Unmarshal(b, l); err != nil {
36: 		return nil, fmt.Errorf("links: parse: %w", err)
37: 	}
38: 	return l, nil
39: }
40: 
41: // Load reads a SUBMODULE-LINKS.yaml file; missing file is empty Links.
42: func Load(path string) (*Links, error) {
43: 	b, err := os.ReadFile(path)
44: 	if err != nil {
45: 		if os.IsNotExist(err) {
46: 			return &Links{}, nil
47: 		}
48: 		return nil, err
49: 	}
50: 	return Parse(b)
51: }
52: 
53: // Save writes the links file deterministically (sorted).
54: func (l *Links) Save(path string) error {
55: 	sort.Strings(l.Submodules)
56: 	sort.Slice(l.Deps, func(i, j int) bool {
57: 		if l.Deps[i].From != l.Deps[j].From {
58: 			return l.Deps[i].From < l.Deps[j].From
59: 		}
60: 		return l.Deps[i].To < l.Deps[j].To
61: 	})
62: 	b, err := yaml.Marshal(l)
63: 	if err != nil {
64: 		return err
65: 	}
66: 	return os.WriteFile(path, b, 0o644)
67: }
68: 
69: // LinkSubmodules records an undirected link between a and b (idempotent).
70: func (l *Links) LinkSubmodules(a, b string) {
71: 	add := func(s string) {
72: 		for _, x := range l.Submodules {
73: 			if x == s {
74: 				return
75: 			}
76: 		}
77: 		l.Submodules = append(l.Submodules, s)
78: 	}
79: 	add(a)
80: 	add(b)
81: }
82: 
83: // AddDep adds a from->to dependency, rejecting it if it creates a cycle.
84: func (l *Links) AddDep(from, to string) error {
85: 	if from == to {
86: 		return fmt.Errorf("links: self-dependency %q", from)
87: 	}
88: 	for _, e := range l.Deps {
89: 		if e.From == from && e.To == to {
90: 			return nil
91: 		}
92: 	}
93: 	l.Deps = append(l.Deps, Edge{From: from, To: to})
94: 	if c := cycle(l.Deps); c != nil {
95: 		l.Deps = l.Deps[:len(l.Deps)-1]
96: 		return fmt.Errorf("links: dependency %s->%s creates cycle: %v", from, to, c)
97: 	}
98: 	return nil
99: }
100: 
101: // HasCycle reports whether the dependency graph contains a wait cycle.
102: func (l *Links) HasCycle() bool { return cycle(l.Deps) != nil }
103: 
104: // cycle returns a node sequence forming a cycle, or nil if acyclic (DFS).
105: func cycle(edges []Edge) []string {
106: 	adj := map[string][]string{}
107: 	for _, e := range edges {
108: 		adj[e.From] = append(adj[e.From], e.To)
109: 	}
110: 	const (
111: 		white = 0
112: 		gray  = 1
113: 		black = 2
114: 	)
115: 	color := map[string]int{}
116: 	var path, found []string
117: 	var dfs func(string) bool
118: 	dfs = func(n string) bool {
119: 		color[n] = gray
120: 		path = append(path, n)
121: 		for _, m := range adj[n] {
122: 			if color[m] == gray {
123: 				found = append(append([]string{}, path...), m)
124: 				return true
125: 			}
126: 			if color[m] == white && dfs(m) {
127: 				return true
128: 			}
129: 		}
130: 		path = path[:len(path)-1]
131: 		color[n] = black
132: 		return false
133: 	}
134: 	nodes := make([]string, 0, len(adj))
135: 	for n := range adj {
136: 		nodes = append(nodes, n)
137: 	}
138: 	sort.Strings(nodes)
139: 	for _, n := range nodes {
140: 		if color[n] == white && dfs(n) {
141: 			return found
142: 		}
143: 	}
144: 	return nil
145: }

(End of file - total 145 lines)
</content>
```


## assistant

> 💭 I need to dig into the plan and repo packages to understand how Task, Candidates, Selectable, and Submodule work together, then trace through swarm.go for the commit flow and cmd_submodule.go to see how AddDep gets called.

Now let me read the `plan` package, `repo` package, and how links are currently used (CLI), plus the swarm commit path.

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/internal/plan/plan.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/internal/plan/plan.go</path>
<type>file</type>
<content>
1: // Package plan parses and rewrites PLAN.md: the per-submodule task list, its
2: // status state machine, ROI reconcile stamp, heartbeat timestamps, attempt
3: // counters, dependency tags, and TTL math. Deterministic; no LLM.
4: //
5: // PLAN.md format (line-oriented, stable round-trip):
6: //
7: //	<!-- Beehive-ROI: <sha> -->
8: //	# Plan
9: //
10: //	## <id> [<STATUS>] <!-- attempts=N deps=a,b heartbeat=<RFC3339> -->
11: //	free-form body lines...
12: //
13: // The ROI stamp is the first comment; tasks are H2 headers carrying a metadata
14: // comment. Body lines between headers belong to the preceding task.
15: package plan
16: 
17: import (
18: 	"bufio"
19: 	"fmt"
20: 	"regexp"
21: 	"strconv"
22: 	"strings"
23: 	"time"
24: )
25: 
26: // Status is a task state. The machine is:
27: //
28: //	TODO -> IN-PROGRESS -> NEEDS-REVIEW -> {DONE | NEEDS-ARBITRATION}
29: //	NEEDS-ARBITRATION -> {TODO | DONE}
30: //	rejections > limit -> NEEDS-HUMAN (terminal, frontend-only)
31: type Status string
32: 
33: const (
34: 	StatusTODO       Status = "TODO"
35: 	StatusInProgress Status = "IN-PROGRESS"
36: 	StatusReview     Status = "NEEDS-REVIEW"
37: 	StatusArb        Status = "NEEDS-ARBITRATION"
38: 	StatusDone       Status = "DONE"
39: 	StatusHuman      Status = "NEEDS-HUMAN"
40: )
41: 
42: var allStatuses = map[Status]bool{
43: 	StatusTODO: true, StatusInProgress: true, StatusReview: true,
44: 	StatusArb: true, StatusDone: true, StatusHuman: true,
45: }
46: 
47: // Task is one PLAN.md item.
48: type Task struct {
49: 	ID        string
50: 	Title     string
51: 	Status    Status
52: 	Attempts  int
53: 	Deps      []string
54: 	Weight    int       // selection weight, default 1
55: 	Heartbeat time.Time // zero when not IN-PROGRESS
56: 	Body      []string  // body lines verbatim, without trailing blank
57: }
58: 
59: // Plan is a parsed PLAN.md.
60: type Plan struct {
61: 	ROI    string // Beehive-ROI stamp sha, "" if none
62: 	Header []string
63: 	Tasks  []*Task
64: }
65: 
66: var (
67: 	stampRe  = regexp.MustCompile(`<!--\s*Beehive-ROI:\s*([0-9a-f]*)\s*-->`)
68: 	headerRe = regexp.MustCompile(`^##\s+(\S+)\s+\[([A-Z-]+)\](?:\s+<!--\s*(.*?)\s*-->)?\s*$`)
69: )
70: 
71: // Parse reads PLAN.md text.
72: func Parse(s string) (*Plan, error) {
73: 	p := &Plan{}
74: 	sc := bufio.NewScanner(strings.NewReader(s))
75: 	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
76: 	var cur *Task
77: 	for sc.Scan() {
78: 		line := sc.Text()
79: 		if cur == nil {
80: 			if m := stampRe.FindStringSubmatch(line); m != nil {
81: 				p.ROI = m[1]
82: 				p.Header = append(p.Header, line)
83: 				continue
84: 			}
85: 		}
86: 		if m := headerRe.FindStringSubmatch(line); m != nil {
87: 			t, err := parseHeader(m)
88: 			if err != nil {
89: 				return nil, err
90: 			}
91: 			cur = t
92: 			p.Tasks = append(p.Tasks, t)
93: 			continue
94: 		}
95: 		if cur == nil {
96: 			p.Header = append(p.Header, line)
97: 		} else {
98: 			cur.Body = append(cur.Body, line)
99: 		}
100: 	}
101: 	if err := sc.Err(); err != nil {
102: 		return nil, err
103: 	}
104: 	for _, t := range p.Tasks {
105: 		t.Body = trimTrailingBlank(t.Body)
106: 	}
107: 	p.Header = trimTrailingBlank(p.Header)
108: 	return p, nil
109: }
110: 
111: func parseHeader(m []string) (*Task, error) {
112: 	st := Status(m[2])
113: 	if !allStatuses[st] {
114: 		return nil, fmt.Errorf("plan: unknown status %q for task %s", m[2], m[1])
115: 	}
116: 	t := &Task{ID: m[1], Status: st}
117: 	for _, kv := range strings.Fields(m[3]) {
118: 		k, v, ok := strings.Cut(kv, "=")
119: 		if !ok {
120: 			continue
121: 		}
122: 		switch k {
123: 		case "attempts":
124: 			n, err := strconv.Atoi(v)
125: 			if err != nil {
126: 				return nil, fmt.Errorf("plan: bad attempts %q for %s", v, t.ID)
127: 			}
128: 			t.Attempts = n
129: 		case "deps":
130: 			if v != "" {
131: 				t.Deps = strings.Split(v, ",")
132: 			}
133: 		case "weight":
134: 			n, err := strconv.Atoi(v)
135: 			if err != nil {
136: 				return nil, fmt.Errorf("plan: bad weight %q for %s", v, t.ID)
137: 			}
138: 			t.Weight = n
139: 		case "heartbeat":
140: 			ts, err := time.Parse(time.RFC3339, v)
141: 			if err != nil {
142: 				return nil, fmt.Errorf("plan: bad heartbeat %q for %s", v, t.ID)
143: 			}
144: 			t.Heartbeat = ts
145: 		}
146: 	}
147: 	return t, nil
148: }
149: 
150: func trimTrailingBlank(ls []string) []string {
151: 	for len(ls) > 0 && strings.TrimSpace(ls[len(ls)-1]) == "" {
152: 		ls = ls[:len(ls)-1]
153: 	}
154: 	return ls
155: }
156: 
157: // String serializes a plan deterministically; Parse(p.String()) round-trips.
158: func (p *Plan) String() string {
159: 	var b strings.Builder
160: 	if len(p.Header) > 0 {
161: 		b.WriteString(strings.Join(p.Header, "\n"))
162: 		b.WriteString("\n")
163: 	}
164: 	for _, t := range p.Tasks {
165: 		b.WriteString("\n")
166: 		b.WriteString(t.header())
167: 		b.WriteString("\n")
168: 		if len(t.Body) > 0 {
169: 			b.WriteString(strings.Join(t.Body, "\n"))
170: 			b.WriteString("\n")
171: 		}
172: 	}
173: 	return b.String()
174: }
175: 
176: func (t *Task) header() string {
177: 	meta := fmt.Sprintf("attempts=%d deps=%s", t.Attempts, strings.Join(t.Deps, ","))
178: 	if t.Weight > 1 {
179: 		meta += fmt.Sprintf(" weight=%d", t.Weight)
180: 	}
181: 	if !t.Heartbeat.IsZero() {
182: 		meta += " heartbeat=" + t.Heartbeat.UTC().Format(time.RFC3339)
183: 	}
184: 	return fmt.Sprintf("## %s [%s] <!-- %s -->", t.ID, t.Status, meta)
185: }
186: 
187: // Stamp sets the Beehive-ROI sha, inserting the comment if absent.
188: func (p *Plan) Stamp(sha string) {
189: 	line := "<!-- Beehive-ROI: " + sha + " -->"
190: 	for i, h := range p.Header {
191: 		if stampRe.MatchString(h) {
192: 			p.Header[i] = line
193: 			p.ROI = sha
194: 			return
195: 		}
196: 	}
197: 	p.Header = append([]string{line}, p.Header...)
198: 	p.ROI = sha
199: }
200: 
201: // Task returns the task with id, or nil.
202: func (p *Plan) Task(id string) *Task {
203: 	for _, t := range p.Tasks {
204: 		if t.ID == id {
205: 			return t
206: 		}
207: 	}
208: 	return nil
209: }

(End of file - total 209 lines)
</content>
```

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/internal/plan/state.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/internal/plan/state.go</path>
<type>file</type>
<content>
1: package plan
2: 
3: import (
4: 	"fmt"
5: 	"time"
6: )
7: 
8: // transitions enumerates the legal status edges. NEEDS-HUMAN is terminal and
9: // only reachable via Reject overflow, never a direct transition.
10: var transitions = map[Status]map[Status]bool{
11: 	StatusTODO:       {StatusInProgress: true},
12: 	StatusInProgress: {StatusReview: true, StatusTODO: true},
13: 	StatusReview:     {StatusDone: true, StatusArb: true},
14: 	StatusArb:        {StatusTODO: true, StatusDone: true},
15: }
16: 
17: // CanTransition reports whether from->to is legal.
18: func CanTransition(from, to Status) bool { return transitions[from][to] }
19: 
20: // Transition moves a task to a new status, enforcing the machine. It clears the
21: // heartbeat on any terminal/non-in-progress state and stamps it on IN-PROGRESS.
22: func (t *Task) Transition(to Status, now time.Time) error {
23: 	if !CanTransition(t.Status, to) {
24: 		return fmt.Errorf("plan: illegal transition %s -> %s for %s", t.Status, to, t.ID)
25: 	}
26: 	t.Status = to
27: 	if to == StatusInProgress {
28: 		t.Heartbeat = now
29: 	} else {
30: 		t.Heartbeat = time.Time{}
31: 	}
32: 	return nil
33: }
34: 
35: // Heartbeat re-stamps an IN-PROGRESS task; error otherwise.
36: func (t *Task) HeartbeatNow(now time.Time) error {
37: 	if t.Status != StatusInProgress {
38: 		return fmt.Errorf("plan: heartbeat on non-in-progress task %s (%s)", t.ID, t.Status)
39: 	}
40: 	t.Heartbeat = now
41: 	return nil
42: }
43: 
44: // Stale reports whether an IN-PROGRESS task's heartbeat is older than ttl: a GC
45: // candidate. Non-in-progress tasks are never stale.
46: func (t *Task) Stale(now time.Time, ttl time.Duration) bool {
47: 	if t.Status != StatusInProgress || t.Heartbeat.IsZero() {
48: 		return false
49: 	}
50: 	return now.Sub(t.Heartbeat) > ttl
51: }
52: 
53: // Reject records a rejection: bumps attempts, and once attempts exceed limit the
54: // task goes NEEDS-HUMAN (no longer auto-recycled). Otherwise it returns to TODO.
55: // Valid from NEEDS-REVIEW or NEEDS-ARBITRATION.
56: func (t *Task) Reject(limit int, now time.Time) error {
57: 	if t.Status != StatusReview && t.Status != StatusArb {
58: 		return fmt.Errorf("plan: reject on non-reviewable task %s (%s)", t.ID, t.Status)
59: 	}
60: 	t.Attempts++
61: 	t.Heartbeat = time.Time{}
62: 	if t.Attempts > limit {
63: 		t.Status = StatusHuman
64: 	} else {
65: 		t.Status = StatusTODO
66: 	}
67: 	return nil
68: }
69: 
70: // Selectable reports whether a task can be auto-selected: not terminal, not
71: // NEEDS-HUMAN, and all deps DONE in p.
72: func (p *Plan) Selectable(t *Task) bool {
73: 	if t.Status == StatusDone || t.Status == StatusHuman {
74: 		return false
75: 	}
76: 	for _, d := range t.Deps {
77: 		dep := p.Task(d)
78: 		if dep == nil || dep.Status != StatusDone {
79: 			return false
80: 		}
81: 	}
82: 	return true
83: }

(End of file - total 83 lines)
</content>
```


## assistant

> 💭 I need to dig into how `Candidates` works in select.go and trace through its usage with `Selectable`, then look at how `AddDep` is being used in cmd_submodule.go and understand the swarm commit path and claim.go interactions.

Let me find `Candidates`, the `repo` package, and how links/AddDep are wired into the CLI and commit paths.

**🔧 bash** `cd /home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap && echo "=== Candidates def ===" && grep -rn "func.*Candidates" internal/ && echo "=== links usage ===" && grep -rn "links\." --include=*.go internal/ cmd/ | grep -v "_test.go" && echo "=== SUBMODULE-LINKS refs ===" && grep -rn "SUBMODULE-LINKS\|LinksFile\|links.Load" --include=*.go internal/ cmd/`

```
=== Candidates def ===
internal/plan/compat.go:23:func (p *Plan) Candidates(now time.Time, ttl time.Duration) []Task {
=== links usage ===
cmd/beehive/cmd_submodule.go:86:				l, err := links.Load(p)
=== SUBMODULE-LINKS refs ===
internal/links/links.go:1:// Package links models SUBMODULE-LINKS.yaml: declared links between submodules
internal/links/links.go:16:// Links is the parsed SUBMODULE-LINKS.yaml: bidirectional submodule links and
internal/links/links.go:29:// Parse reads SUBMODULE-LINKS.yaml text. Empty input is an empty Links.
internal/links/links.go:41:// Load reads a SUBMODULE-LINKS.yaml file; missing file is empty Links.
internal/links/links_test.go:59:func LinksName() string { return "SUBMODULE-LINKS.yaml" }
internal/repo/repo.go:19:	LinksFile   = "SUBMODULE-LINKS.yaml"
internal/web/web.go:282:	p := filepath.Join(s.repo.Root, repo.LinksFile)
cmd/beehive/cmd_submodule.go:85:				p := filepath.Join(root, "submodules", sm, repo.LinksFile)
cmd/beehive/cmd_submodule.go:86:				l, err := links.Load(p)

```


## assistant

> 💭 I need to examine several files to understand how submodules and dependencies are handled across the codebase—looking at the compatibility layer, repository structure, the submodule command implementation, and how submodule links are rendered in the web interface.

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/internal/plan/compat.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/internal/plan/compat.go</path>
<type>file</type>
<content>
1: package plan
2: 
3: import "time"
4: 
5: // Short status aliases for swarm/select/claim consumers.
6: const (
7: 	TODO        = StatusTODO
8: 	InProgress  = StatusInProgress
9: 	NeedsReview = StatusReview
10: 	NeedsArb    = StatusArb
11: 	Done        = StatusDone
12: 	NeedsHuman  = StatusHuman
13: )
14: 
15: // Find returns the task with id, nil if absent (alias of Task).
16: func (p *Plan) Find(id string) *Task { return p.Task(id) }
17: 
18: // ROIStamp returns the recorded ROI reconcile sha.
19: func (p *Plan) ROIStamp() string { return p.ROI }
20: 
21: // priorityTiers orders selectable types: GC stale IN-PROGRESS > arbitration >
22: // review > main (TODO). Candidates returns the highest non-empty tier's tasks.
23: func (p *Plan) Candidates(now time.Time, ttl time.Duration) []Task {
24: 	var gc, arb, rev, main []Task
25: 	for _, t := range p.Tasks {
26: 		switch {
27: 		case t.Stale(now, ttl):
28: 			gc = append(gc, *t)
29: 		case t.Status == StatusArb:
30: 			arb = append(arb, *t)
31: 		case t.Status == StatusReview:
32: 			rev = append(rev, *t)
33: 		case t.Status == StatusTODO && p.Selectable(t):
34: 			main = append(main, *t)
35: 		}
36: 	}
37: 	for _, tier := range [][]Task{gc, arb, rev, main} {
38: 		if len(tier) > 0 {
39: 			return tier
40: 		}
41: 	}
42: 	return nil
43: }

(End of file - total 43 lines)
</content>
```

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/internal/repo/repo.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/internal/repo/repo.go</path>
<type>file</type>
<content>
1: // Package repo models the beehive repo layout and derives all state from files.
2: // The frontend and honeybee read state through this package; nothing else owns truth.
3: package repo
4: 
5: import (
6: 	"os"
7: 	"path/filepath"
8: 	"regexp"
9: 	"sort"
10: )
11: 
12: // Layout names. ROI is human-owned and never written by honeybees.
13: const (
14: 	AgentsFile  = "AGENTS.md"
15: 	PlanFile    = "PLAN.md"
16: 	ROIFile     = "ROI.md"
17: 	InfraFile   = "INFRASTRUCTURE.md"
18: 	Artifacts   = "ARTIFACTS.md"
19: 	LinksFile   = "SUBMODULE-LINKS.yaml"
20: 	SecretsFile = "SECRETS.yaml.gpg"
21: )
22: 
23: // roiStamp matches the PLAN.md reconcile marker: <!-- Beehive-ROI: <sha> -->
24: var roiStamp = regexp.MustCompile(`Beehive-ROI:\s*([0-9a-f]+)`)
25: 
26: // Repo is a beehive repo rooted at Root.
27: type Repo struct{ Root string }
28: 
29: // Open returns a Repo if Root contains AGENTS.md and a submodules dir.
30: func Open(root string) (*Repo, error) {
31: 	if _, err := os.Stat(filepath.Join(root, AgentsFile)); err != nil {
32: 		return nil, err
33: 	}
34: 	return &Repo{Root: root}, nil
35: }
36: 
37: // Submodule is one tracked target repo with its beehive coordination files.
38: type Submodule struct {
39: 	Name string
40: 	Path string // submodules/<name>
41: }
42: 
43: // RepoDir is the tracked target checkout (worktree base).
44: func (s Submodule) RepoDir() string { return filepath.Join(s.Path, "repo") }
45: 
46: // PlanPath, ROIPath, WorktreesDir locate coordination files.
47: func (s Submodule) PlanPath() string     { return filepath.Join(s.Path, PlanFile) }
48: func (s Submodule) ROIPath() string      { return filepath.Join(s.Path, ROIFile) }
49: func (s Submodule) WorktreesDir() string { return filepath.Join(s.Path, "worktrees") }
50: 
51: // Submodules lists submodule dirs sorted by name.
52: func (r *Repo) Submodules() ([]Submodule, error) {
53: 	base := filepath.Join(r.Root, "submodules")
54: 	ents, err := os.ReadDir(base)
55: 	if err != nil {
56: 		if os.IsNotExist(err) {
57: 			return nil, nil
58: 		}
59: 		return nil, err
60: 	}
61: 	var out []Submodule
62: 	for _, e := range ents {
63: 		if !e.IsDir() {
64: 			continue
65: 		}
66: 		out = append(out, Submodule{Name: e.Name(), Path: filepath.Join(base, e.Name())})
67: 	}
68: 	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
69: 	return out, nil
70: }
71: 
72: // Dormant reports whether a submodule has no ROI.md and is never selected.
73: func (s Submodule) Dormant() bool {
74: 	_, err := os.Stat(filepath.Join(s.Path, ROIFile))
75: 	return os.IsNotExist(err)
76: }
77: 
78: // NeedsBootstrap reports ROI present but PLAN absent.
79: func (s Submodule) NeedsBootstrap() bool {
80: 	_, roiErr := os.Stat(filepath.Join(s.Path, ROIFile))
81: 	_, planErr := os.Stat(s.PlanPath())
82: 	return roiErr == nil && os.IsNotExist(planErr)
83: }
84: 
85: // ROIStamp reads the last-reconciled ROI commit from PLAN.md, "" if none.
86: func (s Submodule) ROIStamp() (string, error) {
87: 	b, err := os.ReadFile(s.PlanPath())
88: 	if err != nil {
89: 		if os.IsNotExist(err) {
90: 			return "", nil
91: 		}
92: 		return "", err
93: 	}
94: 	if m := roiStamp.FindSubmatch(b); m != nil {
95: 		return string(m[1]), nil
96: 	}
97: 	return "", nil
98: }

(End of file - total 98 lines)
</content>
```


## assistant

> 💭 I need to trace through the CLI link flow and understand how AddDep works, then examine the swarm commit path and claim.go to see where PLAN.md gets written and dep-tag commits are created, plus check the pre-commit hook in config/hook.go for the commit-path guard mentioned in the task.

Now let me read the CLI link command, the config/hook (pre-commit guard), and the claim/swarm commit paths to find where dep-tag commits happen.

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/cmd/beehive/cmd_submodule.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/cmd/beehive/cmd_submodule.go</path>
<type>file</type>
<content>
1: package main
2: 
3: import (
4: 	"fmt"
5: 	"os"
6: 	"path/filepath"
7: 	"strings"
8: 
9: 	"github.com/spencerharmon/beehive/internal/git"
10: 	"github.com/spencerharmon/beehive/internal/links"
11: 	"github.com/spencerharmon/beehive/internal/plan"
12: 	"github.com/spencerharmon/beehive/internal/repo"
13: 	"github.com/spf13/cobra"
14: )
15: 
16: // findRoot ascends from cwd until AGENTS.md is found.
17: func findRoot() (string, error) {
18: 	d, err := os.Getwd()
19: 	if err != nil {
20: 		return "", err
21: 	}
22: 	for {
23: 		if _, err := os.Stat(filepath.Join(d, repo.AgentsFile)); err == nil {
24: 			return d, nil
25: 		}
26: 		p := filepath.Dir(d)
27: 		if p == d {
28: 			return "", fmt.Errorf("not inside a beehive repo (no %s found)", repo.AgentsFile)
29: 		}
30: 		d = p
31: 	}
32: }
33: 
34: func submoduleCmd() *cobra.Command {
35: 	c := &cobra.Command{Use: "submodule", Short: "manage beehive submodules"}
36: 	c.AddCommand(submoduleAddCmd(), submoduleLinkCmd(), submodulePlanCmd())
37: 	return c
38: }
39: 
40: func submoduleAddCmd() *cobra.Command {
41: 	var name, branch string
42: 	c := &cobra.Command{
43: 		Use:   "add <repo-url>",
44: 		Short: "add a target repo as a tracked submodule (dormant until ROI.md exists)",
45: 		Args:  cobra.ExactArgs(1),
46: 		RunE: func(cmd *cobra.Command, args []string) error {
47: 			root, err := findRoot()
48: 			if err != nil {
49: 				return err
50: 			}
51: 			url := args[0]
52: 			if name == "" {
53: 				name = strings.TrimSuffix(filepath.Base(url), ".git")
54: 			}
55: 			subdir := filepath.Join(root, "submodules", name)
56: 			if err := os.MkdirAll(filepath.Join(subdir, "worktrees"), 0o755); err != nil {
57: 				return err
58: 			}
59: 			g := git.New(root)
60: 			rel := filepath.Join("submodules", name, "repo")
61: 			if _, err := g.Run(cmd.Context(), "submodule", "add", "-b", branch, url, rel); err != nil {
62: 				return err
63: 			}
64: 			fmt.Printf("added submodule %s tracking %s (dormant; author ROI.md to activate)\n", name, branch)
65: 			return nil
66: 		},
67: 	}
68: 	c.Flags().StringVar(&name, "name", "", "submodule name (default: repo basename)")
69: 	c.Flags().StringVar(&branch, "branch", "main", "tracked branch tip")
70: 	return c
71: }
72: 
73: func submoduleLinkCmd() *cobra.Command {
74: 	return &cobra.Command{
75: 		Use:   "link <submodule-a> <submodule-b>",
76: 		Short: "link two submodules so each plan may depend on the other",
77: 		Args:  cobra.ExactArgs(2),
78: 		RunE: func(_ *cobra.Command, args []string) error {
79: 			root, err := findRoot()
80: 			if err != nil {
81: 				return err
82: 			}
83: 			a, b := args[0], args[1]
84: 			for _, sm := range []string{a, b} {
85: 				p := filepath.Join(root, "submodules", sm, repo.LinksFile)
86: 				l, err := links.Load(p)
87: 				if err != nil {
88: 					return err
89: 				}
90: 				l.LinkSubmodules(a, b)
91: 				if err := l.Save(p); err != nil {
92: 					return err
93: 				}
94: 			}
95: 			fmt.Printf("linked %s <-> %s\n", a, b)
96: 			return nil
97: 		},
98: 	}
99: }
100: 
101: func submodulePlanCmd() *cobra.Command {
102: 	c := &cobra.Command{Use: "plan", Short: "submodule plan operations"}
103: 	c.AddCommand(&cobra.Command{
104: 		Use:   "rollback <submodule> <commit>",
105: 		Short: "restore a submodule's PLAN.md to an earlier commit",
106: 		Args:  cobra.ExactArgs(2),
107: 		RunE: func(cmd *cobra.Command, args []string) error {
108: 			root, err := findRoot()
109: 			if err != nil {
110: 				return err
111: 			}
112: 			planRel := filepath.Join("submodules", args[0], repo.PlanFile)
113: 			g := git.New(root)
114: 			if err := plan.Rollback(cmd.Context(), g, planRel, args[1]); err != nil {
115: 				return err
116: 			}
117: 			fmt.Printf("rolled %s back to %s (staged; commit to apply)\n", planRel, args[1])
118: 			return nil
119: 		},
120: 	})
121: 	return c
122: }

(End of file - total 122 lines)
</content>
```

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/internal/config/hook.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/internal/config/hook.go</path>
<type>file</type>
<content>
1: package config
2: 
3: import (
4: 	"fmt"
5: 	"os"
6: 	"path/filepath"
7: )
8: 
9: // roiHook rejects commits that touch ROI.md when authored by a honeybee identity.
10: // Honeybees export BEEHIVE_HONEYBEE=1; only the frontend (unset) may change ROI.md.
11: const roiHook = `#!/usr/bin/env sh
12: # beehive ROI-protect hook (installed by CLI). ROI.md is human-owned; honeybees
13: # must never change it. Enforced here for local commits; a server pre-receive
14: # mirrors this for pushes.
15: [ "${BEEHIVE_HONEYBEE:-0}" = "1" ] || exit 0
16: if git diff --cached --name-only | grep -E '(^|/)ROI\.md$' >/dev/null; then
17:   echo "beehive: honeybee identity may not modify ROI.md" >&2
18:   exit 1
19: fi
20: exit 0
21: `
22: 
23: // InstallROIHook writes the ROI-protect pre-commit hook into the repo's .git dir.
24: func InstallROIHook(repoRoot string) error {
25: 	dir := filepath.Join(repoRoot, ".git", "hooks")
26: 	if _, err := os.Stat(filepath.Join(repoRoot, ".git")); err != nil {
27: 		return fmt.Errorf("not a git repo: %s", repoRoot)
28: 	}
29: 	if err := os.MkdirAll(dir, 0o755); err != nil {
30: 		return err
31: 	}
32: 	p := filepath.Join(dir, "pre-commit")
33: 	return os.WriteFile(p, []byte(roiHook), 0o755)
34: }

(End of file - total 34 lines)
</content>
```


## assistant

> 💭 I'm mapping out the design for the `links-graph-enforcement` task, which needs two key integration points: first, loading the `SUBMODULE-LINKS.yaml` file during selection to check if linked submodule tasks are complete before allowing a dependency to proceed, and second, running cycle detection across the combined dependency graph including project-level edges to prevent circular references.
> 
> For the data model, I need to support qualified cross-submodule task IDs in the format `<submodule>:<taskid>` alongside same-submodule references, and extend the `Selectable` logic to resolve these external dependencies. Since `Selectable` only has access to the current plan, I'm thinking the cleanest approach is to keep that method as-is and push the cross-submodule gating logic into `select.go fromSubmodule`, where I have access to the repo and can load other submodules' plans as needed.
> 
> The key challenge is making the dependency resolution pluggable so that `pl.Candidates` can check both same-plan deps and cross-submodule deps without tight coupling. I'm sketching out a `DepResolver` interface that can abstract away the details of resolving a dependency ID to its status, whether it's in the current plan or a linked submodule.
> 
> Before committing to that design, I need to check how `Candidates` and `Selectable` are used throughout the codebase to avoid breaking existing callers. I should keep the current API intact and add new variants that accept a resolver.
> 
> I'm also realizing that qualified dep IDs like `beehive:foo` work fine with the existing comma-separated parsing — the colon doesn't conflict. When a dep contains a colon, I can split it to extract the submodule name and task ID, then verify that submodule is actually linked before treating it as a cross-submodule reference. If it's not linked or malformed, I need to decide whether to block selection or reject it outright.
> 
> For cycle detection, I need to combine edges from both the PLAN's task dependencies and the links declared in SUBMODULE-LINKS.yaml, then run `HasCycle()` over the combined graph. But detecting cycles that span multiple submodules gets complex — I'd need to load all plans to see the full picture. Let me focus on what's testable and aligns with the documented behavior: load the links, resolve cross-submodule dep status by checking if tasks are DONE, and skip any cyclic tasks during selection.
> 
> I'm thinking of adding a helper in the links package that builds a unified edge set from a plan's dependencies and merges it with the existing links edges, then exposes a cycle detection method that works over this combined graph. I'm thinking through where to place the cross-submodule dependency resolution and cycle detection so it stays testable and loosely coupled. I'll add a `Selectable` variant to plan that accepts a resolver function for external dependencies, expose cycle detection in links, and wire everything together in select.go by building a combined edge graph from both plan dependencies and linked submodules. For the commit-path guard, I should add a validation function that the plan write path calls rather than relying on a shell hook, since cycle detection needs to parse the actual plan and links data. I need to nail down the implementation strategy: qualified dep IDs in the format `<submodule>:<taskid>`, then update the plan package to handle them, build a cycle detector in the links package, and wire it into the select package's resolver. For the commit-path guard, I'll add a validation function that checks for cycles and either call it directly in the claim write path or expose it through a `beehive plan lint` subcommand with a pre-commit hook. Let me trace through claim.go to see where PLAN.md gets committed.

Let me read the claim and swarm code (the PLAN.md write/commit path), plus find all callers of `Selectable`/`Candidates` and check the git package API.

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/internal/claim/claim.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/internal/claim/claim.go</path>
<type>file</type>
<content>
1: // Package claim implements the commit-race claim protocol: mark a PLAN.md task
2: // IN-PROGRESS with a timestamp, commit to beehive main, then re-pull and assert
3: // our own timestamp won. Merge is not a lock; the re-verify is. Also: heartbeat
4: // re-stamp each turn, stale/TTL detection for GC, and the rejection counter.
5: package claim
6: 
7: import (
8: 	"context"
9: 	"errors"
10: 	"fmt"
11: 	"os"
12: 	"time"
13: 
14: 	"github.com/spencerharmon/beehive/internal/git"
15: 	"github.com/spencerharmon/beehive/internal/plan"
16: 	"github.com/spencerharmon/beehive/internal/repo"
17: )
18: 
19: // ErrLost is returned when re-verify finds another bee's stamp won the race.
20: var ErrLost = errors.New("claim: lost commit race")
21: 
22: // Claimer races to own a task in a submodule PLAN.md.
23: type Claimer struct {
24: 	Repo *repo.Repo
25: 	Sub  repo.Submodule
26: 	Git  *git.Repo // beehive repo root
27: 	TTL  time.Duration
28: 	Now  func() time.Time // injectable clock; defaults to time.Now
29: }
30: 
31: func (c *Claimer) now() time.Time {
32: 	if c.Now != nil {
33: 		return c.Now().UTC()
34: 	}
35: 	return time.Now().UTC()
36: }
37: 
38: func (c *Claimer) load() (*plan.Plan, error) {
39: 	b, err := os.ReadFile(c.Sub.PlanPath())
40: 	if err != nil {
41: 		return nil, err
42: 	}
43: 	return plan.Parse(string(b))
44: }
45: 
46: func (c *Claimer) save(p *plan.Plan) error {
47: 	return os.WriteFile(c.Sub.PlanPath(), []byte(p.String()), 0o644)
48: }
49: 
50: // stampMsg keeps the commit message linkable for the frontend.
51: func stampMsg(taskID, action string) string {
52: 	return fmt.Sprintf("plan: %s %s\n\nBeehive: %s plan", action, taskID, taskID)
53: }
54: 
55: // Claim marks taskID IN-PROGRESS with ts, commits to main, then re-verifies our
56: // stamp won. ErrLost means abandon and reselect. ts must be unique to this bee.
57: func (c *Claimer) Claim(ctx context.Context, taskID string, ts time.Time) error {
58: 	ts = ts.UTC().Truncate(time.Second)
59: 	p, err := c.load()
60: 	if err != nil {
61: 		return err
62: 	}
63: 	t := p.Find(taskID)
64: 	if t == nil {
65: 		return fmt.Errorf("claim: task %q absent", taskID)
66: 	}
67: 	t.Status = plan.InProgress
68: 	t.Heartbeat = ts
69: 	if err := c.save(p); err != nil {
70: 		return err
71: 	}
72: 	if err := c.Git.Commit(ctx, stampMsg(taskID, "claim")); err != nil {
73: 		return err
74: 	}
75: 	return c.verify(taskID, ts)
76: }
77: 
78: // verify re-reads PLAN.md (post-pull caller) and asserts our timestamp owns it.
79: func (c *Claimer) verify(taskID string, ts time.Time) error {
80: 	p, err := c.load()
81: 	if err != nil {
82: 		return err
83: 	}
84: 	t := p.Find(taskID)
85: 	if t == nil {
86: 		return ErrLost
87: 	}
88: 	if t.Status != plan.InProgress || !t.Heartbeat.Equal(ts.UTC().Truncate(time.Second)) {
89: 		return ErrLost
90: 	}
91: 	return nil
92: }
93: 
94: // Heartbeat re-stamps taskID to keep it from going stale, committing the bump.
95: func (c *Claimer) Heartbeat(ctx context.Context, taskID string, ts time.Time) error {
96: 	ts = ts.UTC().Truncate(time.Second)
97: 	p, err := c.load()
98: 	if err != nil {
99: 		return err
100: 	}
101: 	t := p.Find(taskID)
102: 	if t == nil {
103: 		return fmt.Errorf("heartbeat: task %q absent", taskID)
104: 	}
105: 	if t.Status != plan.InProgress {
106: 		return fmt.Errorf("heartbeat: task %q not in progress", taskID)
107: 	}
108: 	t.Heartbeat = ts
109: 	if err := c.save(p); err != nil {
110: 		return err
111: 	}
112: 	if err := c.Git.Commit(ctx, stampMsg(taskID, "heartbeat")); err != nil && err != git.ErrNothing {
113: 		return err
114: 	}
115: 	return c.verify(taskID, ts)
116: }
117: 
118: // Stale reports whether taskID's heartbeat exceeded the TTL (a GC candidate).
119: func (c *Claimer) Stale(taskID string) (bool, error) {
120: 	p, err := c.load()
121: 	if err != nil {
122: 		return false, err
123: 	}
124: 	t := p.Find(taskID)
125: 	if t == nil {
126: 		return false, fmt.Errorf("stale: task %q absent", taskID)
127: 	}
128: 	return t.Stale(c.now(), c.TTL), nil
129: }
130: 
131: // Reject increments the rejection counter and, past limit, sets NEEDS-HUMAN to
132: // break review/arbitration livelock; otherwise resets to TODO. Commits the change.
133: func (c *Claimer) Reject(ctx context.Context, taskID string, limit int) (plan.Status, error) {
134: 	p, err := c.load()
135: 	if err != nil {
136: 		return "", err
137: 	}
138: 	t := p.Find(taskID)
139: 	if t == nil {
140: 		return "", fmt.Errorf("reject: task %q absent", taskID)
141: 	}
142: 	t.Attempts++
143: 	t.Heartbeat = time.Time{}
144: 	if t.Attempts > limit {
145: 		t.Status = plan.NeedsHuman
146: 	} else {
147: 		t.Status = plan.TODO
148: 	}
149: 	if err := c.save(p); err != nil {
150: 		return "", err
151: 	}
152: 	if err := c.Git.Commit(ctx, stampMsg(taskID, "reject")); err != nil && err != git.ErrNothing {
153: 		return "", err
154: 	}
155: 	return t.Status, nil
156: }

(End of file - total 156 lines)
</content>
```

**🔧 bash** `cd /home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap && echo "=== Selectable/Candidates callers ===" && grep -rn "Selectable\|Candidates" --include=*.go internal/ cmd/ && echo "=== git package funcs ===" && grep -rn "^func " internal/git/git.go && echo "=== plan write/commit paths ===" && grep -rn "WriteFile\|os.WriteFile\|p.String()\|plan.Write\|Commit" --include=*.go internal/claim/ internal/swarm/ | grep -v _test.go`

```
=== Selectable/Candidates callers ===
internal/plan/compat.go:22:// review > main (TODO). Candidates returns the highest non-empty tier's tasks.
internal/plan/compat.go:23:func (p *Plan) Candidates(now time.Time, ttl time.Duration) []Task {
internal/plan/compat.go:33:		case t.Status == StatusTODO && p.Selectable(t):
internal/plan/plan_test.go:110:func TestSelectable(t *testing.T) {
internal/plan/plan_test.go:112:	if p.Selectable(p.Task("t2")) {
internal/plan/plan_test.go:116:	if !p.Selectable(p.Task("t2")) {
internal/plan/state.go:70:// Selectable reports whether a task can be auto-selected: not terminal, not
internal/plan/state.go:72:func (p *Plan) Selectable(t *Task) bool {
internal/select/select.go:94:	cands := pl.Candidates(now, s.TTL)
=== git package funcs ===
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
=== plan write/commit paths ===
internal/claim/claim.go:47:	return os.WriteFile(c.Sub.PlanPath(), []byte(p.String()), 0o644)
internal/claim/claim.go:72:	if err := c.Git.Commit(ctx, stampMsg(taskID, "claim")); err != nil {
internal/claim/claim.go:112:	if err := c.Git.Commit(ctx, stampMsg(taskID, "heartbeat")); err != nil && err != git.ErrNothing {
internal/claim/claim.go:132:// break review/arbitration livelock; otherwise resets to TODO. Commits the change.
internal/claim/claim.go:152:	if err := c.Git.Commit(ctx, stampMsg(taskID, "reject")); err != nil && err != git.ErrNothing {
internal/swarm/swarm.go:135:	head, err := r.Git.LastCommit(context.Background(), roiPath)

```


## assistant

> 💭 I need to trace through the git.go Commit function and swarm.go to see how the full commit path works and where honeybees write PLAN.md status transitions like NEEDS-REVIEW. I'm also checking the honeybee main and swarm Run functions to figure out when dep-tag commits happen. The key thing I'm realizing is that honeybees update PLAN.md through the claim package with operations like Claim, Heartbeat, and Reject, and possibly through direct edits when adding tasks or dependencies.
> 
> Since honeybees are LLMs that commit via raw git rather than necessarily going through claim.go, the robust approach is a pre-commit hook that validates the graph whenever a commit touches PLAN.md in any submodule. A shell hook can't easily run Go cycle detection, so the clean solution is to have the hook call a beehive subcommand like `beehive plan lint` that finds the repo root, parses each submodule's PLAN.md and links, builds the combined graph, and runs cycle detection to error if there's a cycle.
> 
> I'm structuring this by adding exported cycle detection helpers to the links package that can be reused in tests and in the claim write path, then building the validation logic that both the git hook and the claim operations can call. I'm weighing whether to create a new `depgraph` package or keep everything within the existing structure of plan, links, and select. Since the doc specifies those files without mentioning additional packages, I'll stick with that constraint and add the cycle detection and cross-submodule resolution directly to those modules — putting `Cycle` and `HasCycleWith` in links, and adding `SplitDep` and local edge-building methods to plan.
> 
> The key insight is that plan shouldn't import links to avoid coupling, so instead I'll define an `ExternalStatus` callback interface in plan that resolves cross-submodule dependencies, then extend `Selectable` and `Candidates` to accept that resolver plus a cyclic task set that the select package computes from the combined graph. This keeps plan focused on parsing and local logic while deferring cross-submodule gating to the select layer. I'm weighing whether to create a new `internal/depgraph` package to centralize the dependency graph logic, or keep things simpler by putting the cross-submodule resolution directly in `select.go` and the commit guard. Given the scope and test requirements, I'll go with the focused approach: add a `Cycle` function to `links.go` to detect cycles, and put a `SplitDep` helper in `internal/plan/deps.go` to break down dependencies, then wire these together in `select.go` and the guard CLI without introducing a new package.
> 
> I'm also deciding that `plan` should import `links` since there's no circular dependency risk—`links` only depends on stdlib, and `plan` can safely express its dependencies as `links.Edge` objects. This keeps the graph model centralized in `links` while letting `plan` produce properly typed edges.
> 
> Adding helper functions like `Qualify` to format cross-submodule task references...
> 
> Writing dependency resolution code... Completing the function to return candidate tasks organized by priority tier. Now I'm reconsidering whether cyclic tasks should be excluded from all selection tiers or just the TODO tier — a task already in progress or review shouldn't be blocked by dependency cycles since it never needed to be selectable in the first place. But to keep it simple and match the requirement that cyclic tasks aren't selected, I'll exclude them from all tiers by continuing early in the loop.
> 
> For detecting cyclic tasks, using a single cycle path from `links.Cycle` works for simple cases but misses multiple disjoint cycles. I need to compute all nodes participating in any cycle using strongly connected components instead — that way I can identify every node in a cycle deterministically and completely.
> 
> Adding a helper function to compute cyclic nodes... Implementing Tarjan's algorithm...
> 
> Now I'm thinking through the cross-submodule status resolution in select.go. After parsing the plan, I need to load the links file from each submodule's directory. The submodule path is already rooted relative to the repo, so I can construct the links file path by joining it with the LinksFile constant. I should probably add a LinksPath() helper method to the Submodule type, similar to how PlanPath() works, to keep the path construction consistent.
> 
> For combining the dependency edges, I need to merge the edges from the plan itself with the edges declared in the links file. The key question is how task IDs are qualified in the links file — they should use the same qualified format (submodule:task) as the plan edges so they can properly interconnect in the combined graph. I'll assume the links.Deps endpoints are already qualified for cross-submodule references, since that's the whole purpose of the links file.
> 
> To normalize them safely, I'm writing a function that qualifies any unqualified task IDs by prepending the owning submodule name, treating unqualified references as local to that submodule. This way all edges use a consistent qualified format.
> 
> For full cross-submodule cycle detection, I need to gather edges not just from this submodule's plan and links file, but also from each linked submodule's plan and links file. I'll iterate through the linked submodules, collect all their edges, and run cycle detection on the combined graph. I'll handle missing or unloadable submodule plans gracefully by skipping them.
> 
> For resolving task status across submodules, I'm setting up an external resolver function that can look up the status of a task in a linked submodule. I'm replacing the local `Candidates` call with `CandidatesExt` to handle cross-submodule dependencies and cyclic detection, but I need to keep `Candidates` for backward compatibility since existing tests rely on it. I can make `Candidates` delegate to `CandidatesExt` with nil extension and cyclic parameters, which preserves the local-only behavior those tests expect while avoiding code duplication.
> 
> For the cross-dependency case, I need to guard against nil extension in `SelectableExt` so it treats unresolved cross deps as false rather than panicking. Now I'm thinking about the commit-path guard: I should add a `beehive plan lint` subcommand that validates all submodules' plans for cycles in the combined graph, then integrate it into a pre-commit hook alongside the existing ROI protection check. So the real issue is that LLMs commit raw edits to PLAN.md with cyclic deps, which a Go validation in the claim package won't catch—I need the pre-commit hook to intercept it. I'll create a testable validation function that checks the entire graph for cycles, wire it into a `beehive plan lint` subcommand, and have the pre-commit hook call that. This keeps the logic in Go (testable and maintainable) while ensuring the hook catches LLM commits before they land.
> 
> The tricky part is package dependencies: if plan imports links to return link.Edge types, then links can't import plan back. I'll resolve this by keeping plan → links as the direction, then putting the cross-cutting graph validation logic in a new `internal/depgraph` package that orchestrates both plan and links, providing functions to build the full dependency graph and detect cycles.
> 
> Actually, the doc doesn't explicitly forbid a new package, but to stay minimal and respect the file list, I could inline the graph-building logic in select.go and have the CLI guard reuse it by importing select as a helper—that avoids duplication without adding a new package. I'm placing the edge and cycle helpers in `links`, dependency semantics in `plan`, and the orchestration logic (graph building, resolver) in `select`, which will also export a reusable `Validate` function for the commit guard. The CLI will call `select.Validate` via `beehive plan lint`, and rather than risk conflicts with the pre-receive hook task by modifying config/hook.go, I'll provide the guard as a standalone pre-commit hook installer alongside the lint command.

Let me read the git package API and find how hooks/install are wired, plus the swarm Run loop for context.

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/internal/git/git.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/internal/git/git.go</path>
<type>file</type>
<content>
1: // Package git is a thin exec wrapper around the git binary. Beehive shells out
2: // rather than linking go-git so submodule, gpg, and worktree behavior matches
3: // the system git exactly. All writes happen in worktrees, never the shared checkout.
4: package git
5: 
6: import (
7: 	"bytes"
8: 	"context"
9: 	"errors"
10: 	"fmt"
11: 	"os/exec"
12: 	"strings"
13: )
14: 
15: // ErrConflict is returned when a merge produces conflicts.
16: var ErrConflict = errors.New("git: merge conflict")
17: 
18: // Repo is a git working tree rooted at Dir.
19: type Repo struct{ Dir string }
20: 
21: // New returns a Repo rooted at dir.
22: func New(dir string) *Repo { return &Repo{Dir: dir} }
23: 
24: // Run executes git with args in the repo dir, returning trimmed stdout.
25: func (r *Repo) Run(ctx context.Context, args ...string) (string, error) {
26: 	var out, errb bytes.Buffer
27: 	cmd := exec.CommandContext(ctx, "git", args...)
28: 	cmd.Dir = r.Dir
29: 	cmd.Stdout = &out
30: 	cmd.Stderr = &errb
31: 	if err := cmd.Run(); err != nil {
32: 		return strings.TrimSpace(out.String()), fmt.Errorf("git %s: %w: %s",
33: 			strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
34: 	}
35: 	return strings.TrimSpace(out.String()), nil
36: }
37: 
38: // Head returns the short SHA of HEAD.
39: func (r *Repo) Head(ctx context.Context) (string, error) {
40: 	return r.Run(ctx, "rev-parse", "--short", "HEAD")
41: }
42: 
43: // CurrentBranch returns the checked-out branch name.
44: func (r *Repo) CurrentBranch(ctx context.Context) (string, error) {
45: 	return r.Run(ctx, "rev-parse", "--abbrev-ref", "HEAD")
46: }
47: 
48: // Commit stages everything and commits with msg. ErrNothing if tree clean.
49: var ErrNothing = errors.New("git: nothing to commit")
50: 
51: func (r *Repo) Commit(ctx context.Context, msg string) error {
52: 	if _, err := r.Run(ctx, "add", "-A"); err != nil {
53: 		return err
54: 	}
55: 	clean, err := r.Clean(ctx)
56: 	if err != nil {
57: 		return err
58: 	}
59: 	if clean {
60: 		return ErrNothing
61: 	}
62: 	_, err = r.Run(ctx, "commit", "-m", msg)
63: 	return err
64: }
65: 
66: // Clean reports whether the working tree has no staged or unstaged changes.
67: func (r *Repo) Clean(ctx context.Context) (bool, error) {
68: 	out, err := r.Run(ctx, "status", "--porcelain")
69: 	if err != nil {
70: 		return false, err
71: 	}
72: 	return out == "", nil
73: }
74: 
75: // Merge merges ref into the current branch. Returns ErrConflict on conflict.
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
106: // WorktreeRemove removes the worktree at dir, force-discarding changes.
107: func (r *Repo) WorktreeRemove(ctx context.Context, dir string) error {
108: 	_, err := r.Run(ctx, "worktree", "remove", "--force", dir)
109: 	return err
110: }

(End of file - total 110 lines)
</content>
```

**🔧 bash** `cd /home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap && echo "=== InstallROIHook callers ===" && grep -rn "InstallROIHook\|InstallHook\|hooks\b\|pre-commit" --include=*.go . | grep -v "_test.go" && echo "=== cmd/beehive commands ===" && ls cmd/beehive/ && echo "=== root cmd wiring ===" && grep -rn "AddCommand\|func.*Cmd()" cmd/beehive/main.go cmd/beehive/cmd_basic.go`

```
=== InstallROIHook callers ===
./cmd/beehive/cmd_basic.go:30:			_ = config.InstallROIHook(path)
./cmd/beehive/cmd_basic.go:41:		Short: "install the ROI-protect pre-commit hook",
./cmd/beehive/cmd_basic.go:44:			if err := config.InstallROIHook(args[0]); err != nil {
./internal/config/hook.go:23:// InstallROIHook writes the ROI-protect pre-commit hook into the repo's .git dir.
./internal/config/hook.go:24:func InstallROIHook(repoRoot string) error {
./internal/config/hook.go:25:	dir := filepath.Join(repoRoot, ".git", "hooks")
./internal/config/hook.go:32:	p := filepath.Join(dir, "pre-commit")
=== cmd/beehive commands ===
cmd_basic.go
cmd_secret.go
cmd_submodule.go
cmd_worktree.go
main.go
=== root cmd wiring ===
cmd/beehive/main.go:20:	root.AddCommand(
cmd/beehive/cmd_basic.go:11:func versionCmd() *cobra.Command {
cmd/beehive/cmd_basic.go:19:func initCmd() *cobra.Command {
cmd/beehive/cmd_basic.go:37:func hookCmd() *cobra.Command {
cmd/beehive/cmd_basic.go:39:	c.AddCommand(&cobra.Command{

```


## assistant

> 💭 I need to examine cmd_basic.go and main.go to see how commands are structured and how the initialization and hook system works, so I can integrate a `plan lint` command and set up the appropriate hooks.

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/cmd/beehive/cmd_basic.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/cmd/beehive/cmd_basic.go</path>
<type>file</type>
<content>
1: package main
2: 
3: import (
4: 	"fmt"
5: 
6: 	"github.com/spencerharmon/beehive/internal/config"
7: 	"github.com/spencerharmon/beehive/internal/repo"
8: 	"github.com/spf13/cobra"
9: )
10: 
11: func versionCmd() *cobra.Command {
12: 	return &cobra.Command{
13: 		Use:   "version",
14: 		Short: "print version",
15: 		Run:   func(*cobra.Command, []string) { fmt.Println("beehive dev") },
16: 	}
17: }
18: 
19: func initCmd() *cobra.Command {
20: 	return &cobra.Command{
21: 		Use:   "init <path>",
22: 		Short: "scaffold a beehive repo and install the ROI-protect hook",
23: 		Args:  cobra.ExactArgs(1),
24: 		RunE: func(_ *cobra.Command, args []string) error {
25: 			path := args[0]
26: 			if err := repo.Init(path); err != nil {
27: 				return err
28: 			}
29: 			// Hook install is best-effort: only if path is already a git repo.
30: 			_ = config.InstallROIHook(path)
31: 			fmt.Println("beehive repo at", path)
32: 			return nil
33: 		},
34: 	}
35: }
36: 
37: func hookCmd() *cobra.Command {
38: 	c := &cobra.Command{Use: "hook", Short: "git hook management"}
39: 	c.AddCommand(&cobra.Command{
40: 		Use:   "install <repo>",
41: 		Short: "install the ROI-protect pre-commit hook",
42: 		Args:  cobra.ExactArgs(1),
43: 		RunE: func(_ *cobra.Command, args []string) error {
44: 			if err := config.InstallROIHook(args[0]); err != nil {
45: 				return err
46: 			}
47: 			fmt.Println("ROI-protect hook installed")
48: 			return nil
49: 		},
50: 	})
51: 	return c
52: }

(End of file - total 52 lines)
</content>
```

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/cmd/beehive/main.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/cmd/beehive/main.go</path>
<type>file</type>
<content>
1: // Command beehive is the deterministic CLI: repo init, submodule add/link, plan
2: // rollback, secret add/update/edit, worktree add/rm, honeybee start, and ROI-hook
3: // install. No LLM; every command is plain git + file ops.
4: package main
5: 
6: import (
7: 	"fmt"
8: 	"os"
9: 
10: 	"github.com/spf13/cobra"
11: )
12: 
13: func main() {
14: 	root := &cobra.Command{
15: 		Use:           "beehive",
16: 		Short:         "beehive deterministic CLI",
17: 		SilenceUsage:  true,
18: 		SilenceErrors: true,
19: 	}
20: 	root.AddCommand(
21: 		initCmd(),
22: 		versionCmd(),
23: 		submoduleCmd(),
24: 		secretCmd(),
25: 		worktreeCmd(),
26: 		honeybeeCmd(),
27: 		hookCmd(),
28: 	)
29: 	if err := root.Execute(); err != nil {
30: 		fmt.Fprintln(os.Stderr, "beehive:", err)
31: 		os.Exit(1)
32: 	}
33: }

(End of file - total 33 lines)
</content>
```


## assistant

> 💭 I'm settling on extending the pre-commit hook to handle both the ROI check and dependency cycle linting, since they both operate at commit time and share the same pre-commit file. The approach is to have the hook invoke `beehive` alongside the existing ROI logic. I'm settling on modifying the pre-commit hook to include both the ROI protection and the dep-cycle check, keeping everything in a single hook file to avoid conflicts with the parallel `roi-pre-receive-hook` task. The dep-cycle validation will call `beehive plan lint` as a best-effort check, and I'll add a new command like `beehive plan lint --pre-commit` to handle the staged files efficiently. The working tree is what gets committed since honeybees use `git add -A`, so validating the on-disk PLAN.md files is the right approach. I'll add a top-level `beehive lint` command that validates the whole repo's plan graphs for cycles, keeping it simple and matching what the hook will call.
> 
> For the implementation, I'm putting the graph validation logic in the `select` package since it already handles dependency resolution per submodule, and this is a natural extension. The key is building a combined cross-submodule graph that includes both task dependencies within each plan and declared link dependencies between submodules, then checking for cycles across the entire union.
> 
> Building the shared graph helper... I should compute the global edges and cyclic nodes once in `Select` before looping through submodules, then pass them down to `fromSubmodule` to avoid redundant work. For the external resolver, I need to check if a cross-submodule dependency's target submodule is actually linked in the current submodule's SUBMODULE-LINKS.yaml file—if not linked, the dependency is invalid and should block selection. I'll build the resolver per submodule with knowledge of its linked set, so when resolving a dependency like `sub:task`, it first verifies `sub` is in the linked set before loading that submodule's plan. I need to normalize the dependency edges from the links file by qualifying local IDs with their owning submodule, then combine them with the plan's dependency edges. I'll document that bare IDs in links files belong to that file's submodule, and add a `LinksPath()` method to get the path. The plan package importing links shouldn't cause issues since links doesn't import plan back. So I can keep plan pure by putting the helper functions and extension methods there without importing links, then have select build the edges using those plan helpers. That way plan stays as the lowest layer with no internal dependencies, and select handles the edge construction from plan's public API.
> 
> Writing edge-building logic... Finishing the edge loading and then I need to pass the cyclic nodes into fromSubmodule so it can detect circular dependencies. I'm also adding an externalStatus method that resolves task statuses across submodules by loading their plans on demand, checking if they're linked, and returning the status if found.
> 
> Now I'm defining the dependency resolution logic in the plan package, starting with helper functions to qualify cross-submodule references and parse dependency tokens to distinguish between local and cross-submodule dependencies. I'm also setting up a DepStatus callback type that resolves external task statuses, where returning false means the dependency is unresolved and blocks selection.
> 
> Implementing SelectableExt to check if a task can be auto-selected by verifying it's not terminal, not caught in a wait cycle, and all its dependencies are done—resolving local deps within the plan and cross-submodule deps through the external status callback. Then CandidatesExt mirrors the standard Candidates method but filters out cyclic tasks and uses the external resolver for cross-module dependencies...
> 
> Now I'm refactoring the original Candidates to delegate to CandidatesExt for backward compatibility, then checking the existing tests to make sure nothing breaks. After that I need to add cycle detection utilities using Tarjan's algorithm for finding strongly connected components, and finally add validation logic to the select package that builds the dependency graph and checks for cycles. Adding the lint command...
> 
> Now I'm weighing whether the pre-commit hook is the right place for the cycle guard, or if validation during the write path would be more reliable since the honeybee makes raw git commits rather than going through a claim interface.  The hook is the only place that'll catch direct commits, though it depends on the beehive binary being on PATH—which is a deployment concern, not a correctness one. I'll pair the hook with a unit test of the validation logic itself to ensure it works deterministically regardless of the environment. For the honeybee's workflow of editing files and committing with `git add -A && git commit`, the pre-commit hook will validate the working tree, which matches what gets committed. I should also verify that the existing test files follow the same patterns so I don't accidentally break anything.
> 
> Actually, I need to sort out the worktree situation first. The `bee-links-graph-enforcement` path is currently set up as an outer-repo worktree, but I need a source worktree to edit the beehive code itself. The protocol says to create a code worktree at `submodules/beehive/worktrees/bee-links-graph-enforcement`, but there's already an outer-repo worktree there, which is a conflict.
> 
> Let me check how worktrees are actually created by looking at the worktree command implementation and understanding whether the swarm creates them off the source repo or the outer repo, and whether I need to set up the source worktree myself. I need to figure out where the documentation should live—whether it's in the outer repo's docs or the source repo's docs. The Beehive stamp references a doc path in the source commit itself, so the doc should be under the source repo's `docs/` directory. Let me check the actual worktree layout, the bee-bootstrap source repo structure, and how the WorktreeAdd logic sets things up to clarify the intended organization.

Let me read the test files and worktree-management code, and inspect the actual worktree/branch state to understand where code changes must land.

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/internal/select/select_test.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/internal/select/select_test.go</path>
<type>file</type>
<content>
1: package selectt
2: 
3: import (
4: 	"context"
5: 	"math/rand"
6: 	"os"
7: 	"path/filepath"
8: 	"testing"
9: 	"time"
10: 
11: 	"github.com/spencerharmon/beehive/internal/git"
12: 	"github.com/spencerharmon/beehive/internal/repo"
13: )
14: 
15: func hive(t *testing.T) (*repo.Repo, *git.Repo, string) {
16: 	t.Helper()
17: 	root := t.TempDir()
18: 	ctx := context.Background()
19: 	g := git.New(root)
20: 	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
21: 		g.Run(ctx, a...)
22: 	}
23: 	repo.Init(root)
24: 	rp, _ := repo.Open(root)
25: 	return rp, g, root
26: }
27: 
28: func sub(root, name string, files map[string]string) {
29: 	d := filepath.Join(root, "submodules", name)
30: 	os.MkdirAll(d, 0o755)
31: 	for f, b := range files {
32: 		os.WriteFile(filepath.Join(d, f), []byte(b), 0o644)
33: 	}
34: }
35: 
36: func sel(root string, g *git.Repo) *Selector {
37: 	rp, _ := repo.Open(root)
38: 	return &Selector{Repo: rp, Git: g, Rand: rand.New(rand.NewSource(1)), TTL: time.Hour}
39: }
40: 
41: func TestSelectWork(t *testing.T) {
42: 	_, g, root := hive(t)
43: 	sub(root, "a", map[string]string{"ROI.md": "x", "PLAN.md": "## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"})
44: 	g.Commit(context.Background(), "seed")
45: 	head, _ := g.LastCommit(context.Background(), "submodules/a/ROI.md")
46: 	os.WriteFile(filepath.Join(root, "submodules/a/PLAN.md"), []byte("<!-- Beehive-ROI: "+head+" -->\n## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
47: 	g.Commit(context.Background(), "stamp")
48: 	s, err := sel(root, g).Select(context.Background())
49: 	if err != nil || s == nil {
50: 		t.Fatalf("sel %v %v", s, err)
51: 	}
52: 	if s.Kind != Work || s.Task.ID != "T1" {
53: 		t.Fatalf("got %+v", s)
54: 	}
55: }
56: 
57: func TestDormantSkipped(t *testing.T) {
58: 	_, g, root := hive(t)
59: 	sub(root, "a", map[string]string{}) // no ROI -> dormant
60: 	g.Commit(context.Background(), "seed")
61: 	s, _ := sel(root, g).Select(context.Background())
62: 	if s != nil {
63: 		t.Fatalf("dormant selected: %+v", s)
64: 	}
65: }
66: 
67: func TestBootstrap(t *testing.T) {
68: 	_, g, root := hive(t)
69: 	sub(root, "a", map[string]string{"ROI.md": "x"}) // ROI no PLAN
70: 	g.Commit(context.Background(), "seed")
71: 	s, _ := sel(root, g).Select(context.Background())
72: 	if s == nil || s.Kind != Bootstrap {
73: 		t.Fatalf("want bootstrap, got %+v", s)
74: 	}
75: }
76: 
77: func TestReconcilePriority0(t *testing.T) {
78: 	_, g, root := hive(t)
79: 	// PLAN stamped to an old sha but ROI committed later -> drift.
80: 	sub(root, "a", map[string]string{"ROI.md": "x", "PLAN.md": "<!-- Beehive-ROI: dead -->\n## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"})
81: 	g.Commit(context.Background(), "seed")
82: 	s, _ := sel(root, g).Select(context.Background())
83: 	if s == nil || s.Kind != Reconcile || s.DiffRange == "" {
84: 		t.Fatalf("want reconcile, got %+v", s)
85: 	}
86: }

(End of file - total 86 lines)
</content>
```

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/cmd/beehive/cmd_worktree.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/cmd/beehive/cmd_worktree.go</path>
<type>file</type>
<content>
1: package main
2: 
3: import (
4: 	"fmt"
5: 	"os"
6: 	"os/exec"
7: 	"path/filepath"
8: 
9: 	"github.com/spf13/cobra"
10: )
11: 
12: func worktreeCmd() *cobra.Command {
13: 	c := &cobra.Command{Use: "worktree", Short: "manage per-branch honeybee worktrees"}
14: 	run := func(cmd *cobra.Command, op, sm, br string) error {
15: 		root, err := findRoot()
16: 		if err != nil {
17: 			return err
18: 		}
19: 		script := filepath.Join(root, "scripts", "worktree.sh")
20: 		ex := exec.CommandContext(cmd.Context(), "sh", script, op, sm, br)
21: 		ex.Dir = root
22: 		ex.Stdout, ex.Stderr = os.Stdout, os.Stderr
23: 		return ex.Run()
24: 	}
25: 	c.AddCommand(&cobra.Command{
26: 		Use: "add <submodule> <branch>", Short: "create a worktree off the synced tip",
27: 		Args: cobra.ExactArgs(2),
28: 		RunE: func(cmd *cobra.Command, a []string) error { return run(cmd, "add", a[0], a[1]) },
29: 	}, &cobra.Command{
30: 		Use: "rm <submodule> <branch>", Short: "remove a worktree",
31: 		Args: cobra.ExactArgs(2),
32: 		RunE: func(cmd *cobra.Command, a []string) error { return run(cmd, "rm", a[0], a[1]) },
33: 	})
34: 	return c
35: }
36: 
37: func honeybeeCmd() *cobra.Command {
38: 	c := &cobra.Command{Use: "honeybee", Short: "honeybee process control"}
39: 	c.AddCommand(&cobra.Command{
40: 		Use:   "start <path>",
41: 		Short: "start a honeybee on a beehive repo",
42: 		Args:  cobra.ExactArgs(1),
43: 		RunE: func(cmd *cobra.Command, args []string) error {
44: 			ex := exec.CommandContext(cmd.Context(), "honeybee")
45: 			ex.Dir = args[0]
46: 			ex.Stdin, ex.Stdout, ex.Stderr = os.Stdin, os.Stdout, os.Stderr
47: 			if err := ex.Run(); err != nil {
48: 				return fmt.Errorf("honeybee: %w", err)
49: 			}
50: 			return nil
51: 		},
52: 	})
53: 	return c
54: }

(End of file - total 54 lines)
</content>
```


## assistant

> 💭 I need to look at the `scripts/worktree.sh` script in the source repo to understand how worktrees are actually created and which repository they're based on. I also want to check the git log and documentation for bee-bootstrap to get the full picture of what's happening here.
> 
> Beyond that, I should examine the outer repo's worktree list, look at the test files like `plan_test.go` and `links_test.go` to understand the testing patterns I should follow, and check what branch the current `bee-links-graph-enforcement` outer worktree is on.

Let me read the worktree script, test conventions, and inspect the actual branch/worktree state of both repos.

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/scripts/worktree.sh`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/scripts/worktree.sh</path>
<type>file</type>
<content>
1: #!/usr/bin/env sh
2: # beehive honeybee worktree management. All honeybee writes happen here, never repo/.
3: # Branches off the synced tracked-branch tip. Worktree deleted on DONE+merge.
4: # Usage: worktree.sh add <submodule> <branch> | rm <submodule> <branch>
5: set -eu
6: 
7: cmd="${1:?usage: worktree.sh add|rm <submodule> <branch>}"
8: sm="${2:?submodule}"
9: br="${3:?branch}"
10: repo="submodules/$sm/repo"
11: wt="submodules/$sm/worktrees/$br"
12: 
13: case "$cmd" in
14:   add)
15:     ./scripts/submodule-sync.sh "$sm"   # branch off fresh tip
16:     git -C "$repo" worktree add -b "$br" "../worktrees/$br" HEAD
17:     echo "$wt"
18:     ;;
19:   rm)
20:     git -C "$repo" worktree remove "../worktrees/$br" --force
21:     git -C "$repo" branch -D "$br" 2>/dev/null || true
22:     ;;
23:   *) echo "usage: worktree.sh add|rm <submodule> <branch>" >&2; exit 1 ;;
24: esac

(End of file - total 24 lines)
</content>
```

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/internal/plan/plan_test.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/internal/plan/plan_test.go</path>
<type>file</type>
<content>
1: package plan
2: 
3: import (
4: 	"os"
5: 	"path/filepath"
6: 	"testing"
7: 	"time"
8: )
9: 
10: const sample = `<!-- Beehive-ROI: abc123 -->
11: # Plan
12: 
13: ## t1 [TODO] <!-- attempts=0 deps= -->
14: do the first thing
15: 
16: ## t2 [IN-PROGRESS] <!-- attempts=1 deps=t1 heartbeat=2026-06-29T10:00:00Z -->
17: second, depends on t1
18: 
19: ## t3 [NEEDS-REVIEW] <!-- attempts=2 deps= -->
20: ready for review
21: `
22: 
23: func TestParseRoundTrip(t *testing.T) {
24: 	p, err := Parse(sample)
25: 	if err != nil {
26: 		t.Fatal(err)
27: 	}
28: 	if p.ROI != "abc123" {
29: 		t.Fatalf("roi=%q", p.ROI)
30: 	}
31: 	if len(p.Tasks) != 3 {
32: 		t.Fatalf("tasks=%d", len(p.Tasks))
33: 	}
34: 	t2 := p.Task("t2")
35: 	if t2.Status != StatusInProgress || t2.Attempts != 1 || len(t2.Deps) != 1 || t2.Deps[0] != "t1" {
36: 		t.Fatalf("t2 parsed wrong: %+v", t2)
37: 	}
38: 	if t2.Heartbeat.UTC().Format(time.RFC3339) != "2026-06-29T10:00:00Z" {
39: 		t.Fatalf("heartbeat=%v", t2.Heartbeat)
40: 	}
41: 	if got := p.String(); got != sample {
42: 		t.Fatalf("round trip mismatch:\n%q\nvs\n%q", got, sample)
43: 	}
44: }
45: 
46: func TestBadStatus(t *testing.T) {
47: 	if _, err := Parse("## x [BOGUS] <!-- attempts=0 deps= -->\n"); err == nil {
48: 		t.Fatal("want error on bad status")
49: 	}
50: }
51: 
52: func TestStateMachine(t *testing.T) {
53: 	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
54: 	tk := &Task{ID: "a", Status: StatusTODO}
55: 	if err := tk.Transition(StatusInProgress, now); err != nil {
56: 		t.Fatal(err)
57: 	}
58: 	if tk.Heartbeat.IsZero() {
59: 		t.Fatal("no heartbeat after in-progress")
60: 	}
61: 	if err := tk.Transition(StatusDone, now); err == nil {
62: 		t.Fatal("illegal in-progress->done allowed")
63: 	}
64: 	if err := tk.Transition(StatusReview, now); err != nil {
65: 		t.Fatal(err)
66: 	}
67: 	if !tk.Heartbeat.IsZero() {
68: 		t.Fatal("heartbeat not cleared on review")
69: 	}
70: 	if err := tk.Transition(StatusDone, now); err != nil {
71: 		t.Fatal(err)
72: 	}
73: }
74: 
75: func TestStaleHeartbeat(t *testing.T) {
76: 	base := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
77: 	tk := &Task{ID: "a", Status: StatusInProgress, Heartbeat: base}
78: 	ttl := time.Hour
79: 	if tk.Stale(base.Add(30*time.Minute), ttl) {
80: 		t.Fatal("fresh treated stale")
81: 	}
82: 	if !tk.Stale(base.Add(2*time.Hour), ttl) {
83: 		t.Fatal("old not stale")
84: 	}
85: 	done := &Task{ID: "b", Status: StatusDone}
86: 	if done.Stale(base.Add(99*time.Hour), ttl) {
87: 		t.Fatal("done never stale")
88: 	}
89: }
90: 
91: func TestRejectAttempts(t *testing.T) {
92: 	now := time.Now()
93: 	tk := &Task{ID: "a", Status: StatusReview, Attempts: 0}
94: 	for i := 0; i < 3; i++ {
95: 		tk.Status = StatusReview
96: 		if err := tk.Reject(3, now); err != nil {
97: 			t.Fatal(err)
98: 		}
99: 		if tk.Status != StatusTODO {
100: 			t.Fatalf("attempt %d status %s", i, tk.Status)
101: 		}
102: 	}
103: 	tk.Status = StatusReview
104: 	tk.Reject(3, now) // 4th > 3
105: 	if tk.Status != StatusHuman {
106: 		t.Fatalf("want NEEDS-HUMAN, got %s", tk.Status)
107: 	}
108: }
109: 
110: func TestSelectable(t *testing.T) {
111: 	p, _ := Parse(sample)
112: 	if p.Selectable(p.Task("t2")) {
113: 		t.Fatal("t2 selectable but dep t1 not done")
114: 	}
115: 	p.Task("t1").Status = StatusDone
116: 	if !p.Selectable(p.Task("t2")) {
117: 		t.Fatal("t2 should be selectable after dep done")
118: 	}
119: }
120: 
121: func TestGolden(t *testing.T) {
122: 	now := time.Date(2026, 6, 29, 10, 0, 0, 0, time.UTC)
123: 	p, _ := Parse(sample)
124: 	p.Task("t1").Transition(StatusInProgress, now)
125: 	p.Task("t3").Transition(StatusDone, now)
126: 	p.Stamp("def456")
127: 	got := p.String()
128: 	gp := filepath.Join("testdata", "transition.golden")
129: 	if os.Getenv("UPDATE_GOLDEN") == "1" {
130: 		os.WriteFile(gp, []byte(got), 0o644)
131: 	}
132: 	want, err := os.ReadFile(gp)
133: 	if err != nil {
134: 		t.Fatal(err)
135: 	}
136: 	if got != string(want) {
137: 		t.Fatalf("golden mismatch:\n%s", got)
138: 	}
139: }

(End of file - total 139 lines)
</content>
```


## assistant

> 💭 I'm seeing how the worktree creation works: it first syncs the submodule to its tracked tip, then creates a git worktree in the submodule's repository, branching off from that synced HEAD. So a code worktree is essentially a source repository worktree with its own branch.
> 
> The problem is that the existing `bee-links-graph-enforcement` worktree was created as an outer-repo worktree instead, and since the submodule isn't checked out in the outer worktree, the source worktree approach won't work directly. However, the source module is available at `.git/modules/submodules/beehive/repo` with its own worktrees, and bee-bootstrap is already a worktree there at the synced tip. I could create a new source worktree directly using git, pointing to that same synced commit, but the path `submodules/beehive/worktrees/bee-links-graph-enforcement` is already occupied by the outer-repo worktree, creating a conflict. I need to figure out whether the intended workflow here differs from the bee-bootstrap convention, or if there's a different way to resolve this path collision.
> 
> Let me check the outer repo's worktree list and examine how the runner set up both worktrees, and also look at bee-bootstrap's current branch state and whether it has any uncommitted changes that might indicate it's a completed bootstrap.

Let me inspect the outer repo worktree list, the source module worktrees, and bee-bootstrap's state to resolve where code changes must land.

**🔧 bash** `cd /home/spencer/git-repos/spencerharmon/infra-beehive && echo "=== OUTER worktree list ===" && git worktree list && echo "" && echo "=== SOURCE module worktree list ===" && git -C .git/modules/submodules/beehive/repo worktree list && echo "" && echo "=== bee-bootstrap branch/log ===" && git -C submodules/beehive/worktrees/bee-bootstrap log --oneline -6 && echo "=== bee-bootstrap status ===" && git -C submodules/beehive/worktrees/bee-bootstrap status -s | head && echo "=== bee-bootstrap docs ===" && ls submodules/beehive/worktrees/bee-bootstrap/docs/`

```
=== OUTER worktree list ===
/home/spencer/git-repos/spencerharmon/infra-beehive                                                                                    fdfceff [main]
/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318                                                          fdfceff [bee-1782767318]
/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement d954fdf [bee-links-graph-enforcement]
/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/edit-INFRASTRUCTURE-md-1782765563                                       3e56638 [edit-INFRASTRUCTURE-md-1782765563]
/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/edit-INFRASTRUCTURE-md-1782765744                                       3e56638 [edit-INFRASTRUCTURE-md-1782765744]
/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/edit-submodules-beehive-ROI-md-1782762882                               af3f909 [edit-submodules-beehive-ROI-md-1782762882]

=== SOURCE module worktree list ===
/home/spencer/git-repos/spencerharmon/infra-beehive/.git/modules/submodules/beehive/repo       082d38c [main]
/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap 082d38c [bee-bootstrap]

=== bee-bootstrap branch/log ===
082d38c fix: init writes full embedded AGENTS.md protocol, not placeholder
d5e2095 fix: build static binaries (CGO_ENABLED=0)
15c71de Merge branch 'cave/agent/implementer-mqytmztc-3'
9c9e5a8 Merge branch 'cave/agent/implementer-mqytmzt9-2'
eb9fb24 Merge branch 'cave/agent/implementer-mqytmzt6-1'
2745304 P2: wire honeybee runner
=== bee-bootstrap status ===
=== bee-bootstrap docs ===
cli.md
frontend-components.md
honeybee.md
install.md
opencode.md
README.md
RELEASE-NOTES-TEMPLATE.md
repo-layout.md
secrets.md

```


## assistant

> 💭 I'm mapping out the repository structure: there's an outer repo called `infra-beehive` with a main worktree and my current worktree at `bee-1782767318` where I'm handling coordination tasks, plus a submodule worktree for bee-links-graph-enforcement.
> 
> The source code lives in the beehive submodule, currently checked out only in the `bee-bootstrap` worktree at commit 082d38c. The key insight is that the bee-links-graph-enforcement worktree the runner created is an outer worktree, not a source worktree, so I need to sync the submodule there and figure out how to commit source changes back through the submodule structure.
> 
> The workflow would be: check out a source branch in the submodule, make edits and commits there, then update the outer branch's gitlink pointer to reference the new source commit. But this raises questions about push access to the source repo and whether the protocol expects me to push the source branch to origin for the reviewer to access.
> 
> Actually, looking at the directory structure more carefully, I notice that `bee-bootstrap` is registered as a source worktree while `bee-links-graph-enforcement` was created as an outer worktree by the runner—they're at the same level but belong to different repos. Given this inconsistency and the practical constraints, the cleanest approach is to work directly in the source repo and create a proper source worktree for the `bee-links-graph-enforcement` branch there.
> 
> The outer worktree at that path would need to be removed first, but since `submodules/beehive/worktrees/` appears to be gitignored by the outer repo (based on how `git status` showed it clean despite the worktrees existing there), I can safely replace it with a source worktree following the bee-bootstrap precedent. I'm weighing whether to initialize the source submodule inside that existing outer worktree versus creating a separate source worktree at a sibling path like bee-bootstrap does. The nested approach would work but feels overly deep, while the sibling approach is cleaner but creates a path collision with the outer worktree already occupying `submodules/beehive/worktrees/bee-links-graph-enforcement` — removing it might confuse the runner, so I need to think through the tradeoffs more carefully. So the actual deliverable is clearer now: I need a source repo commit on the `bee-links-graph-enforcement` branch with my code changes, a documentation file, and a Beehive stamp line in the commit message. Then I bump the gitlink pointer in the outer repo's `bee-links-graph-enforcement` branch to point to that source commit. Finally, I mark the task NEEDS-REVIEW on the outer main branch's PLAN.md.
> 
> Before diving into the git ceremony, I should focus on what I can actually verify: whether I can build and test the source code, and whether I have push access to both repositories. Let me start by checking Go availability and testing push access with a dry-run, then re-stamp my heartbeat on main since the protocol requires marking the task IN-PROGRESS immediately and verifying my stamp won.
> 
> I'm realizing there's some ambiguity about which branch to commit to—my current worktree is on `bee-1782767318`, but the protocol says to commit to main. Since the meta-orchestration (cave) is running me in this worktree and will merge my changes to main, I should commit to my current branch and let the meta-runner handle the merge.
> 
> Let me refocus on the most valuable deliverable: getting the Go source changes correct and tested. I'll create a source worktree for the `bee-links-graph-enforcement` branch off the pinned commit, run tests to verify the changes work, then commit with proper documentation. After that, I'll update the outer coordination by marking PLAN.md as NEEDS-REVIEW and bump the submodule pointer if I can push. The cleanest approach is to initialize the submodule inside the existing outer worktree rather than creating a separate path. The heartbeat re-stamp should happen now at the start of this turn to keep the task marked as in-progress, then I'll flip it to NEEDS-REVIEW once the work is done. Since the protocol requires re-stamping at the beginning of each turn, I'll do that first with a commit, complete the submodule work, and then commit the status change at the end. The current heartbeat timestamp is fresh enough, but I'll update it to the current UTC time to be safe and follow the protocol.
> 
> Now I'm clarifying the two-worktree coordination: the outer repo on branch `bee-1782767318` (main) should only get the PLAN.md status flips via commits to main, while the separate branch `bee-links-graph-enforcement` in the code worktree handles the actual source code changes and submodule pointer bumps. The source submodule commits go into the nested `.git/modules` directory, and then the outer repo's pointer gets bumped to reference the new submodule commit.
> 
> I'm considering whether to initialize the nested submodule in the code worktree or use a separate source worktree instead. The nested approach keeps everything localized in one place and naturally handles the pointer bump, so I'll stick with that. But I need to verify that the submodule initialization works and check whether it requires network access.
> 
> Since the module is already cloned locally at `.git/modules/submodules/beehive/repo`, the init should pull from the local store without hitting the network. Before proceeding, I'm going to run a batch of diagnostics: check the current time, verify the Go version, test a dry-run push to see if I can push, and inspect the current submodule state in the code worktree. I'll need to be careful with the push test since it might hang or prompt, so I'll configure SSH with batch mode and a timeout.

I now understand the full topology. Let me check tooling (go), current time for the heartbeat, and the code worktree's submodule state before I set up the source checkout.

**🔧 bash** `cd /home/spencer/git-repos/spencerharmon/infra-beehive && echo "=== date UTC ===" && date -u +%Y-%m-%dT%H:%M:%SZ && echo "=== go ===" && go version 2>&1 && echo "=== code worktree ($CW) submodule status ===" && git -C .worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement submodule status 2>&1 | head && echo "=== outer .gitignore (worktrees ignored?) ===" && grep -n "worktrees\|repo" .gitignore submodules/beehive/.gitignore 2>/dev/null; echo "=== outer branch bee-links log ===" && git log --oneline -3 bee-links-graph-enforcement 2>&1`

```
=== date UTC ===
2026-06-29T21:20:32Z
=== go ===
go version go1.26.3-X:nodwarf5 linux/amd64
=== code worktree () submodule status ===
fatal: no submodule mapping found in .gitmodules for path 'submodules/beehive/worktrees/bee-bootstrap'
-082d38cbd1f52f07b154ebe82b57c2899afe7207 submodules/beehive/repo
=== outer .gitignore (worktrees ignored?) ===
=== outer branch bee-links log ===
d954fdf plan: claim links-graph-enforcement
fe6ab9f session: bee-bootstrap-1782766865
52f61c0 plan: rebootstrap beehive PLAN.md from expanded ROI (32 tasks)

```


## assistant

> 💭 Looking at the current state: the heartbeat in PLAN is still fresh at about 12 minutes old, Go 1.26.3 is available, and the beehive submodule is at commit 082d38c but uninitialized—the bootstrap error seems to stem from stale nested worktree directories that need cleaning up.
> 
> The outer branch `bee-links-graph-enforcement` is behind main because it's a submodule patch branch forked at an earlier claim commit, which is expected. Now I'm puzzling through the `.gitignore` situation: the grep found nothing for `submodules/beehive/worktrees/`, yet `git status` showed clean output without those directories appearing as untracked. This suggests the worktrees might be getting special treatment by git—either because they're registered worktrees with `.git` files pointing elsewhere, or because the parent path itself is tracked in a way that suppresses their visibility.
> 
> Rather than dig deeper into git's internal handling of nested worktrees, I should just rely on `git status` checks after operations and be careful to commit only specific paths (the submodule pointer) rather than using `-A` to avoid accidentally staging nested worktree directories. The key is that when I commit on the outer branch, I'll explicitly add only what I intend, not everything.
> 
> Now I'm reconsidering whether the full complexity of bumping the outer pointer and managing nested submodule initialization is actually necessary. Let me think through the simplest path that still satisfies the protocol's intent and that I can fully verify with a build and test.
> 
> The plan is: first, update the heartbeat in PLAN.md to the current time and commit to main. Then, for the source worktree, instead of creating another standalone worktree at a separate path, I'll just initialize the submodule inside the existing code worktree at `$CW/submodules/beehive/repo` and branch it there — that's simpler and avoids the nesting complexity.
> 
> I'll edit and test the source in that location, commit on the `bee-links-graph-enforcement` branch in the source module, bump the outer pointer in the code worktree, then flip PLAN.md to NEEDS-REVIEW and commit to main. I'll push both branches if possible.
> 
> One thing I need to verify: when I initialize the submodule in the code worktree, it checks out at a detached HEAD at the specified commit, with the gitdir shared across worktrees. I can create a new branch `bee-links-graph-enforcement` in that submodule checkout, but I need to understand how that branch gets registered with the source module's worktree system, especially since the source module already has the `bee-bootstrap` worktree on its own branch.
> 
> Actually, I think the cleaner approach is to create a new worktree directly on the source module using `git worktree add` with the `-b` flag to create the branch, placing it at a distinct path like `/tmp/opencode/bee-links-src` or under `submodules/beehive/worktrees/` with a different name since the outer worktree already occupies the canonical location. The source commit will still be reachable through the shared module store, so the outer branch pointer bump will work fine. Once I commit the source changes and bump the outer pointer, the reviewer will merge the outer branch and get the full submodule patch.
> 
> I'm realizing the push ceremony is secondary to the core task—the Go code changes for selection cycle enforcement and commit guards are what matter most. I should focus on getting those changes correct and tested on a branch, then handle the coordination commits reasonably. Rather than over-engineering the push/pointer dance, I'll just set up the source worktree at a practical location and proceed with the actual code work.
> 
> The cleanest approach: initialize the submodule within the outer worktree I've already created, which will populate the shared gitdir at the expected path. Then I can create a branch in the submodule, make the source changes, and commit—the outer worktree's pointer naturally tracks the new SHA. Submodules handle branches fine, so this is straightforward. I'll start by checking out a branch in the submodule from the pinned commit and begin the actual code work. I'm reconsidering whether I can place the source worktree directly at the submodule path inside the outer worktree. If I use `git worktree add` to create a worktree at `<CW>/submodules/beehive/repo` on the `bee-links-graph-enforcement` branch, that satisfies the gitlink the outer repo expects there, and it's cleaner than initializing a submodule in a linked worktree. The question is whether git will complain about having both a gitlink and a worktree at the same location, but in practice this should work since the worktree fulfills what the gitlink points to.
> 
> The target directory is already empty, so `git worktree add` should accept it. I've analyzed this enough—time to execute and see what happens. Before I do the git plumbing, let me review the test file and go.mod to understand the conventions I need to follow when writing the actual code changes.

I have enough understanding of the topology. Now let me set up a TODO plan and read the remaining test conventions before implementing.

**🔧 todowrite** `{"todos":[{"content":"Re-stamp IN-PROGRESS heartbeat on main (cwd PLAN.md), commit","priority":"high","status":"in_progress"},{"content":"Create source worktree for branch bee-links-graph-enforcement …`

```
[
  {
    "content": "Re-stamp IN-PROGRESS heartbeat on main (cwd PLAN.md), commit",
    "status": "in_progress",
    "priority": "high"
  },
  {
    "content": "Create source worktree for branch bee-links-graph-enforcement off synced tip (082d38c)",
    "status": "pending",
    "priority": "high"
  },
  {
    "content": "links.go: add Cycle() + CyclicNodes() (SCC) exported helpers + tests",
    "status": "pending",
    "priority": "high"
  },
  {
    "content": "plan: add SplitDep/Qualify/SelectableExt/CandidatesExt (+ delegate Candidates) + tests",
    "status": "pending",
    "priority": "high"
  },
  {
    "content": "select.go: build combined graph, cyclic-skip, cross-submodule dep gating + tests",
    "status": "pending",
    "priority": "high"
  },
  {
    "content": "Commit-path guard: selectt.Validate + beehive lint cmd + pre-commit hook wiring + tests",
    "status": "pending",
    "priority": "high"
  },
  {
    "content": "go test ./... green in source worktree",
    "status": "pending",
    "priority": "high"
  },
  {
    "content": "Write change doc docs/bee-links-graph-enforcement-links-graph-enforcement.md",
    "status": "pending",
    "priority": "medium"
  },
  {
    "content": "Commit source (stamped) on branch; bump outer pointer on bee-links branch",
    "status": "pending",
    "priority": "medium"
  },
  {
    "content": "Flip PLAN.md task -> NEEDS-REVIEW on main, commit",
    "status": "pending",
    "priority": "high"
  }
]
```


## assistant

> 💭 I'll check the test file to follow the existing patterns, review the module dependencies, and then update the heartbeat timestamp.

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/internal/links/links_test.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/internal/links/links_test.go</path>
<type>file</type>
<content>
1: package links
2: 
3: import (
4: 	"path/filepath"
5: 	"testing"
6: )
7: 
8: func TestCycleDetection(t *testing.T) {
9: 	l := &Links{}
10: 	if err := l.AddDep("a", "b"); err != nil {
11: 		t.Fatal(err)
12: 	}
13: 	if err := l.AddDep("b", "c"); err != nil {
14: 		t.Fatal(err)
15: 	}
16: 	if err := l.AddDep("c", "a"); err == nil {
17: 		t.Fatal("cycle c->a not rejected")
18: 	}
19: 	if len(l.Deps) != 2 {
20: 		t.Fatalf("cyclic edge retained: %v", l.Deps)
21: 	}
22: 	if l.HasCycle() {
23: 		t.Fatal("graph reported cyclic after rejection")
24: 	}
25: 	if err := l.AddDep("a", "a"); err == nil {
26: 		t.Fatal("self-dep allowed")
27: 	}
28: }
29: 
30: func TestLinkSubmodulesIdempotent(t *testing.T) {
31: 	l := &Links{}
32: 	l.LinkSubmodules("x", "y")
33: 	l.LinkSubmodules("x", "y")
34: 	if len(l.Submodules) != 2 {
35: 		t.Fatalf("dupes: %v", l.Submodules)
36: 	}
37: }
38: 
39: func TestRoundTrip(t *testing.T) {
40: 	p := filepath.Join(t.TempDir(), LinksName())
41: 	l := &Links{}
42: 	l.LinkSubmodules("b", "a")
43: 	l.AddDep("a", "b")
44: 	if err := l.Save(p); err != nil {
45: 		t.Fatal(err)
46: 	}
47: 	got, err := Load(p)
48: 	if err != nil {
49: 		t.Fatal(err)
50: 	}
51: 	if len(got.Submodules) != 2 || got.Submodules[0] != "a" {
52: 		t.Fatalf("submodules=%v", got.Submodules)
53: 	}
54: 	if len(got.Deps) != 1 || got.Deps[0].From != "a" || got.Deps[0].To != "b" {
55: 		t.Fatalf("deps=%v", got.Deps)
56: 	}
57: }
58: 
59: func LinksName() string { return "SUBMODULE-LINKS.yaml" }

(End of file - total 59 lines)
</content>
```

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/go.mod`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/submodules/beehive/worktrees/bee-bootstrap/go.mod</path>
<type>file</type>
<content>
1: module github.com/spencerharmon/beehive
2: 
3: go 1.22
4: 
5: require (
6: 	github.com/spf13/cobra v1.10.2
7: 	gopkg.in/yaml.v3 v3.0.1
8: )
9: 
10: require (
11: 	github.com/inconshreveable/mousetrap v1.1.0 // indirect
12: 	github.com/spf13/pflag v1.0.9 // indirect
13: )

(End of file - total 13 lines)
</content>
```


## assistant

> 💭 I'm starting to execute the plan now. First, I need to update the heartbeat timestamp in the PLAN.md file for the links-graph-enforcement task from 2026-06-29T21:08:38Z to the current time 2026-06-29T21:20:32Z, then commit that change to the main branch.

All conventions understood. First, re-stamp my heartbeat on the coordination PLAN.md (protocol: start of every turn).

**🔧 bash** `date -u +%Y-%m-%dT%H:%M:%SZ`

```
2026-06-29T21:22:03Z

```


## assistant

**🔧 edit** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/PLAN.md`

```
Edit applied successfully.
```


## assistant

**🔧 bash** `cd /home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318 && git add submodules/beehive/PLAN.md && git commit -q -m "plan: heartbeat links-graph-enforcement" && git log --oneline -1`

```
3ba80bd plan: heartbeat links-graph-enforcement

```


## assistant

> 💭 I've re-stamped the heartbeat and satisfied the first protocol step. Now I need to work through the cleanup phases—garbage collection, arbitration, and review—before tackling the main task. Scanning the plan, I see `links-graph-enforcement` is the only task currently in progress besides mine.
> 
> Since there's nothing stale, no arbitration needed, and no reviews pending, I can skip straight to the main work. The remote branch is already synced at the current pointer, so that's a no-op. Now I'm setting up the source worktree by creating a new git worktree at the submodule location, checking out the tracked commit.

Heartbeat committed. No GC/arbitration/review tasks pending (only my task is IN-PROGRESS; all others TODO). Remote tip = `082d38c` = pointer, so sync is a no-op. Now let me create the source worktree for my branch off the synced tip.

**🔧 bash** `cd /home/spencer/git-repos/spencerharmon/infra-beehive && CW=.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement && MOD=/home/spencer/git-repos/spencerharmon/infra-beehive/.git/modules/submodules/beehive/repo && echo "removing empty placeholder repo dir" && rmdir "$CW/submodules/beehive/repo" 2>&1; echo "creating source worktree branch" && git -C "$MOD" worktree add -b bee-links-graph-enforcement "$(pwd)/$CW/submodules/beehive/repo" 082d38c 2>&1 && echo "=== verify ===" && git -C "$CW/submodules/beehive/repo" status -s && git -C "$CW/submodules/beehive/repo" log --oneline -1 && ls "$CW/submodules/beehive/repo" | head`

```
removing empty placeholder repo dir
creating source worktree branch
Preparing worktree (new branch 'bee-links-graph-enforcement')
HEAD is now at 082d38c fix: init writes full embedded AGENTS.md protocol, not placeholder
=== verify ===
082d38c fix: init writes full embedded AGENTS.md protocol, not placeholder
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

```


## assistant

> 💭 I've set up a worktree for the beehive repository on the `bee-links-graph-enforcement` branch at commit 082d38c, and the source code is now checked out. Before implementing the changes, I need to verify the build works as a baseline. I'm defining a path variable to reference the worktree location for easier access during development.
> 
> Now I'm running the build and test suite to ensure the checkout compiles and all tests pass before making any modifications. This will also pull down the necessary dependencies like cobra and yaml from the module cache.

Source worktree ready on branch `bee-links-graph-enforcement`. Let me verify a clean baseline build/test before changing anything.

**🔧 bash** `cd /home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo && echo "=== go env GOFLAGS/cache ===" && go env GOFLAGS GOMODCACHE | head && echo "=== baseline build ===" && CGO_ENABLED=0 go build ./... 2>&1 | head -30 && echo "BUILD_EXIT=$?" && echo "=== baseline test ===" && go test ./... 2>&1 | tail -30`

```
=== go env GOFLAGS/cache ===

/home/spencer/go/pkg/mod
=== baseline build ===
github.com/spencerharmon/beehive/prompts: write /tmp/go-build949573839/b080/importcfg: disk quota exceeded
github.com/spencerharmon/beehive/internal/git: write /tmp/go-build949573839/b073/importcfg: disk quota exceeded
github.com/spencerharmon/beehive/internal/config: write /tmp/go-build949573839/b063/importcfg: disk quota exceeded
github.com/spencerharmon/beehive/internal/links: write /tmp/go-build949573839/b076/importcfg: disk quota exceeded
BUILD_EXIT=0
=== baseline test ===
FAIL	github.com/spencerharmon/beehive/internal/links [setup failed]
# github.com/spencerharmon/beehive/internal/plan
write /tmp/go-build2960794240/b242/_testmain.go: disk quota exceeded
FAIL	github.com/spencerharmon/beehive/internal/plan [setup failed]
# github.com/spencerharmon/beehive/internal/repo
write /tmp/go-build2960794240/b244/_testmain.go: disk quota exceeded
FAIL	github.com/spencerharmon/beehive/internal/repo [setup failed]
# github.com/spencerharmon/beehive/internal/secrets
write /tmp/go-build2960794240/b246/_testmain.go: disk quota exceeded
FAIL	github.com/spencerharmon/beehive/internal/secrets [setup failed]
# github.com/spencerharmon/beehive/internal/select
write /tmp/go-build2960794240/b248/_testmain.go: disk quota exceeded
FAIL	github.com/spencerharmon/beehive/internal/select [setup failed]
# github.com/spencerharmon/beehive/internal/swarm
write /tmp/go-build2960794240/b250/_testmain.go: disk quota exceeded
FAIL	github.com/spencerharmon/beehive/internal/swarm [setup failed]
# github.com/spencerharmon/beehive/internal/web
write /tmp/go-build2960794240/b254/_testmain.go: disk quota exceeded
FAIL	github.com/spencerharmon/beehive/internal/web [setup failed]
github.com/spencerharmon/beehive/prompts: write /tmp/go-build2960794240/b080/importcfg: disk quota exceeded
github.com/spencerharmon/beehive/internal/git: write /tmp/go-build2960794240/b073/importcfg: disk quota exceeded
github.com/spencerharmon/beehive/internal/links: write /tmp/go-build2960794240/b076/importcfg: disk quota exceeded
github.com/spencerharmon/beehive/internal/config: write /tmp/go-build2960794240/b063/importcfg: disk quota exceeded
# runtime/cgo
cgo: write /tmp/cgo-gcc-input-3673258544.c: disk quota exceeded
FAIL	github.com/spencerharmon/beehive/cmd/beehive [build failed]
FAIL	github.com/spencerharmon/beehive/cmd/beehived [build failed]
FAIL	github.com/spencerharmon/beehive/cmd/honeybee [build failed]
FAIL	github.com/spencerharmon/beehive/prompts [build failed]
FAIL

```


## assistant

> 💭 The build is failing because /tmp has hit its disk quota, and the Go build cache is trying to write there. I need to redirect TMPDIR and GOTMPDIR to /tmp/opencode, which has available space, so the build can proceed without quota issues.

Build temp is hitting a disk quota on `/tmp`. Let me check available space and redirect Go's temp/cache.

**🔧 bash** `echo "=== df -h /tmp /home /tmp/opencode . ===" && df -h /tmp /home /tmp/opencode 2>&1; echo "=== quota ===" && quota -s 2>/dev/null | head; echo "=== go env GOCACHE GOTMPDIR ===" && go env GOCACHE GOTMPDIR; echo "=== du of GOCACHE ===" && du -sh "$(go env GOCACHE)" 2>/dev/null; echo "=== space in /tmp/opencode ===" && df -hP /tmp/opencode | tail -1`

```
=== df -h /tmp /home /tmp/opencode . ===
Filesystem             Size  Used Avail Use% Mounted on
tmpfs                   16G   13G  3.1G  81% /tmp
/dev/mapper/cryptroot  477G  308G  170G  65% /
tmpfs                   16G   13G  3.1G  81% /tmp
=== quota ===
=== go env GOCACHE GOTMPDIR ===
/home/spencer/.cache/go-build

=== du of GOCACHE ===
225M	/home/spencer/.cache/go-build
=== space in /tmp/opencode ===
tmpfs            16G   13G  3.1G  81% /tmp

```


## assistant

> 💭 Looking at the disk space situation, `/tmp` is a tmpfs with 16GB total and 3.1GB available, while the Go build cache is on the root filesystem which has plenty of space. The build failed with a "disk quota exceeded" error, which seems odd given the available space, and the thinking cuts off mid-explanation of what's happening.
> 
> The next thinking clarifies that this is likely a per-user quota on the tmpfs rather than just free space constraints. The plan is to redirect `GOTMPDIR` to a directory on the root filesystem (like inside the worktree at `./.gobuildtmp`) instead of letting it default to `/tmp`, since the GOCACHE already works fine on the root filesystem. This should bypass the tmpfs quota issue that's preventing the Go build from writing its temporary files.
> 
> Before running the full test suite, I should verify this fix works by building a single package first. I also need to remember that the project requires static binaries with `CGO_ENABLED=0`, so I should run tests with that flag set to avoid invoking cgo, which was hitting the quota error.

The `/tmp` tmpfs has a quota limit. Let me redirect Go's temp to the root filesystem (170G free) and verify with a smoke build.

**🔧 bash** `mkdir -p /home/spencer/.cache/gotmp && export GOTMPDIR=/home/spencer/.cache/gotmp TMPDIR=/home/spencer/.cache/gotmp CGO_ENABLED=0 && cd /home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo && echo "=== test links+plan+select+config (baseline) ===" && go test ./internal/links/... ./internal/plan/... ./internal/select/... ./internal/config/... 2>&1 | tail -20`

```
=== test links+plan+select+config (baseline) ===
ok  	github.com/spencerharmon/beehive/internal/links	0.003s
ok  	github.com/spencerharmon/beehive/internal/plan	0.003s
ok  	github.com/spencerharmon/beehive/internal/select	0.082s
ok  	github.com/spencerharmon/beehive/internal/config	0.003s

```


## assistant

> 💭 The baseline tests are passing after fixing the GOTMPDIR issue. I've completed the heartbeat and source worktree tasks, and now I'm moving on to implement the links functionality. I'm setting up the SRC path variable for easier access to the repository, then starting with the Cycle() method in links.go.
> 
> I need to implement CyclicNodes using Tarjan's strongly-connected components algorithm to find all nodes participating in cycles, ensuring deterministic ordering by visiting nodes and adjacency lists in sorted order. I'm building the adjacency map from the edges...
> 
> Writing Tarjan algorithm... Finishing the Tarjan algorithm to identify nodes in cycles, then checking if components have self-loops or multiple nodes to mark them as cyclic. Now I need to integrate this into the codebase—I'll add the new functions to links.go after HasCycle, then update plan.go to use the cyclic information without importing links, and finally adjust compat.go and select.go to work with the new cycle detection.

Baseline green. Now implementing. Start with `links` cycle helpers.

**🔧 edit** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/links/links.go`

```
Edit applied successfully.
```


## assistant

> 💭 I'm setting up the deps.go file in the internal/plan directory to handle dependencies.

Now the `plan` package: dependency qualification + external-resolver-aware selection (keeping `plan` free of `links`).

**🔧 write** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/plan/deps.go`

```
Wrote file successfully.
```


## assistant

Now make the existing `Candidates` delegate to `CandidatesExt` (local-only behavior preserved):

**🔧 edit** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/plan/compat.go`

```
Edit applied successfully.
```


## assistant

> 💭 I'm keeping the original `Selectable` for backward compatibility and test coverage in plan_test.go, while `CandidatesExt` now uses `SelectableExt` instead. Let me verify that `Selectable` isn't referenced elsewhere — it looks like `compat.go` doesn't use it, and `state.go` is where it's defined, so it's fine to maintain it as an exported type.
> 
> Now I need to add `LinksPath()` to repo.Submodule by editing repo.go.

Add a `LinksPath()` helper to `repo.Submodule` alongside the other path helpers:

**🔧 edit** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/repo/repo.go`

```
Edit applied successfully.
```


## assistant

> 💭 Now I'm working through the main changes needed in select.go: importing the links package, adding helper functions to build edges from plan and link dependencies, implementing a LoadEdges function to gather all submodule edges, adding validation logic to detect cycles, and then refactoring the Select function to compute the global edge graph once and pass it down to the submodule processing.
> 
> Updating the Select method to load edges and identify cyclic nodes before iterating through submodules, then modifying fromSubmodule to accept the cyclic set and build an external resolver for candidate filtering... Finishing the error handling for loading links, then building a resolver function that checks if a dependency is linked and retrieves its task status from the linked submodule's plan file. Now I'm creating a new graph.go file in the select package to handle edge loading and validation logic across all submodules, keeping the main select.go file focused.
> 
> Loading edges from both PLAN.md task dependencies and SUBMODULE-LINKS.yaml files, normalizing them to qualified "submodule:taskid" format so the selection cycle-skip and commit-time cycle guard can use the same combined dependency graph. The Validate function will check this graph for cycles... Now I need to update select.go by adding the links import, modifying the Select function to include edges and cycle detection, updating the fromSubmodule signature and implementation, and adding the externalStatus function. I'm checking that links is the only new import needed since filepath is already there. I'll surface errors from the links file since LoadEdges already does cycle detection earlier in Select, so failing loudly on a malformed links file is consistent with the overall approach.

Now wire the combined graph into selection. First add a `graph.go` to the `select` package:

**🔧 write** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/select/graph.go`

```
Wrote file successfully.
```


## assistant

Now update `select.go`: add the `links` import, compute the combined cyclic set in `Select`, and make `fromSubmodule` cycle-aware with cross-submodule dep resolution.

**🔧 edit** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/select/select.go`

```
Edit applied successfully.
```


## assistant

**🔧 edit** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/select/select.go`

```
Edit applied successfully.
```


## assistant

> 💭 I should build the modified packages first to identify any compilation issues before moving on to the CLI and hook implementation.

Let me compile the changed packages to catch errors early.

**🔧 bash** `export GOTMPDIR=/home/spencer/.cache/gotmp TMPDIR=/home/spencer/.cache/gotmp CGO_ENABLED=0 && cd /home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo && go build ./internal/... 2>&1 | head -30 && echo "BUILD done"`

```
BUILD done

```


## assistant

> 💭 Now I'm adding the lint command to validate submodule PLAN.md dependency graphs, creating the command handler and wiring it into the main CLI.
> 
> Then I need to extend the pre-commit hook to run the dep-cycle guard when PLAN.md files are staged, making sure it doesn't break the existing ROI behavior. The original hook had an early exit for non-honeybee commits, making it a no-op for the frontend. I'm restructuring it so the ROI check only runs for honeybees, but the dependency-cycle guard runs for everyone—since a cyclic PLAN.md is problematic regardless of who writes it. This is a behavioral change from the original, but it's more correct. I should verify whether hook_test.go exists yet (the PLAN.md lists it as part of the roi-pre-receive-hook task), and if not, I'll need to add tests for the dep-cycle guard logic, probably by testing the core validation function and asserting that the installed hook contains the guard.
> 
> Given the risk of modifying the ROI hook and the fragility of relying on a binary being on PATH, I'm weighing whether a validation in the plan write path would be cleaner and more testable. The protocol offers both options—a pre-commit guard or validation in the claim/plan write path. I think the best approach is to do both minimally: keep the pre-commit hook as the primary guard (covering raw LLM commits to git), and expose the validation logic through `selectt.Validate` and `beehive lint` for testability and coverage.
> 
> For testing, I'll verify that `selectt.Validate` correctly rejects cyclic dependency graphs and accepts acyclic ones, and I'll add a config test confirming the hook content invokes the guard. I'll also check if hook_test.go already exists and look for any existing references to the roiHook content to understand what I'm working with.

Internal packages compile. Now add the `beehive lint` command and wire the commit-time guard. Let me first check for existing hook tests/references.

**🔧 bash** `cd /home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo && echo "=== hook_test exists? ===" && ls internal/config/ && echo "=== references to roiHook / InstallROIHook / hook content ===" && grep -rn "roiHook\|InstallROIHook\|pre-commit\|may not modify ROI" --include=*.go . | grep -i test`

```
=== hook_test exists? ===
config.go
config_test.go
hook.go
hook_test.go
=== references to roiHook / InstallROIHook / hook content ===
./internal/config/hook_test.go:9:func TestInstallROIHook(t *testing.T) {
./internal/config/hook_test.go:11:	if err := InstallROIHook(root); err == nil {
./internal/config/hook_test.go:17:	if err := InstallROIHook(root); err != nil {
./internal/config/hook_test.go:20:	p := filepath.Join(root, ".git", "hooks", "pre-commit")

```


## user


## assistant

> 💭 I'm setting up the anchored summary for the `links-graph-enforcement` task in the beehive repo, keeping in mind the constraints around pure Go static binaries, comprehensive testing, and not modifying ROI.md while working autonomously.
> 
> I've already completed the core implementation work—the cycle detection logic in links.go, dependency qualification and splitting in plan/deps.go, and the graph validation layer in select/graph.go. The internal packages are building successfully. Now I need to wire in the `beehive lint` command with a pre-commit hook guard that enforces the dependency cycle checks, being careful since hook_test.go already exists and validates the pre-commit hook content. After that comes writing the full test suite for the cycle detection, dependency splitting, and cross-submodule gating logic.
> 
> I'm documenting the key paths and build environment for this work—the outer worktree at bee-1782767318, the source worktree for the links-graph-enforcement branch, and the reference checkout at bee-bootstrap, all pointing to the same commit 082d38c.
> 
> Setting up the build environment with GOTMPDIR and TMPDIR redirected to avoid the /tmp disk quota issue, then running the test suite across the internal packages. The design keeps plan and links decoupled by using a cyclic dependency map and DepStatus function instead of direct imports, while select imports links and uses Tarjan's algorithm for detecting strongly connected components. The commit guard extends the pre-commit hook to run beehive lint when PLAN.md is staged, and I've added Cycle, CyclicNodes, and HasCycleWith functions to links.go plus a new deps.go file in plan with dependency qualification and resolution logic.
> 
> Now I need to create the lint command, wire it into main, extend the hook logic, and add test coverage for the new modules—being careful not to break the existing hook_test.go assertion about the pre-commit hook path. The heartbeat stays at 2026-06-29T21:22:03Z and I'll document this work in the links-graph-enforcement markdown file.

## Goal
- Implement the `links-graph-enforcement` PLAN task: enforce cross-submodule dep gating + wait-cycle detection in beehive selection, and add a commit-time guard rejecting dep-tag commits that form a cycle.

## Constraints & Preferences
- Pure Go, static binaries (CGO_ENABLED=0); every fix ships tests; no weakened tests/swallowed errors/stub values; NEVER edit ROI.md.
- Act autonomously (no confirmation prompts).
- Doc caveat: Edge{From,To} = From depends on To; cross-submodule ids need qualified form `<submodule>:<taskid>`.

## Progress
### Done
- ROI reconcile not needed (ROI.md unchanged since PLAN stamp `af3f9092ab48fc2e624d39c67c41353ab4c9aa74`).
- Re-stamped heartbeat → `2026-06-29T21:22:03Z`, committed to main (`3ba80bd plan: heartbeat links-graph-enforcement`). No GC/arbitration/review tasks pending.
- Created source worktree on branch `bee-links-graph-enforcement` off synced tip `082d38c`. Baseline `go test` green.
- Code edits in SRC (build green for `./internal/...`):
  - `internal/links/links.go`: added `Cycle(edges)`, `CyclicNodes(edges)` (Tarjan SCC), `HasCycleWith(extra...)`.
  - `internal/plan/deps.go` (NEW): `Qualify`, `SplitDep`, `DepStatus` type, `SelectableExt`, `CandidatesExt`.
  - `internal/plan/compat.go`: `Candidates` now delegates to `CandidatesExt(..., "", nil, nil)`.
  - `internal/repo/repo.go`: added `Submodule.LinksPath()`.
  - `internal/select/graph.go` (NEW): `LoadEdges(r)`, `Validate(r)`, `planEdges`, `linkEdges`, `qualifyToken`.
  - `internal/select/select.go`: added `links` import; `Select` computes combined `cyclic` set; `fromSubmodule` now cycle-aware + `externalStatus` resolver (gated on SUBMODULE-LINKS submodules list).

### In Progress
- Commit-path guard wiring: about to add `beehive lint` cmd + extend pre-commit hook. Just found `internal/config/hook_test.go` EXISTS (`TestInstallROIHook`, asserts `.git/hooks/pre-commit` at line 20) — must inspect what it asserts before editing `roiHook`.

### Blocked
- (none)

## Key Decisions
- `plan` package must NOT import `links` (kept layering); `select` imports `links`. Edge-building lives in `select/graph.go`.
- Cross-submodule dep `<sm>:<task>` resolved only if `<sm>` is in the owning submodule's SUBMODULE-LINKS.yaml `submodules:` list; otherwise unresolved → blocks selection.
- Cyclic tasks excluded from ALL tiers in `CandidatesExt` (qualified-id `continue`).
- Kept original `Selectable` (state.go:72) for backward compat (plan_test TestSelectable).
- Commit guard plan: extend `roiHook` so ROI-protect runs only for honeybee (`BEEHIVE_HONEYBEE=1`) but dep-cycle check (`beehive lint`, when PLAN.md staged + binary on PATH) runs for everyone. `Validate` reports cycle via `links.Cycle`.

## Next Steps
1. Read `internal/config/hook_test.go` to avoid breaking content assertions.
2. Create `cmd/beehive/cmd_lint.go` (`findRoot`→`repo.Open`→`selectt.Validate`) and wire `lintCmd()` into `cmd/beehive/main.go`.
3. Extend `roiHook` in `internal/config/hook.go` with dep-cycle guard.
4. Write tests: links (CyclicNodes/Cycle), plan (SplitDep/SelectableExt/CandidatesExt), select (cross-submodule gating not-DONE blocks/DONE unblocks; cyclic task not selected; `Validate` rejects cycle), config hook content.
5. `go test ./...` green (with GOTMPDIR/TMPDIR/CGO env).
6. Write change doc `docs/bee-links-graph-enforcement-links-graph-enforcement.md`; commit source stamped `Beehive: links-graph-enforcement <doc-path>`.
7. Bump outer submodule pointer on branch `bee-links-graph-enforcement`; flip PLAN.md task → NEEDS-REVIEW on main (cwd), commit.

## Critical Context
- Build env REQUIRED (/tmp tmpfs hits "disk quota exceeded"): `export GOTMPDIR=/home/spencer/.cache/gotmp TMPDIR=/home/spencer/.cache/gotmp CGO_ENABLED=0`.
- Test cmd used: `go test ./internal/links/... ./internal/plan/... ./internal/select/... ./internal/config/...`.
- go1.26.3; module `github.com/spencerharmon/beehive` (go 1.22); deps cobra + yaml.v3.
- Topology: outer repo `infra-beehive`; beehive Go source is nested submodule `submodules/beehive/repo` → `git@github.com:spencerharmon/beehive.git`; remote main `082d38c` == recorded pointer (sync no-op). SSH read works; push not yet tested.
- `select` package name is `selectt`; import as `selectt "github.com/spencerharmon/beehive/internal/select"`.
- Outer branch `bee-links-graph-enforcement` is at `d954fdf` (claim); main at `3ba80bd`.
- Current PLAN task line: `## links-graph-enforcement [IN-PROGRESS] <!-- attempts=0 deps= weight=3 heartbeat=2026-06-29T21:22:03Z -->`.

## Relevant Files
- `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318`: cwd / outer worktree ($MAIN); edit `submodules/beehive/PLAN.md` (heartbeat/NEEDS-REVIEW), commit to main.
- `…/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo`: SRC source worktree (branch bee-links-graph-enforcement) — all code edits/tests here.
- `…/infra-beehive/.git/modules/submodules/beehive/repo`: shared source module gitdir.
- `submodules/beehive/worktrees/bee-bootstrap`: reference source checkout @082d38c (read-only reference).
- `submodules/beehive/docs/tasks/links-graph-enforcement.md`: task design doc.
- SRC `internal/{links/links.go, plan/deps.go, plan/compat.go, repo/repo.go, select/graph.go, select/select.go}`: changed.
- SRC `cmd/beehive/{main.go, cmd_basic.go, cmd_submodule.go}`, `internal/config/hook.go` + `hook_test.go`: to change/inspect for lint cmd + guard.
- SRC `internal/claim/claim.go`: PLAN write path (load/save/Commit via `git.Commit` add -A).


## user

Continue if you have next steps, or stop and ask for clarification if you are unsure how to proceed.


## assistant

> 💭 I'm getting oriented and need to check my current state—reviewing the plan, verifying my heartbeat status, and looking at the hook_test.go file I just found. Let me set up the environment and do some parallel reads to understand what I'm working with.

I'll re-orient first: re-stamp my heartbeat, verify state, and inspect the files I flagged. Let me start with parallel reads.

**🔧 bash** `date -u +%Y-%m-%dT%H:%M:%SZ && echo "---PLAN---" && git -C /home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318 log --oneline -5 2>/dev/null && echo "---BRANCH---" && git -C /home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318 branch --show-current`

```
2026-06-29T21:27:22Z
---PLAN---
3ba80bd plan: heartbeat links-graph-enforcement
fdfceff plan: heartbeat links-graph-enforcement
d954fdf plan: claim links-graph-enforcement
fe6ab9f session: bee-bootstrap-1782766865
52f61c0 plan: rebootstrap beehive PLAN.md from expanded ROI (32 tasks)
---BRANCH---
bee-1782767318

```

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/PLAN.md`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/PLAN.md</path>
<type>file</type>
<content>
1: <!-- Beehive-ROI: af3f9092ab48fc2e624d39c67c41353ab4c9aa74 -->
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
31: ## claim-repull-verify [TODO] <!-- attempts=0 deps=git-remote-ops weight=4 -->
32: Make the claim lock real. claim.go verify (claim.go:79) re-reads the LOCAL PLAN.md and never
33: re-pulls, so two bees both "win". After Commit in Claim (claim.go:57) and Heartbeat
34: (claim.go:95): pull beehive main, reload PLAN.md, verify our stamp owns it, ErrLost on
35: mismatch (abandon + reselect).
36: Files: internal/claim/claim.go, internal/claim/claim_test.go.
37: Doc: docs/tasks/claim-repull-verify.md
38: Accept: two-claimer race test yields exactly one winner; loser gets ErrLost.
39: 
40: ## claimer-reject-guard [TODO] <!-- attempts=0 deps= weight=3 -->
41: Claimer.Reject (claim.go:133) bumps Attempts unconditionally. Guard status first: only
42: NEEDS-REVIEW or NEEDS-ARBITRATION may be rejected (mirror plan.Task.Reject, state.go:56).
43: Reject from any other status must error and leave Attempts/Status untouched.
44: Files: internal/claim/claim.go, internal/claim/claim_test.go.
45: Doc: docs/tasks/claimer-reject-guard.md
46: Accept: reject on TODO/IN-PROGRESS/DONE errors; attempts unchanged; review/arb still work.
47: 
48: ## opencode-turn-poll [TODO] <!-- attempts=0 deps= weight=4 -->
49: opencode turn engine is fire-and-forget: ocSession.Prompt (opencode.go:49) returns on accept,
50: so the runner's completion check runs before the assistant acts; all turns burn in ms.
51: Poll the session (GET session messages/state) until the assistant turn is idle before
52: returning from a turn / running complete().
53: Files: internal/swarm/opencode.go, internal/swarm/swarm.go (Run loop), swarm_test.go.
54: Doc: docs/tasks/opencode-turn-poll.md
55: Accept: Prompt blocks until turn idle; fake-server test proves no premature completion.
56: 
57: ## worktree-sync-base [TODO] <!-- attempts=0 deps=git-remote-ops weight=4 -->
58: Worktrees branch off HEAD with no sync (swarm.go:78), so bees work stale code. Fetch +
59: hard-reset the tracked tip first, then branch off the synced tip. Wire
60: scripts/submodule-sync.sh into the runner (auto-advances the beehive pointer, no review).
61: Files: internal/swarm/swarm.go, scripts/submodule-sync.sh, swarm_test.go.
62: Doc: docs/tasks/worktree-sync-base.md
63: Accept: runner syncs tracked branch before WorktreeAdd; test asserts base == remote tip.
64: 
65: ## gc-worktree-reclaim [TODO] <!-- attempts=0 deps= weight=3 -->
66: GC orphans worktrees on cap: Run sets GCMarked but never removes the worktree at the
67: turn/wall cap (swarm.go:116) — only the DONE path (swarm.go:108) reclaims. Remove the
68: worktree (or record a GC marker) at the cap too, leaving the IN-PROGRESS heartbeat for GC.
69: Files: internal/swarm/swarm.go, swarm_test.go.
70: Doc: docs/tasks/gc-worktree-reclaim.md
71: Accept: cap path reclaims worktree/records marker; task stays IN-PROGRESS for stale GC.
72: 
73: ## reconcile-prefix-match [TODO] <!-- attempts=0 deps= weight=3 -->
74: Reconcile completion never fires and the diff base is bogus. swarm.go reconciled()
75: (swarm.go:133) does exact `stamp == head` but stamp is short and head is full sha. Compare
76: by prefix. select.go reconcileRange (select.go:121) uses a "ROOT" sentinel for the empty
77: base; replace with the git empty-tree sha 4b825dc642cb6eb9a060e54bf8d69288fbee4904.
78: Files: internal/swarm/swarm.go, internal/select/select.go, *_test.go.
79: Doc: docs/tasks/reconcile-prefix-match.md
80: Accept: short/full-sha prefix match fires once and clears; empty-base diff range is valid.
81: 
82: ## links-graph-enforcement [IN-PROGRESS] <!-- attempts=0 deps= weight=3 heartbeat=2026-06-29T21:22:03Z -->
83: Cross-submodule deps and wait-cycle detection are unused in selection. Resolve linked-
84: submodule deps and run links.HasCycle during candidate selection (select.go fromSubmodule /
85: plan.Candidates). The cycle check must also run on honeybee dep-tag commits (PLAN.md writes),
86: not only the CLI (links.AddDep).
87: Files: internal/select/select.go, internal/links/links.go, internal/plan, commit-path guard.
88: Doc: docs/tasks/links-graph-enforcement.md
89: Accept: linked-submodule dep gates selection; a dep-tag commit forming a cycle is rejected.
90: 
91: ## artifacts-package [TODO] <!-- attempts=0 deps= weight=2 -->
92: internal/artifacts is missing; the web reads ARTIFACTS.md / INFRASTRUCTURE.md raw
93: (web.go:129 explorer, env.go for INFRA). Add a typed model + parser, wire web reads through it.
94: Files: new internal/artifacts/*, internal/web/web.go, internal/web/env.go.
95: Doc: docs/tasks/artifacts-package.md
96: Accept: structured parse/render of both files; web stops reading raw; round-trip tests.
97: 
98: ## roi-pre-receive-hook [TODO] <!-- attempts=0 deps= weight=2 -->
99: ROI protection is incomplete: only a pre-commit hook exists (config/hook.go); pushes are
100: unprotected. Add a server-side pre-receive hook (mirror the honeybee-identity check) plus an
101: install path.
102: Files: internal/config/hook.go, internal/config/hook_test.go.
103: Doc: docs/tasks/roi-pre-receive-hook.md
104: Accept: a push touching ROI.md under honeybee identity is rejected; frontend push allowed.
105: 
106: ## frontend-write-paths-cli [TODO] <!-- attempts=0 deps= weight=2 -->
107: Frontend write paths bypass CLI logic. submoduleAdd does a bare mkdir (web.go:259) instead of
108: `git submodule add`; submoduleLink appends raw `from: [to]` YAML (web.go:276) instead of
109: links.AddDep (cycle-checked). Extract a shared helper from cmd/beehive/cmd_submodule.go and
110: call it from both CLI and web.
111: Files: internal/web/web.go, cmd/beehive/cmd_submodule.go, shared helper pkg, tests.
112: Doc: docs/tasks/frontend-write-paths-cli.md
113: Accept: web add creates a real tracked submodule; web link is cycle-checked valid YAML.
114: 
115: ## web-plan-parser-unify [TODO] <!-- attempts=0 deps= weight=3 -->
116: The web has its own PLAN.md parser (web/plan.go) expecting a bullet format that does NOT match
117: the real header format (internal/plan/plan.go). Dashboard pending/NEEDS-HUMAN counts, plan
118: view, and human view all misparse real plans. Delegate web parsing to internal/plan; unify the
119: duplicate roiStamp regex. Enabler for dashboard-cards and plan-view-pills.
120: Files: internal/web/plan.go, internal/web/web.go, internal/web/env.go, web_test.go.
121: Doc: docs/tasks/web-plan-parser-unify.md
122: Accept: a real header-format PLAN.md parses; counts/pills correct; tests use real format.
123: 
124: ## publish-main-writes [TODO] <!-- attempts=0 deps=git-remote-ops weight=2 -->
125: beehived writes commit locally but never push, so edits don't reach origin and other
126: hosts/honeybees never see them. Every handler uses s.commit (web.go:341): roiPost (web.go:182),
127: secretsPost (209), mergePost (232), submoduleAdd (259), submoduleLink (276), envDeploy (306).
128: Add publishMain (commit + push origin main, ff-retry on non-ff) and route all writes through it.
129: Files: internal/web/web.go, web_test.go.
130: Doc: docs/tasks/publish-main-writes.md
131: Accept: each write lands on origin main (temp-origin test); concurrent advance retries, no lost
132: write; no-origin install still commits locally.
133: 
134: ## config-layered [TODO] <!-- attempts=0 deps= weight=2 -->
135: config.Load (config.go:53) reads one flat /etc/beehive/config.yaml; no layering, no per-submodule
136: scope, no temperature/token-limit knobs. Add Resolve(root, submodule) merging layers most-
137: specific-wins: Defaults -> host file -> in-repo global -> per-submodule override; add
138: Temperature/MaxTokens and thread them into opencode (opencode.go:51). Callers resolve the
139: effective config per submodule at runtime.
140: Files: internal/config/config.go, config_test.go, internal/swarm/opencode.go.
141: Doc: docs/tasks/config-layered.md
142: Accept: per-scope precedence verified field-by-field (submodule>global>host>default); unset
143: fields fall through; bare single-host install still works.
144: 
145: ## design-system-css [TODO] <!-- attempts=0 deps= weight=2 -->
146: Replace the ad-hoc 4-line style.css with a coherent design system: typography scale, spacing
147: scale, and status-color tokens for TODO / IN-PROGRESS / NEEDS-REVIEW / NEEDS-ARBITRATION /
148: DONE / NEEDS-HUMAN, in light + dark (prefers-color-scheme). Foundation for all frontend views.
149: Keep the single-binary embed (no SPA, no CDN).
150: Files: internal/web/assets/style.css, internal/web/templates/layout.html.
151: Doc: docs/tasks/design-system-css.md
152: Accept: tokens cover all six statuses; dark mode works; assets stay embedded.
153: 
154: ## dashboard-cards [TODO] <!-- attempts=0 deps=design-system-css,web-plan-parser-unify -->
155: Dashboard as submodule cards: live swarm status (active/dormant/bootstrap), env badge
156: (blue/green), and a NEEDS-HUMAN count. Counts must come from the unified parser.
157: Files: internal/web/templates/dashboard.html, internal/web/web.go (subView).
158: Doc: docs/tasks/dashboard-cards.md
159: Accept: one card per submodule with correct state, env badge, and human count.
160: 
161: ## plan-view-pills [TODO] <!-- attempts=0 deps=design-system-css,web-plan-parser-unify -->
162: Plan view: clear status pills (tokenized), dependency indicators, heartbeat/TTL freshness
163: indicators for IN-PROGRESS tasks, and change-doc links from the commit stamp.
164: Files: internal/web/templates/plan_items.html, internal/web/web.go.
165: Doc: docs/tasks/plan-view-pills.md
166: Accept: pills per status; deps shown; stale heartbeat flagged; doc links resolve.
167: 
168: ## branch-graph-sectioned [TODO] <!-- attempts=0 deps=design-system-css -->
169: Branch graph sectioned/paginated per submodule with commit-stamp linkage and no cross-
170: submodule crawl (branches.go already paginates one repo; add sectioning + stamp links UI).
171: Files: internal/web/templates/branch_view.html, internal/web/branches.go.
172: Doc: docs/tasks/branch-graph-sectioned.md
173: Accept: per-submodule sections, paginated, each commit links its Beehive change doc.
174: 
175: ## htmx-polish [TODO] <!-- attempts=0 deps=design-system-css -->
176: Polish htmx interactions: loading states/indicators, inline edit affordances, and a confirm
177: prompt on destructive merge. Keep single-binary embed; no SPA.
178: Files: internal/web/templates/*.html.
179: Doc: docs/tasks/htmx-polish.md
180: Accept: visible loading state on swaps; inline edit on ROI/secrets; merge asks confirm.
181: 
182: ## poll-scroll-preserve [TODO] <!-- attempts=0 deps=design-system-css weight=2 -->
183: htmx poll refresh replaces scrollable panes (markdown explorer, chat panel, session list),
184: resetting scrollTop to 0 mid-read. Patch-in-place (hx-preserve/morph or OOB row swaps) or
185: save/restore scrollTop around htmx:beforeSwap/afterSwap via embedded JS in the layout; keep
186: chat/stream bottom-pinned. (Polling panes live on the synced tip, ahead of 082d38c.)
187: Files: internal/web/templates/*.html, internal/web/assets/*, web_test.go.
188: Doc: docs/tasks/poll-scroll-preserve.md
189: Accept: polling fragments carry preserve/pane-id wiring and the restore script is embedded; live
190: updates not dropped; manual scroll check documented.
191: 
192: ## merge-button-wire [TODO] <!-- attempts=0 deps=git-remote-ops weight=2 -->
193: The merge action must publish a change, not no-op. mergePost (web.go:232) merges + commits LOCALLY
194: but never pushes origin nor advances/commits the beehive pointer; the branch-view publish control
195: is inert. Route merge through publishMain: Merge -> Push origin tracked-branch -> stage+commit the
196: pointer; wire the UI control with destructive-confirm.
197: Files: internal/web/web.go, internal/web/templates/merge_panel.html, branch_view.html, web_test.go.
198: Doc: docs/tasks/merge-button-wire.md
199: Accept: POST /merge ff's + pushes origin + advances pointer (temp-origin test); conflict returns
200: 409, origin untouched; idempotent on already-merged.
201: 
202: ## chat-diff-editor-core [TODO] <!-- attempts=0 deps=opencode-turn-poll weight=2 -->
203: Generalize the per-file editor into one chat-diff surface over any repo file. 082d38c has only a
204: textarea (roiGet/roiPost web.go:172-198); the shipped chat editor is on the synced tip. Add a
205: generic /edit?path= handler backed by an opencode session in a per-edit ROOT worktree: render
206: worktree state inline, propose a unified diff, apply+commit only on human approval. Reuse
207: opencode-turn-poll to await the assistant turn.
208: Files: internal/web/web.go, internal/web/templates/*, internal/swarm, web_test.go.
209: Doc: docs/tasks/chat-diff-editor-core.md
210: Accept: a chat turn for an arbitrary path yields a proposed diff; approve writes+commits in the
211: edit worktree, reject is a no-op; path traversal rejected. FOUNDATION for the chat-diff tasks.
212: 
213: ## chat-diff-file-context [TODO] <!-- attempts=0 deps=chat-diff-editor-core -->
214: Per-file edits need injected rules so they stay formatted/protocol-safe. Add a path->context
215: resolver (ROI.md conventions+FORBIDDEN, PLAN.md format/state machine per internal/plan, RULES.md +
216: AGENTS.md, generic default) and seed it into the opencode session (NewSession system arg,
217: opencode.go:25) for both per-file links and the generic window. Per-file links route into the
218: generic handler with path preset, replacing bare textareas.
219: Files: internal/web (context resolver + handlers), templates, web_test.go.
220: Doc: docs/tasks/chat-diff-file-context.md
221: Accept: distinct preambles per path; seeded prompt contains the target's rules; editing PLAN.md
222: still round-trips plan.Parse.
223: 
224: ## submodule-rules-md [TODO] <!-- attempts=0 deps=chat-diff-editor-core weight=2 -->
225: Add a per-submodule beehive-owned RULES.md at submodules/<sm>/RULES.md (outside repo/), additive
226: to any AGENTS.md, read into honeybee + chat-diff editor context and shown in the explorer
227: (web.go:129 docs map). Add the path constant alongside ROIFile/PlanFile/InfraFile/Artifacts
228: (internal/repo). Absence is a safe no-op.
229: Files: internal/repo (constants), internal/web/web.go, context resolver, web_test.go.
230: Doc: docs/tasks/submodule-rules-md.md
231: Accept: present RULES.md appears in explorer + agent/edit context; AGENTS.md+RULES.md both present,
232: order AGENTS-then-RULES; absence no-op; never auto-edits ROI.md.
233: 
234: ## chat-skills [TODO] <!-- attempts=0 deps=chat-diff-editor-core -->
235: Expose named, invocable maintenance skills from the chat surface: cleanup-stale, gc (reuse
236: gc-worktree-reclaim/editor-session-persist), resources (INFRASTRUCTURE.md rigs), infra-conventions.
237: Skills propose diffs via the approval path or report-only; destructive ones require confirm.
238: Files: internal/web (skill registry + chat wiring), templates, tests.
239: Doc: docs/tasks/chat-skills.md
240: Accept: registry lookup + dry-run returns a deterministic plan without mutating; applying performs
241: exactly the proposed change; unknown skill errors; no destructive action without approval.
242: 
243: ## frontend-perf-cache [TODO] <!-- attempts=0 deps=web-plan-parser-unify -->
244: Deferred. Parse-once frontend cache: cache derived views, invalidate on commit (HEAD change);
245: document the supported-submodule ceiling the cache assumes.
246: Files: internal/web (cache layer), web_test.go.
247: Doc: docs/tasks/frontend-perf-cache.md
248: Accept: repeated reads hit cache; a commit invalidates; ceiling documented + tested.
249: 
250: ## multi-beehive-ui [TODO] <!-- attempts=0 deps= -->
251: Deferred + coarse (re-decompose when prioritized). Manage/merge multiple beehive repos from
252: one frontend, with per-repo gpg keyrings for strict secret isolation.
253: Files: internal/web, internal/config, internal/secrets.
254: Doc: docs/tasks/multi-beehive-ui.md
255: Accept: register/switch repos; secrets never cross repo keyrings; design doc enumerates subtasks.
256: 
257: ## release-verify [TODO] <!-- attempts=0 deps= -->
258: Deferred. Confirm CI cross-compiles static binaries (CGO_ENABLED=0) and cosign-signs them, and
259: that the artifacts verify. Fix the pipeline if not.
260: Files: .github/workflows/*, packaging/*, docs/RELEASE-NOTES-TEMPLATE.md.
261: Doc: docs/tasks/release-verify.md
262: Accept: CI produces signed static artifacts; `cosign verify-blob` passes in a clean checkout.
263: 
264: ## editor-markdown-render [TODO] <!-- attempts=0 deps=design-system-css -->
265: Deferred. Editor/explorer render markdown as raw text (roiGet web.go:172 textarea; explorer
266: web.go:123 dumps strings; chat-diff shows raw diffs). Render markdown->HTML for VIEW panes with a
267: pure-Go CGO-free renderer (goldmark/gomarkdown), sanitized; keep an editable raw source view; style
268: diffs with design tokens.
269: Files: internal/web (render helper), templates, assets, web_test.go.
270: Doc: docs/tasks/editor-markdown-render.md
271: Accept: markdown renders to expected sanitized HTML; raw edit path returns verbatim; diff renders
272: with add/del classes; dependency is CGO-free.
273: 
274: ## editor-session-persist [TODO] <!-- attempts=0 deps= -->
275: Deferred. The shipped chat editor leaks ROOT edit worktrees (live: edit-* branches in git worktree
276: list) and loses in-flight sessions on restart. Persist session state (path, session id, branch,
277: activity); at startup prune stale edit-* worktrees + branches (TTL/no live session) via internal/git
278: Worktree* (mirror gc-worktree-reclaim), never touching bee-* or the main checkout.
279: Files: internal/web or internal/swarm (session store + startup prune), internal/git, tests.
280: Doc: docs/tasks/editor-session-persist.md
281: Accept: startup prune removes exactly stale edit worktrees+branches, keeps active/bee-*; session
282: state survives a simulated restart; never removes worktrees with pending approved changes.
283: 
284: ## remote-host-session-view [TODO] <!-- attempts=0 deps=git-remote-ops -->
285: Deferred. Follow off-box honeybee runs: sessions are markdown under docs/sessions/<branch>.md.
286: Producer periodically commits+pushes session appends (remote-flagged cadence); viewer periodically
287: pulls --ff-only (git-remote-ops Pull) and re-renders the session pane, surfacing last-pulled
288: staleness. Combine with poll-scroll-preserve.
289: Files: internal/swarm (session writer cadence), internal/web (viewer pull+render), tests.
290: Doc: docs/tasks/remote-host-session-view.md
291: Accept: fake-origin periodic session commits are picked up by the viewer pull and rendered;
292: ff-only divergence handled; commits coalesced per interval.
293: 
294: ## agent-output-streaming [TODO] <!-- attempts=0 deps=opencode-turn-poll -->
295: Deferred. opencode is request/response (Prompt opencode.go:49 returns on accept). Consume opencode's
296: streaming/event channel to emit assistant tokens live; add a streaming read path in internal/swarm
297: beside the poll path (stream-end => turn-idle, consistent with opencode-turn-poll). Surface via
298: embedded SSE to the chat/session pane (bottom-pinned); fall back to poll-then-render if unsupported.
299: Files: internal/swarm/opencode.go, swarm_test.go, internal/web (SSE pane), templates.
300: Doc: docs/tasks/agent-output-streaming.md
301: Accept: fake streaming server tokens assemble incrementally; stream-end maps to turn-idle;
302: non-streaming server falls back; ctx-cancel leaks no goroutines.

(End of file - total 302 lines)
</content>
```

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/config/hook_test.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/config/hook_test.go</path>
<type>file</type>
<content>
1: package config
2: 
3: import (
4: 	"os"
5: 	"path/filepath"
6: 	"testing"
7: )
8: 
9: func TestInstallROIHook(t *testing.T) {
10: 	root := t.TempDir()
11: 	if err := InstallROIHook(root); err == nil {
12: 		t.Fatal("want error: not a git repo")
13: 	}
14: 	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
15: 		t.Fatal(err)
16: 	}
17: 	if err := InstallROIHook(root); err != nil {
18: 		t.Fatal(err)
19: 	}
20: 	p := filepath.Join(root, ".git", "hooks", "pre-commit")
21: 	fi, err := os.Stat(p)
22: 	if err != nil {
23: 		t.Fatal(err)
24: 	}
25: 	if fi.Mode()&0o100 == 0 {
26: 		t.Fatal("hook not executable")
27: 	}
28: }

(End of file - total 28 lines)
</content>

<system-reminder>
Instructions from: /home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/AGENTS.md
# Honeybee Agent Instructions

You are a honeybee: one autonomous agent working a single task in a beehive repo. The swarm shares
state only through git merges to `main`. No controller exists. You coordinate by committing.

## Absolute rules
- NEVER edit `ROI.md`. It is the record of intent, owned by humans. FORBIDDEN. (Also enforced by hook.)
- ALL writes happen in your worktree: submodules/<submodule>/worktrees/<worktree>. Use the helper scripts;
  never write the shared repo/ checkout. `scripts/worktree.sh add <sm> <branch>` creates it off the synced
  tip; `scripts/worktree.sh rm <sm> <branch>` removes it on DONE.
- Sync the submodule's tracked branch before working: `scripts/submodule-sync.sh <sm>` fetches the remote
  tracked branch tip and auto-advances the beehive pointer (no review). Always want latest.
- Re-stamp your IN-PROGRESS heartbeat at the start of every turn.
- No shortcuts. Compute real values. No placeholders, no swallowed errors, no fake "done".
- Every plan item you add MUST ship a terse doc (LLM-targeted) under the submodule `docs/`.
- Always keep `PLAN.md`, `ARTIFACTS.md`, `INFRASTRUCTURE.md` current.
- Every submodule commit carries a stamp line `Beehive: <task-id> <doc-path>` so the frontend links
  commits to change docs without scanning. Required.

## You were started with one task in submodules/<submodule>/PLAN.md. Begin.

## Protocol
0. **ROI reconcile (priority 0).** The runner checks if ROI.md changed since PLAN.md's stamp
   `<!-- Beehive-ROI: <sha> -->`. If so you reconcile FIRST: read the ROI.md diff, fold changes into PLAN.md
   (add/modify/retire tasks; in-flight retirees -> NEEDS-REVIEW), restamp to current ROI commit, commit.
   Never edit ROI.md. Then exit; another bee works the updated plan.
1. Immediately mark the task IN-PROGRESS with a UTC timestamp and commit to main. Re-pull and verify your
   stamp won; if not, abandon and reselect. Heartbeat re-stamps each turn; stale heartbeat (1h) -> GC.
2. **GC tasks first.** A task IN-PROGRESS past TTL is garbage. Either finish it from its branch
   (-> NEEDS-REVIEW) or delete dangling state and mark TODO. Merge to main.
3. **Arbitration next.** Resolve NEEDS-ARBITRATION: merge implementer branch (-> DONE) or side with the
   reviewer, mark TODO, notate rejection in plan. Merge.
4. **Review next.** Evaluate branch vs task + ROI. Merge (-> DONE) or set NEEDS-ARBITRATION + rejection
   doc. Merge.
5. **Main task last.** Sync the tracked branch (scripts/submodule-sync.sh, incorporates out-of-band remote
   changes, auto-advances pointer no review). Evaluate vs ROI. If invalid/needs change -> NEEDS-REVIEW + doc. Else work to completion: PLAN.md -> NEEDS-REVIEW on main, branch
   with submodule patch, doc named <branch>-<taskid> covering how/why, tests, follow-ups, caveats.
6. On any -> DONE, update linked dependents (same plan or linked submodule) to unlock them.
7. Plan additions need design/code-ref docs. Terse, LLM-only.
8. NEVER touch ROI.md.

## Turn loop
Each turn the runner checks completion deterministically. If met, you exit. If not, you get "continue":
keep reconciling the assigned task. Conflict on the same item -> select another task or stop.

</system-reminder>
```


## assistant

> 💭 I need to update the heartbeat timestamp in PLAN.md and commit that change to main before moving forward. Looking at the hook_test.go file, I see it's currently testing three scenarios: verifying that InstallROIHook fails when not in a git repository, confirming it succeeds after initializing .git, and checking that the pre-commit hook file gets created at the expected path.

First, re-stamp my heartbeat (protocol requires it each turn). The current heartbeat is `2026-06-29T21:22:03Z`; updating to now.

**🔧 edit** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/PLAN.md`

```
Edit applied successfully.
```


## assistant

> 💭 I'm committing the heartbeat changes to main and verifying that my stamp was successful before pushing.

**🔧 bash** `git -C /home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318 add submodules/beehive/PLAN.md && git -C /home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318 commit -q -m "plan: heartbeat links-graph-enforcement" && echo OK`

```
OK

```


## assistant

> 💭 I'm checking the current state of my work by reviewing the git status in the source worktree, setting up environment variables, and examining the files I've modified to understand where things stand.

Heartbeat re-stamped. Now let me verify the state of my source work — check git status and re-read the files I've edited.

**🔧 bash** `export GOTMPDIR=/home/spencer/.cache/gotmp TMPDIR=/home/spencer/.cache/gotmp CGO_ENABLED=0
SRC=/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo
echo "---STATUS---"; git -C "$SRC" status --short
echo "---BRANCH---"; git -C "$SRC" branch --show-current
echo "---LOG---"; git -C "$SRC" log --oneline -3`

```
---STATUS---
 M internal/links/links.go
 M internal/plan/compat.go
 M internal/repo/repo.go
 M internal/select/select.go
?? internal/plan/deps.go
?? internal/select/graph.go
---BRANCH---
bee-links-graph-enforcement
---LOG---
082d38c fix: init writes full embedded AGENTS.md protocol, not placeholder
d5e2095 fix: build static binaries (CGO_ENABLED=0)
15c71de Merge branch 'cave/agent/implementer-mqytmztc-3'

```


## assistant

> 💭 I need to check the current state of my edited files to see what changes are still uncommitted, then I can move forward with the remaining work.

State matches my notes. Let me re-read all my edited/new source files to get exact current content before continuing.

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/links/links.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/links/links.go</path>
<type>file</type>
<content>
1: // Package links models SUBMODULE-LINKS.yaml: declared links between submodules
2: // plus task-level dependency edges, and enforces an acyclic graph. A link lets a
3: // honeybee on either submodule reference and depend on the other's PLAN.md;
4: // stepwise cross-repo chains are sequenced by dependency tags. Every tag write
5: // runs a cycle check so a wait cycle is rejected. Deterministic; no LLM.
6: package links
7: 
8: import (
9: 	"fmt"
10: 	"os"
11: 	"sort"
12: 
13: 	"gopkg.in/yaml.v3"
14: )
15: 
16: // Links is the parsed SUBMODULE-LINKS.yaml: bidirectional submodule links and
17: // directed task dependencies (Edge: from depends on to).
18: type Links struct {
19: 	Submodules []string `yaml:"submodules,omitempty"` // linked submodule names
20: 	Deps       []Edge   `yaml:"deps,omitempty"`       // directed dependency edges
21: }
22: 
23: // Edge means From depends on To (To must complete before From).
24: type Edge struct {
25: 	From string `yaml:"from"`
26: 	To   string `yaml:"to"`
27: }
28: 
29: // Parse reads SUBMODULE-LINKS.yaml text. Empty input is an empty Links.
30: func Parse(b []byte) (*Links, error) {
31: 	l := &Links{}
32: 	if len(b) == 0 {
33: 		return l, nil
34: 	}
35: 	if err := yaml.Unmarshal(b, l); err != nil {
36: 		return nil, fmt.Errorf("links: parse: %w", err)
37: 	}
38: 	return l, nil
39: }
40: 
41: // Load reads a SUBMODULE-LINKS.yaml file; missing file is empty Links.
42: func Load(path string) (*Links, error) {
43: 	b, err := os.ReadFile(path)
44: 	if err != nil {
45: 		if os.IsNotExist(err) {
46: 			return &Links{}, nil
47: 		}
48: 		return nil, err
49: 	}
50: 	return Parse(b)
51: }
52: 
53: // Save writes the links file deterministically (sorted).
54: func (l *Links) Save(path string) error {
55: 	sort.Strings(l.Submodules)
56: 	sort.Slice(l.Deps, func(i, j int) bool {
57: 		if l.Deps[i].From != l.Deps[j].From {
58: 			return l.Deps[i].From < l.Deps[j].From
59: 		}
60: 		return l.Deps[i].To < l.Deps[j].To
61: 	})
62: 	b, err := yaml.Marshal(l)
63: 	if err != nil {
64: 		return err
65: 	}
66: 	return os.WriteFile(path, b, 0o644)
67: }
68: 
69: // LinkSubmodules records an undirected link between a and b (idempotent).
70: func (l *Links) LinkSubmodules(a, b string) {
71: 	add := func(s string) {
72: 		for _, x := range l.Submodules {
73: 			if x == s {
74: 				return
75: 			}
76: 		}
77: 		l.Submodules = append(l.Submodules, s)
78: 	}
79: 	add(a)
80: 	add(b)
81: }
82: 
83: // AddDep adds a from->to dependency, rejecting it if it creates a cycle.
84: func (l *Links) AddDep(from, to string) error {
85: 	if from == to {
86: 		return fmt.Errorf("links: self-dependency %q", from)
87: 	}
88: 	for _, e := range l.Deps {
89: 		if e.From == from && e.To == to {
90: 			return nil
91: 		}
92: 	}
93: 	l.Deps = append(l.Deps, Edge{From: from, To: to})
94: 	if c := cycle(l.Deps); c != nil {
95: 		l.Deps = l.Deps[:len(l.Deps)-1]
96: 		return fmt.Errorf("links: dependency %s->%s creates cycle: %v", from, to, c)
97: 	}
98: 	return nil
99: }
100: 
101: // HasCycle reports whether the dependency graph contains a wait cycle.
102: func (l *Links) HasCycle() bool { return cycle(l.Deps) != nil }
103: 
104: // HasCycleWith reports whether l.Deps combined with extra edges (e.g. PLAN.md
105: // dependency tags) forms a wait cycle. The receiver is left unchanged.
106: func (l *Links) HasCycleWith(extra ...Edge) bool {
107: 	all := make([]Edge, 0, len(l.Deps)+len(extra))
108: 	all = append(all, l.Deps...)
109: 	all = append(all, extra...)
110: 	return cycle(all) != nil
111: }
112: 
113: // Cycle returns a node sequence forming a directed cycle in edges (the offending
114: // path, first node repeated last), or nil if the graph is acyclic. Exported so
115: // the commit-time guard can report the precise cycle it rejected.
116: func Cycle(edges []Edge) []string { return cycle(edges) }
117: 
118: // CyclicNodes returns the set of nodes that participate in any directed cycle of
119: // edges, via Tarjan strongly-connected components: every node in an SCC of size
120: // greater than one, plus any node carrying a self-loop, is cyclic. Selection uses
121: // this to exclude exactly the entangled tasks instead of deadlocking on them.
122: // Deterministic: nodes and adjacency lists are visited in sorted order.
123: func CyclicNodes(edges []Edge) map[string]bool {
124: 	adj := map[string][]string{}
125: 	nodeSet := map[string]bool{}
126: 	self := map[string]bool{}
127: 	for _, e := range edges {
128: 		adj[e.From] = append(adj[e.From], e.To)
129: 		nodeSet[e.From] = true
130: 		nodeSet[e.To] = true
131: 		if e.From == e.To {
132: 			self[e.From] = true
133: 		}
134: 	}
135: 	for n := range adj {
136: 		sort.Strings(adj[n])
137: 	}
138: 	nodes := make([]string, 0, len(nodeSet))
139: 	for n := range nodeSet {
140: 		nodes = append(nodes, n)
141: 	}
142: 	sort.Strings(nodes)
143: 
144: 	idx := map[string]int{}
145: 	low := map[string]int{}
146: 	onStack := map[string]bool{}
147: 	var stack []string
148: 	next := 0
149: 	cyclic := map[string]bool{}
150: 
151: 	var strong func(v string)
152: 	strong = func(v string) {
153: 		idx[v] = next
154: 		low[v] = next
155: 		next++
156: 		stack = append(stack, v)
157: 		onStack[v] = true
158: 		for _, w := range adj[v] {
159: 			if _, seen := idx[w]; !seen {
160: 				strong(w)
161: 				if low[w] < low[v] {
162: 					low[v] = low[w]
163: 				}
164: 			} else if onStack[w] && idx[w] < low[v] {
165: 				low[v] = idx[w]
166: 			}
167: 		}
168: 		if low[v] != idx[v] {
169: 			return
170: 		}
171: 		var comp []string
172: 		for {
173: 			w := stack[len(stack)-1]
174: 			stack = stack[:len(stack)-1]
175: 			onStack[w] = false
176: 			comp = append(comp, w)
177: 			if w == v {
178: 				break
179: 			}
180: 		}
181: 		if len(comp) > 1 {
182: 			for _, w := range comp {
183: 				cyclic[w] = true
184: 			}
185: 		} else if self[comp[0]] {
186: 			cyclic[comp[0]] = true
187: 		}
188: 	}
189: 	for _, v := range nodes {
190: 		if _, seen := idx[v]; !seen {
191: 			strong(v)
192: 		}
193: 	}
194: 	return cyclic
195: }
196: 
197: // cycle returns a node sequence forming a cycle, or nil if acyclic (DFS).
198: func cycle(edges []Edge) []string {
199: 	adj := map[string][]string{}
200: 	for _, e := range edges {
201: 		adj[e.From] = append(adj[e.From], e.To)
202: 	}
203: 	const (
204: 		white = 0
205: 		gray  = 1
206: 		black = 2
207: 	)
208: 	color := map[string]int{}
209: 	var path, found []string
210: 	var dfs func(string) bool
211: 	dfs = func(n string) bool {
212: 		color[n] = gray
213: 		path = append(path, n)
214: 		for _, m := range adj[n] {
215: 			if color[m] == gray {
216: 				found = append(append([]string{}, path...), m)
217: 				return true
218: 			}
219: 			if color[m] == white && dfs(m) {
220: 				return true
221: 			}
222: 		}
223: 		path = path[:len(path)-1]
224: 		color[n] = black
225: 		return false
226: 	}
227: 	nodes := make([]string, 0, len(adj))
228: 	for n := range adj {
229: 		nodes = append(nodes, n)
230: 	}
231: 	sort.Strings(nodes)
232: 	for _, n := range nodes {
233: 		if color[n] == white && dfs(n) {
234: 			return found
235: 		}
236: 	}
237: 	return nil
238: }

(End of file - total 238 lines)
</content>
```

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/plan/deps.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/plan/deps.go</path>
<type>file</type>
<content>
1: package plan
2: 
3: import (
4: 	"strings"
5: 	"time"
6: )
7: 
8: // Cross-submodule dependency references use a qualified form: "<submodule>:<taskid>".
9: // A bare token (no colon) is a dependency on a task in the same PLAN.md. This is
10: // the single, deterministic convention shared by selection, the cycle guard, and
11: // SUBMODULE-LINKS.yaml task edges.
12: 
13: // Qualify returns the cross-submodule task reference "<submodule>:<id>".
14: func Qualify(submodule, id string) string { return submodule + ":" + id }
15: 
16: // SplitDep parses a dependency token. "<submodule>:<taskid>" is a cross-submodule
17: // reference (cross=true); a bare token is local to the owning plan and returns
18: // submodule "". Only the first colon splits, so task ids may not contain one.
19: func SplitDep(dep string) (submodule, task string, cross bool) {
20: 	if i := strings.IndexByte(dep, ':'); i >= 0 {
21: 		return dep[:i], dep[i+1:], true
22: 	}
23: 	return "", dep, false
24: }
25: 
26: // DepStatus resolves a cross-submodule dependency's status by loading the linked
27: // submodule's PLAN.md. ok=false means unresolved — missing/unlinked submodule or
28: // missing task — which blocks selection (a dep that cannot be proven DONE gates).
29: type DepStatus func(submodule, task string) (Status, bool)
30: 
31: // SelectableExt reports whether t can be auto-selected from the plan owned by
32: // submodule self. Unlike Selectable it (a) excludes any task entangled in a wait
33: // cycle — cyclic holds qualified ids "<self>:<id>" — and (b) resolves cross-
34: // submodule deps through ext while resolving local deps within p. A nil ext makes
35: // every cross-submodule dep unresolved (blocking), matching local-only callers.
36: func (p *Plan) SelectableExt(self string, t *Task, ext DepStatus, cyclic map[string]bool) bool {
37: 	if t.Status == StatusDone || t.Status == StatusHuman {
38: 		return false
39: 	}
40: 	if cyclic[Qualify(self, t.ID)] {
41: 		return false
42: 	}
43: 	for _, d := range t.Deps {
44: 		sub, task, cross := SplitDep(d)
45: 		var (
46: 			st Status
47: 			ok bool
48: 		)
49: 		switch {
50: 		case cross:
51: 			if ext != nil {
52: 				st, ok = ext(sub, task)
53: 			}
54: 		default:
55: 			if dep := p.Task(task); dep != nil {
56: 				st, ok = dep.Status, true
57: 			}
58: 		}
59: 		if !ok || st != StatusDone {
60: 			return false
61: 		}
62: 	}
63: 	return true
64: }
65: 
66: // CandidatesExt mirrors Candidates but is link-graph aware: tasks whose qualified
67: // id is in cyclic are never selected (a wait cycle would otherwise deadlock), and
68: // TODO selectability resolves cross-submodule deps via ext. The tier priority
69: // (GC > arbitration > review > main) is unchanged.
70: func (p *Plan) CandidatesExt(now time.Time, ttl time.Duration, self string, ext DepStatus, cyclic map[string]bool) []Task {
71: 	var gc, arb, rev, main []Task
72: 	for _, t := range p.Tasks {
73: 		if cyclic[Qualify(self, t.ID)] {
74: 			continue
75: 		}
76: 		switch {
77: 		case t.Stale(now, ttl):
78: 			gc = append(gc, *t)
79: 		case t.Status == StatusArb:
80: 			arb = append(arb, *t)
81: 		case t.Status == StatusReview:
82: 			rev = append(rev, *t)
83: 		case t.Status == StatusTODO && p.SelectableExt(self, t, ext, cyclic):
84: 			main = append(main, *t)
85: 		}
86: 	}
87: 	for _, tier := range [][]Task{gc, arb, rev, main} {
88: 		if len(tier) > 0 {
89: 			return tier
90: 		}
91: 	}
92: 	return nil
93: }

(End of file - total 93 lines)
</content>
```

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/select/graph.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/select/graph.go</path>
<type>file</type>
<content>
1: package selectt
2: 
3: import (
4: 	"fmt"
5: 	"os"
6: 	"strings"
7: 
8: 	"github.com/spencerharmon/beehive/internal/links"
9: 	"github.com/spencerharmon/beehive/internal/plan"
10: 	"github.com/spencerharmon/beehive/internal/repo"
11: )
12: 
13: // LoadEdges returns every dependency edge across all submodules — each PLAN.md's
14: // task dep tags plus its SUBMODULE-LINKS.yaml task edges — normalized to qualified
15: // "<submodule>:<taskid>" ids. It is the single combined graph shared by both
16: // selection's cycle-skip and the commit-time cycle guard, so they never disagree.
17: func LoadEdges(r *repo.Repo) ([]links.Edge, error) {
18: 	subs, err := r.Submodules()
19: 	if err != nil {
20: 		return nil, err
21: 	}
22: 	var all []links.Edge
23: 	for _, sm := range subs {
24: 		b, err := os.ReadFile(sm.PlanPath())
25: 		switch {
26: 		case err == nil:
27: 			p, perr := plan.Parse(string(b))
28: 			if perr != nil {
29: 				return nil, fmt.Errorf("select: parse %s PLAN.md: %w", sm.Name, perr)
30: 			}
31: 			all = append(all, planEdges(sm.Name, p)...)
32: 		case os.IsNotExist(err):
33: 			// no plan yet; only link edges (if any) contribute
34: 		default:
35: 			return nil, err
36: 		}
37: 		l, lerr := links.Load(sm.LinksPath())
38: 		if lerr != nil {
39: 			return nil, lerr
40: 		}
41: 		all = append(all, linkEdges(sm.Name, l)...)
42: 	}
43: 	return all, nil
44: }
45: 
46: // Validate reports a wait cycle in the combined cross-submodule dependency graph.
47: // The pre-commit guard calls it to reject a dep-tag commit that would form a cycle
48: // (links.AddDep already guards CLI link writes; this guards honeybee PLAN.md writes).
49: func Validate(r *repo.Repo) error {
50: 	edges, err := LoadEdges(r)
51: 	if err != nil {
52: 		return err
53: 	}
54: 	if c := links.Cycle(edges); c != nil {
55: 		return fmt.Errorf("dependency wait cycle: %s", strings.Join(c, " -> "))
56: 	}
57: 	return nil
58: }
59: 
60: // planEdges converts a plan's task dep tags into qualified graph edges (From
61: // depends on To). Local deps are qualified with self; cross-submodule deps keep
62: // their own qualifier.
63: func planEdges(self string, p *plan.Plan) []links.Edge {
64: 	var es []links.Edge
65: 	for _, t := range p.Tasks {
66: 		from := plan.Qualify(self, t.ID)
67: 		for _, d := range t.Deps {
68: 			sub, task, cross := plan.SplitDep(d)
69: 			to := plan.Qualify(self, task)
70: 			if cross {
71: 				to = plan.Qualify(sub, task)
72: 			}
73: 			es = append(es, links.Edge{From: from, To: to})
74: 		}
75: 	}
76: 	return es
77: }
78: 
79: // linkEdges converts SUBMODULE-LINKS.yaml task edges into qualified graph edges.
80: // A bare endpoint is interpreted as a task in the file's own submodule (self).
81: func linkEdges(self string, l *links.Links) []links.Edge {
82: 	es := make([]links.Edge, 0, len(l.Deps))
83: 	for _, e := range l.Deps {
84: 		es = append(es, links.Edge{From: qualifyToken(self, e.From), To: qualifyToken(self, e.To)})
85: 	}
86: 	return es
87: }
88: 
89: func qualifyToken(self, tok string) string {
90: 	if strings.Contains(tok, ":") {
91: 		return tok
92: 	}
93: 	return plan.Qualify(self, tok)
94: }

(End of file - total 94 lines)
</content>
```

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/select/select.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/select/select.go</path>
<type>file</type>
<content>
1: // Package selectt performs deterministic, no-LLM task selection that always
2: // yields a workable task: weighted-random over submodules, ROI-reconcile as
3: // priority 0 (PLAN.md stamp vs ROI.md commit), bootstrap when PLAN absent, then
4: // GC > arbitration > review > main by priority, dependency-gated, cycle-skipped,
5: // NEEDS-HUMAN excluded. The package name avoids the "select" keyword.
6: package selectt
7: 
8: import (
9: 	"context"
10: 	"math/rand"
11: 	"os"
12: 	"path/filepath"
13: 	"strconv"
14: 	"strings"
15: 	"time"
16: 
17: 	"github.com/spencerharmon/beehive/internal/git"
18: 	"github.com/spencerharmon/beehive/internal/links"
19: 	"github.com/spencerharmon/beehive/internal/plan"
20: 	"github.com/spencerharmon/beehive/internal/repo"
21: )
22: 
23: // Kind names the work a selection yields.
24: type Kind string
25: 
26: const (
27: 	Reconcile Kind = "reconcile" // priority 0: ROI.md drifted from PLAN stamp
28: 	Bootstrap Kind = "bootstrap" // ROI present, PLAN absent
29: 	Work      Kind = "work"      // a concrete PLAN task
30: )
31: 
32: // Selection is the deterministic result handed to the swarm before launch.
33: type Selection struct {
34: 	Kind      Kind
35: 	Submodule repo.Submodule
36: 	Task      plan.Task // valid only when Kind == Work
37: 	DiffRange string    // <stamp>..<head> for reconcile, "" otherwise
38: }
39: 
40: // Selector picks a submodule and task. Rand makes results reproducible for tests
41: // while still random per-process; TTL drives GC detection.
42: type Selector struct {
43: 	Repo *repo.Repo
44: 	Git  *git.Repo // beehive repo root, for ROI commit lookup
45: 	Rand *rand.Rand
46: 	TTL  time.Duration
47: }
48: 
49: // Select walks weighted-random submodules and returns the first workable item.
50: // nil is returned only when no submodule has any workable task.
51: func (s *Selector) Select(ctx context.Context) (*Selection, error) {
52: 	subs, err := s.Repo.Submodules()
53: 	if err != nil {
54: 		return nil, err
55: 	}
56: 	// Combined cross-submodule dependency graph: tasks entangled in a wait cycle
57: 	// are excluded from selection (rather than deadlocking) in every submodule.
58: 	edges, err := LoadEdges(s.Repo)
59: 	if err != nil {
60: 		return nil, err
61: 	}
62: 	cyclic := links.CyclicNodes(edges)
63: 	order := s.weightedOrder(subs)
64: 	now := time.Now().UTC()
65: 	for _, sm := range order {
66: 		sel, err := s.fromSubmodule(ctx, sm, now, cyclic)
67: 		if err != nil {
68: 			return nil, err
69: 		}
70: 		if sel != nil {
71: 			return sel, nil
72: 		}
73: 	}
74: 	return nil, nil
75: }
76: 
77: func (s *Selector) fromSubmodule(ctx context.Context, sm repo.Submodule, now time.Time, cyclic map[string]bool) (*Selection, error) {
78: 	if sm.Dormant() {
79: 		return nil, nil
80: 	}
81: 	if sm.NeedsBootstrap() {
82: 		return &Selection{Kind: Bootstrap, Submodule: sm}, nil
83: 	}
84: 	rng, err := s.reconcileRange(ctx, sm)
85: 	if err != nil {
86: 		return nil, err
87: 	}
88: 	if rng != "" {
89: 		return &Selection{Kind: Reconcile, Submodule: sm, DiffRange: rng}, nil
90: 	}
91: 	b, err := os.ReadFile(sm.PlanPath())
92: 	if err != nil {
93: 		if os.IsNotExist(err) {
94: 			return nil, nil
95: 		}
96: 		return nil, err
97: 	}
98: 	pl, err := plan.Parse(string(b))
99: 	if err != nil {
100: 		return nil, err
101: 	}
102: 	ext, err := s.externalStatus(sm)
103: 	if err != nil {
104: 		return nil, err
105: 	}
106: 	cands := pl.CandidatesExt(now, s.TTL, sm.Name, ext, cyclic)
107: 	if len(cands) == 0 {
108: 		return nil, nil
109: 	}
110: 	t := s.pickTask(cands)
111: 	return &Selection{Kind: Work, Submodule: sm, Task: t}, nil
112: }
113: 
114: // externalStatus returns a resolver for sm's cross-submodule dependencies: it
115: // reads the linked submodule's PLAN.md and reports the referenced task's status.
116: // Only submodules declared in sm's SUBMODULE-LINKS.yaml are resolvable; a dep on
117: // an unlinked submodule (or a missing task/plan) stays unresolved and so blocks
118: // selection until satisfied.
119: func (s *Selector) externalStatus(sm repo.Submodule) (plan.DepStatus, error) {
120: 	l, err := links.Load(sm.LinksPath())
121: 	if err != nil {
122: 		return nil, err
123: 	}
124: 	linked := make(map[string]bool, len(l.Submodules))
125: 	for _, n := range l.Submodules {
126: 		linked[n] = true
127: 	}
128: 	return func(subName, taskID string) (plan.Status, bool) {
129: 		if !linked[subName] {
130: 			return "", false
131: 		}
132: 		osm := repo.Submodule{Name: subName, Path: filepath.Join(s.Repo.Root, "submodules", subName)}
133: 		b, err := os.ReadFile(osm.PlanPath())
134: 		if err != nil {
135: 			return "", false
136: 		}
137: 		op, err := plan.Parse(string(b))
138: 		if err != nil {
139: 			return "", false
140: 		}
141: 		dt := op.Task(taskID)
142: 		if dt == nil {
143: 			return "", false
144: 		}
145: 		return dt.Status, true
146: 	}, nil
147: }
148: 
149: // reconcileRange returns "<stamp>..<roiHead>" when ROI.md drifted, else "".
150: func (s *Selector) reconcileRange(ctx context.Context, sm repo.Submodule) (string, error) {
151: 	if _, err := os.Stat(sm.ROIPath()); err != nil {
152: 		return "", nil
153: 	}
154: 	roiPath := "submodules/" + sm.Name + "/" + repo.ROIFile
155: 	head, err := s.Git.LastCommit(ctx, roiPath)
156: 	if err != nil || head == "" {
157: 		return "", err
158: 	}
159: 	stamp, err := sm.ROIStamp()
160: 	if err != nil {
161: 		return "", err
162: 	}
163: 	if stamp == head || strings.HasPrefix(head, stamp) && stamp != "" {
164: 		return "", nil
165: 	}
166: 	from := stamp
167: 	if from == "" {
168: 		from = "ROOT"
169: 	}
170: 	return from + ".." + head, nil
171: }
172: 
173: // weightedOrder returns submodules shuffled, each repeated by its weight, so
174: // higher-weighted submodules are tried first on average. Deterministic per Rand.
175: func (s *Selector) weightedOrder(subs []repo.Submodule) []repo.Submodule {
176: 	pool := make([]repo.Submodule, 0, len(subs))
177: 	for _, sm := range subs {
178: 		w := s.weight(sm)
179: 		for i := 0; i < w; i++ {
180: 			pool = append(pool, sm)
181: 		}
182: 	}
183: 	s.Rand.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
184: 	seen := map[string]bool{}
185: 	out := make([]repo.Submodule, 0, len(subs))
186: 	for _, sm := range pool {
187: 		if !seen[sm.Name] {
188: 			seen[sm.Name] = true
189: 			out = append(out, sm)
190: 		}
191: 	}
192: 	return out
193: }
194: 
195: // weight reads submodules/<name>/weight (positive int), default 1.
196: func (s *Selector) weight(sm repo.Submodule) int {
197: 	b, err := os.ReadFile(filepath.Join(sm.Path, "weight"))
198: 	if err != nil {
199: 		return 1
200: 	}
201: 	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
202: 	if err != nil || n < 1 {
203: 		return 1
204: 	}
205: 	return n
206: }
207: 
208: // pickTask weighted-randomly chooses one candidate by Task.Weight.
209: func (s *Selector) pickTask(cands []plan.Task) plan.Task {
210: 	total := 0
211: 	for _, t := range cands {
212: 		w := t.Weight
213: 		if w < 1 {
214: 			w = 1
215: 		}
216: 		total += w
217: 	}
218: 	r := s.Rand.Intn(total)
219: 	for _, t := range cands {
220: 		w := t.Weight
221: 		if w < 1 {
222: 			w = 1
223: 		}
224: 		if r < w {
225: 			return t
226: 		}
227: 		r -= w
228: 	}
229: 	return cands[len(cands)-1]
230: }

(End of file - total 230 lines)
</content>
```


## assistant

> 💭 My code looks solid, so I'm moving past that `"ROOT"` sentinel issue in `reconcileRange` since it belongs to a different task. Now I need to verify the changes in `internal/plan/compat.go` and `internal/repo/repo.go`, then extend `internal/config/hook.go` and work on the command-line interface.
> 
> I'm also reviewing the plan and repo packages in parallel to understand their type definitions, status constants, and key methods like Task, Stale, and Selectable.

My core selection logic looks complete. Now let me read the remaining files I need to modify/understand: compat.go, repo.go, hook.go, and the cmd files + plan types.

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/plan/compat.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/plan/compat.go</path>
<type>file</type>
<content>
1: package plan
2: 
3: import "time"
4: 
5: // Short status aliases for swarm/select/claim consumers.
6: const (
7: 	TODO        = StatusTODO
8: 	InProgress  = StatusInProgress
9: 	NeedsReview = StatusReview
10: 	NeedsArb    = StatusArb
11: 	Done        = StatusDone
12: 	NeedsHuman  = StatusHuman
13: )
14: 
15: // Find returns the task with id, nil if absent (alias of Task).
16: func (p *Plan) Find(id string) *Task { return p.Task(id) }
17: 
18: // ROIStamp returns the recorded ROI reconcile sha.
19: func (p *Plan) ROIStamp() string { return p.ROI }
20: 
21: // priorityTiers orders selectable types: GC stale IN-PROGRESS > arbitration >
22: // review > main (TODO). Candidates returns the highest non-empty tier's tasks.
23: // It is the local-only (no cross-submodule, no cycle data) form of CandidatesExt;
24: // selection uses CandidatesExt to enforce the link graph.
25: func (p *Plan) Candidates(now time.Time, ttl time.Duration) []Task {
26: 	return p.CandidatesExt(now, ttl, "", nil, nil)
27: }

(End of file - total 27 lines)
</content>
```

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/config/hook.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/config/hook.go</path>
<type>file</type>
<content>
1: package config
2: 
3: import (
4: 	"fmt"
5: 	"os"
6: 	"path/filepath"
7: )
8: 
9: // roiHook rejects commits that touch ROI.md when authored by a honeybee identity.
10: // Honeybees export BEEHIVE_HONEYBEE=1; only the frontend (unset) may change ROI.md.
11: const roiHook = `#!/usr/bin/env sh
12: # beehive ROI-protect hook (installed by CLI). ROI.md is human-owned; honeybees
13: # must never change it. Enforced here for local commits; a server pre-receive
14: # mirrors this for pushes.
15: [ "${BEEHIVE_HONEYBEE:-0}" = "1" ] || exit 0
16: if git diff --cached --name-only | grep -E '(^|/)ROI\.md$' >/dev/null; then
17:   echo "beehive: honeybee identity may not modify ROI.md" >&2
18:   exit 1
19: fi
20: exit 0
21: `
22: 
23: // InstallROIHook writes the ROI-protect pre-commit hook into the repo's .git dir.
24: func InstallROIHook(repoRoot string) error {
25: 	dir := filepath.Join(repoRoot, ".git", "hooks")
26: 	if _, err := os.Stat(filepath.Join(repoRoot, ".git")); err != nil {
27: 		return fmt.Errorf("not a git repo: %s", repoRoot)
28: 	}
29: 	if err := os.MkdirAll(dir, 0o755); err != nil {
30: 		return err
31: 	}
32: 	p := filepath.Join(dir, "pre-commit")
33: 	return os.WriteFile(p, []byte(roiHook), 0o755)
34: }

(End of file - total 34 lines)
</content>
```

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/cmd/beehive/main.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/cmd/beehive/main.go</path>
<type>file</type>
<content>
1: // Command beehive is the deterministic CLI: repo init, submodule add/link, plan
2: // rollback, secret add/update/edit, worktree add/rm, honeybee start, and ROI-hook
3: // install. No LLM; every command is plain git + file ops.
4: package main
5: 
6: import (
7: 	"fmt"
8: 	"os"
9: 
10: 	"github.com/spf13/cobra"
11: )
12: 
13: func main() {
14: 	root := &cobra.Command{
15: 		Use:           "beehive",
16: 		Short:         "beehive deterministic CLI",
17: 		SilenceUsage:  true,
18: 		SilenceErrors: true,
19: 	}
20: 	root.AddCommand(
21: 		initCmd(),
22: 		versionCmd(),
23: 		submoduleCmd(),
24: 		secretCmd(),
25: 		worktreeCmd(),
26: 		honeybeeCmd(),
27: 		hookCmd(),
28: 	)
29: 	if err := root.Execute(); err != nil {
30: 		fmt.Fprintln(os.Stderr, "beehive:", err)
31: 		os.Exit(1)
32: 	}
33: }

(End of file - total 33 lines)
</content>
```


## assistant

> 💭 I need to examine the existing command file patterns to understand how hookCmd and findRoot work, then look at the repo and plan package types to see what structures I'm working with.

Let me look at the cmd files (for the `hookCmd` pattern + how root/repo is found), repo.go, and plan types.

**🔧 glob** `cmd/beehive/*.go in /home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo`

```
/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/cmd/beehive/main.go
/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/cmd/beehive/cmd_worktree.go
/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/cmd/beehive/cmd_submodule.go
/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/cmd/beehive/cmd_secret.go
/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/cmd/beehive/cmd_basic.go
```

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/repo/repo.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/repo/repo.go</path>
<type>file</type>
<content>
1: // Package repo models the beehive repo layout and derives all state from files.
2: // The frontend and honeybee read state through this package; nothing else owns truth.
3: package repo
4: 
5: import (
6: 	"os"
7: 	"path/filepath"
8: 	"regexp"
9: 	"sort"
10: )
11: 
12: // Layout names. ROI is human-owned and never written by honeybees.
13: const (
14: 	AgentsFile  = "AGENTS.md"
15: 	PlanFile    = "PLAN.md"
16: 	ROIFile     = "ROI.md"
17: 	InfraFile   = "INFRASTRUCTURE.md"
18: 	Artifacts   = "ARTIFACTS.md"
19: 	LinksFile   = "SUBMODULE-LINKS.yaml"
20: 	SecretsFile = "SECRETS.yaml.gpg"
21: )
22: 
23: // roiStamp matches the PLAN.md reconcile marker: <!-- Beehive-ROI: <sha> -->
24: var roiStamp = regexp.MustCompile(`Beehive-ROI:\s*([0-9a-f]+)`)
25: 
26: // Repo is a beehive repo rooted at Root.
27: type Repo struct{ Root string }
28: 
29: // Open returns a Repo if Root contains AGENTS.md and a submodules dir.
30: func Open(root string) (*Repo, error) {
31: 	if _, err := os.Stat(filepath.Join(root, AgentsFile)); err != nil {
32: 		return nil, err
33: 	}
34: 	return &Repo{Root: root}, nil
35: }
36: 
37: // Submodule is one tracked target repo with its beehive coordination files.
38: type Submodule struct {
39: 	Name string
40: 	Path string // submodules/<name>
41: }
42: 
43: // RepoDir is the tracked target checkout (worktree base).
44: func (s Submodule) RepoDir() string { return filepath.Join(s.Path, "repo") }
45: 
46: // PlanPath, ROIPath, WorktreesDir locate coordination files.
47: func (s Submodule) PlanPath() string     { return filepath.Join(s.Path, PlanFile) }
48: func (s Submodule) ROIPath() string      { return filepath.Join(s.Path, ROIFile) }
49: func (s Submodule) WorktreesDir() string { return filepath.Join(s.Path, "worktrees") }
50: 
51: // LinksPath locates the submodule's SUBMODULE-LINKS.yaml (links + task deps).
52: func (s Submodule) LinksPath() string { return filepath.Join(s.Path, LinksFile) }
53: 
54: // Submodules lists submodule dirs sorted by name.
55: func (r *Repo) Submodules() ([]Submodule, error) {
56: 	base := filepath.Join(r.Root, "submodules")
57: 	ents, err := os.ReadDir(base)
58: 	if err != nil {
59: 		if os.IsNotExist(err) {
60: 			return nil, nil
61: 		}
62: 		return nil, err
63: 	}
64: 	var out []Submodule
65: 	for _, e := range ents {
66: 		if !e.IsDir() {
67: 			continue
68: 		}
69: 		out = append(out, Submodule{Name: e.Name(), Path: filepath.Join(base, e.Name())})
70: 	}
71: 	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
72: 	return out, nil
73: }
74: 
75: // Dormant reports whether a submodule has no ROI.md and is never selected.
76: func (s Submodule) Dormant() bool {
77: 	_, err := os.Stat(filepath.Join(s.Path, ROIFile))
78: 	return os.IsNotExist(err)
79: }
80: 
81: // NeedsBootstrap reports ROI present but PLAN absent.
82: func (s Submodule) NeedsBootstrap() bool {
83: 	_, roiErr := os.Stat(filepath.Join(s.Path, ROIFile))
84: 	_, planErr := os.Stat(s.PlanPath())
85: 	return roiErr == nil && os.IsNotExist(planErr)
86: }
87: 
88: // ROIStamp reads the last-reconciled ROI commit from PLAN.md, "" if none.
89: func (s Submodule) ROIStamp() (string, error) {
90: 	b, err := os.ReadFile(s.PlanPath())
91: 	if err != nil {
92: 		if os.IsNotExist(err) {
93: 			return "", nil
94: 		}
95: 		return "", err
96: 	}
97: 	if m := roiStamp.FindSubmatch(b); m != nil {
98: 		return string(m[1]), nil
99: 	}
100: 	return "", nil
101: }

(End of file - total 101 lines)
</content>
```

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/plan/plan.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/plan/plan.go</path>
<type>file</type>
<content>
1: // Package plan parses and rewrites PLAN.md: the per-submodule task list, its
2: // status state machine, ROI reconcile stamp, heartbeat timestamps, attempt
3: // counters, dependency tags, and TTL math. Deterministic; no LLM.
4: //
5: // PLAN.md format (line-oriented, stable round-trip):
6: //
7: //	<!-- Beehive-ROI: <sha> -->
8: //	# Plan
9: //
10: //	## <id> [<STATUS>] <!-- attempts=N deps=a,b heartbeat=<RFC3339> -->
11: //	free-form body lines...
12: //
13: // The ROI stamp is the first comment; tasks are H2 headers carrying a metadata
14: // comment. Body lines between headers belong to the preceding task.
15: package plan
16: 
17: import (
18: 	"bufio"
19: 	"fmt"
20: 	"regexp"
21: 	"strconv"
22: 	"strings"
23: 	"time"
24: )
25: 
26: // Status is a task state. The machine is:
27: //
28: //	TODO -> IN-PROGRESS -> NEEDS-REVIEW -> {DONE | NEEDS-ARBITRATION}
29: //	NEEDS-ARBITRATION -> {TODO | DONE}
30: //	rejections > limit -> NEEDS-HUMAN (terminal, frontend-only)
31: type Status string
32: 
33: const (
34: 	StatusTODO       Status = "TODO"
35: 	StatusInProgress Status = "IN-PROGRESS"
36: 	StatusReview     Status = "NEEDS-REVIEW"
37: 	StatusArb        Status = "NEEDS-ARBITRATION"
38: 	StatusDone       Status = "DONE"
39: 	StatusHuman      Status = "NEEDS-HUMAN"
40: )
41: 
42: var allStatuses = map[Status]bool{
43: 	StatusTODO: true, StatusInProgress: true, StatusReview: true,
44: 	StatusArb: true, StatusDone: true, StatusHuman: true,
45: }
46: 
47: // Task is one PLAN.md item.
48: type Task struct {
49: 	ID        string
50: 	Title     string
51: 	Status    Status
52: 	Attempts  int
53: 	Deps      []string
54: 	Weight    int       // selection weight, default 1
55: 	Heartbeat time.Time // zero when not IN-PROGRESS
56: 	Body      []string  // body lines verbatim, without trailing blank
57: }
58: 
59: // Plan is a parsed PLAN.md.
60: type Plan struct {
61: 	ROI    string // Beehive-ROI stamp sha, "" if none
62: 	Header []string
63: 	Tasks  []*Task
64: }
65: 
66: var (
67: 	stampRe  = regexp.MustCompile(`<!--\s*Beehive-ROI:\s*([0-9a-f]*)\s*-->`)
68: 	headerRe = regexp.MustCompile(`^##\s+(\S+)\s+\[([A-Z-]+)\](?:\s+<!--\s*(.*?)\s*-->)?\s*$`)
69: )
70: 
71: // Parse reads PLAN.md text.
72: func Parse(s string) (*Plan, error) {
73: 	p := &Plan{}
74: 	sc := bufio.NewScanner(strings.NewReader(s))
75: 	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
76: 	var cur *Task
77: 	for sc.Scan() {
78: 		line := sc.Text()
79: 		if cur == nil {
80: 			if m := stampRe.FindStringSubmatch(line); m != nil {
81: 				p.ROI = m[1]
82: 				p.Header = append(p.Header, line)
83: 				continue
84: 			}
85: 		}
86: 		if m := headerRe.FindStringSubmatch(line); m != nil {
87: 			t, err := parseHeader(m)
88: 			if err != nil {
89: 				return nil, err
90: 			}
91: 			cur = t
92: 			p.Tasks = append(p.Tasks, t)
93: 			continue
94: 		}
95: 		if cur == nil {
96: 			p.Header = append(p.Header, line)
97: 		} else {
98: 			cur.Body = append(cur.Body, line)
99: 		}
100: 	}
101: 	if err := sc.Err(); err != nil {
102: 		return nil, err
103: 	}
104: 	for _, t := range p.Tasks {
105: 		t.Body = trimTrailingBlank(t.Body)
106: 	}
107: 	p.Header = trimTrailingBlank(p.Header)
108: 	return p, nil
109: }
110: 
111: func parseHeader(m []string) (*Task, error) {
112: 	st := Status(m[2])
113: 	if !allStatuses[st] {
114: 		return nil, fmt.Errorf("plan: unknown status %q for task %s", m[2], m[1])
115: 	}
116: 	t := &Task{ID: m[1], Status: st}
117: 	for _, kv := range strings.Fields(m[3]) {
118: 		k, v, ok := strings.Cut(kv, "=")
119: 		if !ok {
120: 			continue
121: 		}
122: 		switch k {
123: 		case "attempts":
124: 			n, err := strconv.Atoi(v)
125: 			if err != nil {
126: 				return nil, fmt.Errorf("plan: bad attempts %q for %s", v, t.ID)
127: 			}
128: 			t.Attempts = n
129: 		case "deps":
130: 			if v != "" {
131: 				t.Deps = strings.Split(v, ",")
132: 			}
133: 		case "weight":
134: 			n, err := strconv.Atoi(v)
135: 			if err != nil {
136: 				return nil, fmt.Errorf("plan: bad weight %q for %s", v, t.ID)
137: 			}
138: 			t.Weight = n
139: 		case "heartbeat":
140: 			ts, err := time.Parse(time.RFC3339, v)
141: 			if err != nil {
142: 				return nil, fmt.Errorf("plan: bad heartbeat %q for %s", v, t.ID)
143: 			}
144: 			t.Heartbeat = ts
145: 		}
146: 	}
147: 	return t, nil
148: }
149: 
150: func trimTrailingBlank(ls []string) []string {
151: 	for len(ls) > 0 && strings.TrimSpace(ls[len(ls)-1]) == "" {
152: 		ls = ls[:len(ls)-1]
153: 	}
154: 	return ls
155: }
156: 
157: // String serializes a plan deterministically; Parse(p.String()) round-trips.
158: func (p *Plan) String() string {
159: 	var b strings.Builder
160: 	if len(p.Header) > 0 {
161: 		b.WriteString(strings.Join(p.Header, "\n"))
162: 		b.WriteString("\n")
163: 	}
164: 	for _, t := range p.Tasks {
165: 		b.WriteString("\n")
166: 		b.WriteString(t.header())
167: 		b.WriteString("\n")
168: 		if len(t.Body) > 0 {
169: 			b.WriteString(strings.Join(t.Body, "\n"))
170: 			b.WriteString("\n")
171: 		}
172: 	}
173: 	return b.String()
174: }
175: 
176: func (t *Task) header() string {
177: 	meta := fmt.Sprintf("attempts=%d deps=%s", t.Attempts, strings.Join(t.Deps, ","))
178: 	if t.Weight > 1 {
179: 		meta += fmt.Sprintf(" weight=%d", t.Weight)
180: 	}
181: 	if !t.Heartbeat.IsZero() {
182: 		meta += " heartbeat=" + t.Heartbeat.UTC().Format(time.RFC3339)
183: 	}
184: 	return fmt.Sprintf("## %s [%s] <!-- %s -->", t.ID, t.Status, meta)
185: }
186: 
187: // Stamp sets the Beehive-ROI sha, inserting the comment if absent.
188: func (p *Plan) Stamp(sha string) {
189: 	line := "<!-- Beehive-ROI: " + sha + " -->"
190: 	for i, h := range p.Header {
191: 		if stampRe.MatchString(h) {
192: 			p.Header[i] = line
193: 			p.ROI = sha
194: 			return
195: 		}
196: 	}
197: 	p.Header = append([]string{line}, p.Header...)
198: 	p.ROI = sha
199: }
200: 
201: // Task returns the task with id, or nil.
202: func (p *Plan) Task(id string) *Task {
203: 	for _, t := range p.Tasks {
204: 		if t.ID == id {
205: 			return t
206: 		}
207: 	}
208: 	return nil
209: }

(End of file - total 209 lines)
</content>
```


## assistant

> 💭 I need to examine the `Selectable` and `Stale` methods in state.go, then look at cmd_basic.go to understand the hookCmd pattern and how findRoot works.

Now let me read state.go (for `Stale`/`Selectable`) and cmd_basic.go (for the `hookCmd`/root-finding pattern).

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/plan/state.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/plan/state.go</path>
<type>file</type>
<content>
1: package plan
2: 
3: import (
4: 	"fmt"
5: 	"time"
6: )
7: 
8: // transitions enumerates the legal status edges. NEEDS-HUMAN is terminal and
9: // only reachable via Reject overflow, never a direct transition.
10: var transitions = map[Status]map[Status]bool{
11: 	StatusTODO:       {StatusInProgress: true},
12: 	StatusInProgress: {StatusReview: true, StatusTODO: true},
13: 	StatusReview:     {StatusDone: true, StatusArb: true},
14: 	StatusArb:        {StatusTODO: true, StatusDone: true},
15: }
16: 
17: // CanTransition reports whether from->to is legal.
18: func CanTransition(from, to Status) bool { return transitions[from][to] }
19: 
20: // Transition moves a task to a new status, enforcing the machine. It clears the
21: // heartbeat on any terminal/non-in-progress state and stamps it on IN-PROGRESS.
22: func (t *Task) Transition(to Status, now time.Time) error {
23: 	if !CanTransition(t.Status, to) {
24: 		return fmt.Errorf("plan: illegal transition %s -> %s for %s", t.Status, to, t.ID)
25: 	}
26: 	t.Status = to
27: 	if to == StatusInProgress {
28: 		t.Heartbeat = now
29: 	} else {
30: 		t.Heartbeat = time.Time{}
31: 	}
32: 	return nil
33: }
34: 
35: // Heartbeat re-stamps an IN-PROGRESS task; error otherwise.
36: func (t *Task) HeartbeatNow(now time.Time) error {
37: 	if t.Status != StatusInProgress {
38: 		return fmt.Errorf("plan: heartbeat on non-in-progress task %s (%s)", t.ID, t.Status)
39: 	}
40: 	t.Heartbeat = now
41: 	return nil
42: }
43: 
44: // Stale reports whether an IN-PROGRESS task's heartbeat is older than ttl: a GC
45: // candidate. Non-in-progress tasks are never stale.
46: func (t *Task) Stale(now time.Time, ttl time.Duration) bool {
47: 	if t.Status != StatusInProgress || t.Heartbeat.IsZero() {
48: 		return false
49: 	}
50: 	return now.Sub(t.Heartbeat) > ttl
51: }
52: 
53: // Reject records a rejection: bumps attempts, and once attempts exceed limit the
54: // task goes NEEDS-HUMAN (no longer auto-recycled). Otherwise it returns to TODO.
55: // Valid from NEEDS-REVIEW or NEEDS-ARBITRATION.
56: func (t *Task) Reject(limit int, now time.Time) error {
57: 	if t.Status != StatusReview && t.Status != StatusArb {
58: 		return fmt.Errorf("plan: reject on non-reviewable task %s (%s)", t.ID, t.Status)
59: 	}
60: 	t.Attempts++
61: 	t.Heartbeat = time.Time{}
62: 	if t.Attempts > limit {
63: 		t.Status = StatusHuman
64: 	} else {
65: 		t.Status = StatusTODO
66: 	}
67: 	return nil
68: }
69: 
70: // Selectable reports whether a task can be auto-selected: not terminal, not
71: // NEEDS-HUMAN, and all deps DONE in p.
72: func (p *Plan) Selectable(t *Task) bool {
73: 	if t.Status == StatusDone || t.Status == StatusHuman {
74: 		return false
75: 	}
76: 	for _, d := range t.Deps {
77: 		dep := p.Task(d)
78: 		if dep == nil || dep.Status != StatusDone {
79: 			return false
80: 		}
81: 	}
82: 	return true
83: }

(End of file - total 83 lines)
</content>
```

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/cmd/beehive/cmd_basic.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/cmd/beehive/cmd_basic.go</path>
<type>file</type>
<content>
1: package main
2: 
3: import (
4: 	"fmt"
5: 
6: 	"github.com/spencerharmon/beehive/internal/config"
7: 	"github.com/spencerharmon/beehive/internal/repo"
8: 	"github.com/spf13/cobra"
9: )
10: 
11: func versionCmd() *cobra.Command {
12: 	return &cobra.Command{
13: 		Use:   "version",
14: 		Short: "print version",
15: 		Run:   func(*cobra.Command, []string) { fmt.Println("beehive dev") },
16: 	}
17: }
18: 
19: func initCmd() *cobra.Command {
20: 	return &cobra.Command{
21: 		Use:   "init <path>",
22: 		Short: "scaffold a beehive repo and install the ROI-protect hook",
23: 		Args:  cobra.ExactArgs(1),
24: 		RunE: func(_ *cobra.Command, args []string) error {
25: 			path := args[0]
26: 			if err := repo.Init(path); err != nil {
27: 				return err
28: 			}
29: 			// Hook install is best-effort: only if path is already a git repo.
30: 			_ = config.InstallROIHook(path)
31: 			fmt.Println("beehive repo at", path)
32: 			return nil
33: 		},
34: 	}
35: }
36: 
37: func hookCmd() *cobra.Command {
38: 	c := &cobra.Command{Use: "hook", Short: "git hook management"}
39: 	c.AddCommand(&cobra.Command{
40: 		Use:   "install <repo>",
41: 		Short: "install the ROI-protect pre-commit hook",
42: 		Args:  cobra.ExactArgs(1),
43: 		RunE: func(_ *cobra.Command, args []string) error {
44: 			if err := config.InstallROIHook(args[0]); err != nil {
45: 				return err
46: 			}
47: 			fmt.Println("ROI-protect hook installed")
48: 			return nil
49: 		},
50: 	})
51: 	return c
52: }

(End of file - total 52 lines)
</content>
```


## assistant

> 💭 I'm looking at how other commands locate the repository root by examining the submodule and worktree command files for a shared helper function.

Let me check how other commands locate the repo root (looking for a findRoot helper).

**🔧 bash** `export GOTMPDIR=/home/spencer/.cache/gotmp TMPDIR=/home/spencer/.cache/gotmp
SRC=/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo
cd "$SRC"
rg -n "findRoot|repo.Open|os.Getwd|repo.Open\(" cmd/ internal/swarm/ internal/honeybee/ 2>/dev/null | head -40`

```
cmd/beehive/cmd_submodule.go:16:// findRoot ascends from cwd until AGENTS.md is found.
cmd/beehive/cmd_submodule.go:17:func findRoot() (string, error) {
cmd/beehive/cmd_submodule.go:18:	d, err := os.Getwd()
cmd/beehive/cmd_submodule.go:47:			root, err := findRoot()
cmd/beehive/cmd_submodule.go:79:			root, err := findRoot()
cmd/beehive/cmd_submodule.go:108:			root, err := findRoot()
cmd/beehived/main.go:24:	r, err := repo.Open(*root)
cmd/beehive/cmd_secret.go:18:		root, err := findRoot()
cmd/honeybee/main.go:39:	rp, err := repo.Open(root)
internal/swarm/swarm_test.go:66:	rp, _ := repo.Open(root)
internal/swarm/swarm_test.go:98:	rp, _ := repo.Open(root)
cmd/beehive/cmd_worktree.go:15:		root, err := findRoot()

```


## assistant

> 💭 I need to look at the `findRoot()` function in `cmd_submodule.go` to see what it does and how it's structured.

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/cmd/beehive/cmd_submodule.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/cmd/beehive/cmd_submodule.go</path>
<type>file</type>
<content>
1: package main
2: 
3: import (
4: 	"fmt"
5: 	"os"
6: 	"path/filepath"
7: 	"strings"
8: 
9: 	"github.com/spencerharmon/beehive/internal/git"
10: 	"github.com/spencerharmon/beehive/internal/links"
11: 	"github.com/spencerharmon/beehive/internal/plan"
12: 	"github.com/spencerharmon/beehive/internal/repo"
13: 	"github.com/spf13/cobra"
14: )
15: 
16: // findRoot ascends from cwd until AGENTS.md is found.
17: func findRoot() (string, error) {
18: 	d, err := os.Getwd()
19: 	if err != nil {
20: 		return "", err
21: 	}
22: 	for {
23: 		if _, err := os.Stat(filepath.Join(d, repo.AgentsFile)); err == nil {
24: 			return d, nil
25: 		}
26: 		p := filepath.Dir(d)
27: 		if p == d {
28: 			return "", fmt.Errorf("not inside a beehive repo (no %s found)", repo.AgentsFile)
29: 		}
30: 		d = p
31: 	}
32: }
33: 
34: func submoduleCmd() *cobra.Command {
35: 	c := &cobra.Command{Use: "submodule", Short: "manage beehive submodules"}
36: 	c.AddCommand(submoduleAddCmd(), submoduleLinkCmd(), submodulePlanCmd())
37: 	return c
38: }
39: 
40: func submoduleAddCmd() *cobra.Command {
41: 	var name, branch string
42: 	c := &cobra.Command{
43: 		Use:   "add <repo-url>",
44: 		Short: "add a target repo as a tracked submodule (dormant until ROI.md exists)",
45: 		Args:  cobra.ExactArgs(1),
46: 		RunE: func(cmd *cobra.Command, args []string) error {
47: 			root, err := findRoot()
48: 			if err != nil {
49: 				return err
50: 			}
51: 			url := args[0]
52: 			if name == "" {
53: 				name = strings.TrimSuffix(filepath.Base(url), ".git")
54: 			}
55: 			subdir := filepath.Join(root, "submodules", name)
56: 			if err := os.MkdirAll(filepath.Join(subdir, "worktrees"), 0o755); err != nil {
57: 				return err
58: 			}
59: 			g := git.New(root)
60: 			rel := filepath.Join("submodules", name, "repo")

(Showing lines 1-60 of 122. Use offset=61 to continue.)
</content>
```


## assistant

> 💭 I need to implement the lint command in a new file, wire it into the main entry point, and extend the ROI hook to check for dependency cycles. I'm also planning to add tests for these new features. Let me work through the hook design first to make sure it handles the validation properly.
> 
> The hook currently prevents honeybees from modifying ROI.md, but I need to extend it to also reject commits that create dependency cycles in PLAN.md or SUBMODULE-LINKS.yaml. The key insight is that the ROI protection should only apply to honeybees, but the cycle check should run for everyone since an invalid cycle is a problem regardless of who introduces it.
> 
> I'm restructuring the hook so the honeybee identity check only guards the ROI portion, then adding a separate cycle validation step that runs whenever those dependency files are staged. The cycle check will invoke `beehive lint` to validate the dependency graph, and it applies universally rather than being gated by identity.
> 
> I'm realizing there's a subtlety with `beehive lint` — it validates the working tree, not the staged content. For the honeybee flow this is fine since `claim.go` writes the file then commits everything together, so working tree and staged content match. For general robustness, validating the working tree is the pragmatic choice and aligns with the actual flow, so I'll document that as the expected behavior.
> 
> The hook runs from the repo root where the commit happens, and `beehive lint` uses `findRoot()` to locate AGENTS.md, so it'll work correctly from anywhere in the repo. The existing hook test just checks that the hook file exists and is executable without actually running it, so my changes won't break that.
> 
> Now I need to write the `beehive lint` subcommand...
> 
> Writing lint command...
> 
> After that, I'll need to add tests across several files: `links_test.go` for cycle detection, a new `deps_test.go` for dependency parsing and qualification, `select_test.go` for cross-submodule gating and cycle rejection, and maybe enhance `hook_test.go` to verify the hook content actually includes the lint invocation. Let me look at the existing test structure to match their conventions.

I now have full context. Let me check existing test files to follow conventions before writing new code/tests.

**🔧 bash** `export GOTMPDIR=/home/spencer/.cache/gotmp TMPDIR=/home/spencer/.cache/gotmp
SRC=/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-links-graph-enforcement/submodules/beehive/repo
SRC=/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo
cd "$SRC"
echo "=== test files ==="; ls internal/links/ internal/plan/ internal/select/
echo "=== links_test exists? ==="; ls internal/links/*_test.go 2>/dev/null || echo none`

```
=== test files ===
internal/links/:
links.go
links_test.go

internal/plan/:
compat.go
deps.go
plan.go
plan_test.go
rollback.go
state.go
testdata

internal/select/:
graph.go
select.go
select_test.go
=== links_test exists? ===
internal/links/links_test.go

```


## assistant

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/links/links_test.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/links/links_test.go</path>
<type>file</type>
<content>
1: package links
2: 
3: import (
4: 	"path/filepath"
5: 	"testing"
6: )
7: 
8: func TestCycleDetection(t *testing.T) {
9: 	l := &Links{}
10: 	if err := l.AddDep("a", "b"); err != nil {
11: 		t.Fatal(err)
12: 	}
13: 	if err := l.AddDep("b", "c"); err != nil {
14: 		t.Fatal(err)
15: 	}
16: 	if err := l.AddDep("c", "a"); err == nil {
17: 		t.Fatal("cycle c->a not rejected")
18: 	}
19: 	if len(l.Deps) != 2 {
20: 		t.Fatalf("cyclic edge retained: %v", l.Deps)
21: 	}
22: 	if l.HasCycle() {
23: 		t.Fatal("graph reported cyclic after rejection")
24: 	}
25: 	if err := l.AddDep("a", "a"); err == nil {
26: 		t.Fatal("self-dep allowed")
27: 	}
28: }
29: 
30: func TestLinkSubmodulesIdempotent(t *testing.T) {
31: 	l := &Links{}
32: 	l.LinkSubmodules("x", "y")
33: 	l.LinkSubmodules("x", "y")
34: 	if len(l.Submodules) != 2 {
35: 		t.Fatalf("dupes: %v", l.Submodules)
36: 	}
37: }
38: 
39: func TestRoundTrip(t *testing.T) {
40: 	p := filepath.Join(t.TempDir(), LinksName())
41: 	l := &Links{}
42: 	l.LinkSubmodules("b", "a")
43: 	l.AddDep("a", "b")
44: 	if err := l.Save(p); err != nil {
45: 		t.Fatal(err)
46: 	}
47: 	got, err := Load(p)
48: 	if err != nil {
49: 		t.Fatal(err)
50: 	}
51: 	if len(got.Submodules) != 2 || got.Submodules[0] != "a" {
52: 		t.Fatalf("submodules=%v", got.Submodules)
53: 	}
54: 	if len(got.Deps) != 1 || got.Deps[0].From != "a" || got.Deps[0].To != "b" {
55: 		t.Fatalf("deps=%v", got.Deps)
56: 	}
57: }
58: 
59: func LinksName() string { return "SUBMODULE-LINKS.yaml" }

(End of file - total 59 lines)
</content>
```

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/select/select_test.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/select/select_test.go</path>
<type>file</type>
<content>
1: package selectt
2: 
3: import (
4: 	"context"
5: 	"math/rand"
6: 	"os"
7: 	"path/filepath"
8: 	"testing"
9: 	"time"
10: 
11: 	"github.com/spencerharmon/beehive/internal/git"
12: 	"github.com/spencerharmon/beehive/internal/repo"
13: )
14: 
15: func hive(t *testing.T) (*repo.Repo, *git.Repo, string) {
16: 	t.Helper()
17: 	root := t.TempDir()
18: 	ctx := context.Background()
19: 	g := git.New(root)
20: 	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
21: 		g.Run(ctx, a...)
22: 	}
23: 	repo.Init(root)
24: 	rp, _ := repo.Open(root)
25: 	return rp, g, root
26: }
27: 
28: func sub(root, name string, files map[string]string) {
29: 	d := filepath.Join(root, "submodules", name)
30: 	os.MkdirAll(d, 0o755)
31: 	for f, b := range files {
32: 		os.WriteFile(filepath.Join(d, f), []byte(b), 0o644)
33: 	}
34: }
35: 
36: func sel(root string, g *git.Repo) *Selector {
37: 	rp, _ := repo.Open(root)
38: 	return &Selector{Repo: rp, Git: g, Rand: rand.New(rand.NewSource(1)), TTL: time.Hour}
39: }
40: 
41: func TestSelectWork(t *testing.T) {
42: 	_, g, root := hive(t)
43: 	sub(root, "a", map[string]string{"ROI.md": "x", "PLAN.md": "## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"})
44: 	g.Commit(context.Background(), "seed")
45: 	head, _ := g.LastCommit(context.Background(), "submodules/a/ROI.md")
46: 	os.WriteFile(filepath.Join(root, "submodules/a/PLAN.md"), []byte("<!-- Beehive-ROI: "+head+" -->\n## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
47: 	g.Commit(context.Background(), "stamp")
48: 	s, err := sel(root, g).Select(context.Background())
49: 	if err != nil || s == nil {
50: 		t.Fatalf("sel %v %v", s, err)
51: 	}
52: 	if s.Kind != Work || s.Task.ID != "T1" {
53: 		t.Fatalf("got %+v", s)
54: 	}
55: }
56: 
57: func TestDormantSkipped(t *testing.T) {
58: 	_, g, root := hive(t)
59: 	sub(root, "a", map[string]string{}) // no ROI -> dormant
60: 	g.Commit(context.Background(), "seed")
61: 	s, _ := sel(root, g).Select(context.Background())
62: 	if s != nil {
63: 		t.Fatalf("dormant selected: %+v", s)
64: 	}
65: }
66: 
67: func TestBootstrap(t *testing.T) {
68: 	_, g, root := hive(t)
69: 	sub(root, "a", map[string]string{"ROI.md": "x"}) // ROI no PLAN
70: 	g.Commit(context.Background(), "seed")
71: 	s, _ := sel(root, g).Select(context.Background())
72: 	if s == nil || s.Kind != Bootstrap {
73: 		t.Fatalf("want bootstrap, got %+v", s)
74: 	}
75: }
76: 
77: func TestReconcilePriority0(t *testing.T) {
78: 	_, g, root := hive(t)
79: 	// PLAN stamped to an old sha but ROI committed later -> drift.
80: 	sub(root, "a", map[string]string{"ROI.md": "x", "PLAN.md": "<!-- Beehive-ROI: dead -->\n## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"})
81: 	g.Commit(context.Background(), "seed")
82: 	s, _ := sel(root, g).Select(context.Background())
83: 	if s == nil || s.Kind != Reconcile || s.DiffRange == "" {
84: 		t.Fatalf("want reconcile, got %+v", s)
85: 	}
86: }

(End of file - total 86 lines)
</content>
```


## assistant

> 💭 I should look at the existing test patterns in `plan_test.go` to see how they're constructing tasks and plans, and understand what `TestSelectable` expects so I don't inadvertently break it.

Let me check the plan_test.go to follow its conventions (and confirm TestSelectable usage).

**🔧 bash** `export GOTMPDIR=/home/spencer/.cache/gotmp TMPDIR=/home/spencer/.cache/gotmp
SRC=/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo
cd "$SRC"
rg -n "func Test|Candidates|Selectable|func.*Plan|parseTasks|Parse\(" internal/plan/plan_test.go | head -40`

```
23:func TestParseRoundTrip(t *testing.T) {
24:	p, err := Parse(sample)
46:func TestBadStatus(t *testing.T) {
47:	if _, err := Parse("## x [BOGUS] <!-- attempts=0 deps= -->\n"); err == nil {
52:func TestStateMachine(t *testing.T) {
75:func TestStaleHeartbeat(t *testing.T) {
91:func TestRejectAttempts(t *testing.T) {
110:func TestSelectable(t *testing.T) {
111:	p, _ := Parse(sample)
112:	if p.Selectable(p.Task("t2")) {
116:	if !p.Selectable(p.Task("t2")) {
121:func TestGolden(t *testing.T) {
123:	p, _ := Parse(sample)

```


## assistant

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/plan/plan_test.go`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/plan/plan_test.go</path>
<type>file</type>
<content>
1: package plan
2: 
3: import (
4: 	"os"
5: 	"path/filepath"
6: 	"testing"
7: 	"time"
8: )
9: 
10: const sample = `<!-- Beehive-ROI: abc123 -->
11: # Plan
12: 
13: ## t1 [TODO] <!-- attempts=0 deps= -->
14: do the first thing
15: 
16: ## t2 [IN-PROGRESS] <!-- attempts=1 deps=t1 heartbeat=2026-06-29T10:00:00Z -->
17: second, depends on t1
18: 
19: ## t3 [NEEDS-REVIEW] <!-- attempts=2 deps= -->
20: ready for review
21: `
22: 

(Showing lines 1-22 of 139. Use offset=23 to continue.)
</content>
```


## assistant

> 💭 I'll start by running a build check to make sure the existing internal packages compile properly, then move forward with implementing the new code and writing tests to cover it.

I have everything I need. Let me set up a todo list and verify the current build compiles.

**🔧 todowrite** `{"todos":[{"content":"Verify internal packages build green (baseline)","priority":"high","status":"in_progress"},{"content":"Create cmd/beehive/cmd_lint.go + wire lintCmd in main.go","priority":"high"…`

```
[
  {
    "content": "Verify internal packages build green (baseline)",
    "status": "in_progress",
    "priority": "high"
  },
  {
    "content": "Create cmd/beehive/cmd_lint.go + wire lintCmd in main.go",
    "status": "pending",
    "priority": "high"
  },
  {
    "content": "Extend roiHook in config/hook.go with dep-cycle guard",
    "status": "pending",
    "priority": "high"
  },
  {
    "content": "Write links tests (CyclicNodes, Cycle, HasCycleWith)",
    "status": "pending",
    "priority": "high"
  },
  {
    "content": "Write plan tests (SplitDep, Qualify, SelectableExt, CandidatesExt)",
    "status": "pending",
    "priority": "high"
  },
  {
    "content": "Write select tests (cross-sub gating, cyclic skip, Validate)",
    "status": "pending",
    "priority": "high"
  },
  {
    "content": "Write config hook_test content assertions",
    "status": "pending",
    "priority": "medium"
  },
  {
    "content": "go test ./... green",
    "status": "pending",
    "priority": "high"
  },
  {
    "content": "Write change doc under docs/",
    "status": "pending",
    "priority": "high"
  },
  {
    "content": "Commit source (stamped) + bump outer pointer; flip PLAN.md to NEEDS-REVIEW",
    "status": "pending",
    "priority": "high"
  }
]
```


## assistant

**🔧 bash** `export GOTMPDIR=/home/spencer/.cache/gotmp TMPDIR=/home/spencer/.cache/gotmp CGO_ENABLED=0
SRC=/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo
cd "$SRC"
go build ./... 2>&1 | head -30 && echo "BUILD_EXIT=$?"`

```
BUILD_EXIT=0

```


## assistant

> 💭 The build is passing, so I'm ready to implement the lint command. I need to create the lint command file, integrate it into the main entry point, and extend the roiHook configuration to support the new functionality.

Build is green. Now let me create the lint command, wire it in, and extend the hook.

**🔧 write** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/cmd/beehive/cmd_lint.go`

```
Wrote file successfully.
```


## assistant

**🔧 edit** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/cmd/beehive/main.go`

```
Edit applied successfully.
```


## assistant

> 💭 I need to refactor the `roiHook` so the ROI protection only applies to honeybee identity without short-circuiting the entire hook, while the dependency cycle check runs for everyone when PLAN.md or SUBMODULE-LINKS.yaml is staged (as a best-effort check if the beehive binary is available). I'll also update the constant name to better reflect its expanded purpose.

Now extend the pre-commit hook. First check where the `roiHook` const is referenced.

**🔧 bash** `export GOTMPDIR=/home/spencer/.cache/gotmp TMPDIR=/home/spencer/.cache/gotmp
SRC=/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo
cd "$SRC"
rg -n "roiHook|InstallROIHook|preCommit" --type go`

```
cmd/beehive/cmd_basic.go:30:			_ = config.InstallROIHook(path)
cmd/beehive/cmd_basic.go:44:			if err := config.InstallROIHook(args[0]); err != nil {
internal/config/hook_test.go:9:func TestInstallROIHook(t *testing.T) {
internal/config/hook_test.go:11:	if err := InstallROIHook(root); err == nil {
internal/config/hook_test.go:17:	if err := InstallROIHook(root); err != nil {
internal/config/hook.go:9:// roiHook rejects commits that touch ROI.md when authored by a honeybee identity.
internal/config/hook.go:11:const roiHook = `#!/usr/bin/env sh
internal/config/hook.go:23:// InstallROIHook writes the ROI-protect pre-commit hook into the repo's .git dir.
internal/config/hook.go:24:func InstallROIHook(repoRoot string) error {
internal/config/hook.go:33:	return os.WriteFile(p, []byte(roiHook), 0o755)

```


## assistant

> 💭 I'm renaming the internal `roiHook` constant to `preCommitHook` while keeping the `InstallROIHook` function name for backward compatibility, then updating the hook's implementation to guard against dependency cycles by checking if PLAN.md or SUBMODULE-LINKS.yaml are staged before running.
> 
> Writing the hook implementation...

**🔧 edit** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/config/hook.go`

```
Edit applied successfully.
```


## assistant

Now build the cmd + config packages to confirm wiring compiles.

**🔧 bash** `export GOTMPDIR=/home/spencer/.cache/gotmp TMPDIR=/home/spencer/.cache/gotmp CGO_ENABLED=0
SRC=/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo
cd "$SRC"
go build ./... 2>&1 | head -30 && echo "BUILD_EXIT=${PIPESTATUS[0]}"
go vet ./cmd/... ./internal/config/... ./internal/select/... 2>&1 | head -20`

```
BUILD_EXIT=0

```


## assistant

> 💭 The build and vet checks are passing, so I'm moving forward with marking the first couple of todos as complete and then diving into writing the tests.

Build green. Now the tests. Let me update todos and write the links tests first.

**🔧 todowrite** `{"todos":[{"content":"Verify internal packages build green (baseline)","priority":"high","status":"completed"},{"content":"Create cmd/beehive/cmd_lint.go + wire lintCmd in main.go","priority":"high","…`

```
[
  {
    "content": "Verify internal packages build green (baseline)",
    "status": "completed",
    "priority": "high"
  },
  {
    "content": "Create cmd/beehive/cmd_lint.go + wire lintCmd in main.go",
    "status": "completed",
    "priority": "high"
  },
  {
    "content": "Extend roiHook in config/hook.go with dep-cycle guard",
    "status": "completed",
    "priority": "high"
  },
  {
    "content": "Write links tests (CyclicNodes, Cycle, HasCycleWith)",
    "status": "in_progress",
    "priority": "high"
  },
  {
    "content": "Write plan tests (SplitDep, Qualify, SelectableExt, CandidatesExt)",
    "status": "pending",
    "priority": "high"
  },
  {
    "content": "Write select tests (cross-sub gating, cyclic skip, Validate)",
    "status": "pending",
    "priority": "high"
  },
  {
    "content": "Write config hook_test content assertions",
    "status": "pending",
    "priority": "medium"
  },
  {
    "content": "go test ./... green",
    "status": "pending",
    "priority": "high"
  },
  {
    "content": "Write change doc under docs/",
    "status": "pending",
    "priority": "high"
  },
  {
    "content": "Commit source (stamped) + bump outer pointer; flip PLAN.md to NEEDS-REVIEW",
    "status": "pending",
    "priority": "high"
  }
]
```


## assistant

> 💭 I'm setting up test cases for the graph cycle detection functions—testing that `Cycle` returns the correct node sequence for cyclic graphs and nil for acyclic ones, `CyclicNodes` identifies all nodes involved in cycles while excluding isolated ones, and `HasCycleWith` checks for cycles involving specific nodes.

**🔧 write** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/links/cycle_test.go`

```
Wrote file successfully.
```


## assistant

**🔧 bash** `export GOTMPDIR=/home/spencer/.cache/gotmp TMPDIR=/home/spencer/.cache/gotmp CGO_ENABLED=0
SRC=/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo
cd "$SRC"
go test ./internal/links/... 2>&1 | tail -20`

```
ok  	github.com/spencerharmon/beehive/internal/links	0.003s

```


## assistant

> 💭 The links tests are passing, so now I'm moving on to write the plan package tests in `deps_test.go`. I need to cover `Qualify` and `SplitDep`, then test `SelectableExt` with scenarios around local dependency resolution within the plan, cross-submodule dependencies through extensions, cyclic exclusion, and how nil extensions block cross-module dependencies. I also need to test `CandidatesExt` for tier priority, cyclic exclusion, and cross-submodule gating. I'm planning to construct test cases by building a Plan through Parse for convenience, or by building it manually.
> 
> For `SelectableExt`, I'll set up a plan called "self" with tasks where t1 is done, t2 depends on t1 (selectable), and t3 depends on both t1 and an external dependency. The extension resolver will return done for the external dependency, making t3 selectable only when the extension provides that resolution. I'll also test cyclic sets blocking selectability.
> 
> For `CandidatesExt`, I need to verify tier ordering with stale in-progress tasks, arbitrary tasks, review tasks, and todos, plus confirm that cyclic review tasks get excluded. For `Qualify` and `SplitDep`, the logic is straightforward: `Qualify` concatenates with a colon, and `SplitDep` splits on the first colon only. I'm constructing test data using Parse with heartbeats in the past to simulate staleness.
> 
> Writing the test file...

Links pass. Now the plan tests.

**🔧 write** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/plan/deps_test.go`

```
Wrote file successfully.
```


## assistant

> 💭 I need to clean up that unused import and variable declaration I added as a workaround.

I added an unused `reflect` import. Let me remove it.

**🔧 edit** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/plan/deps_test.go`

```
Edit applied successfully.
```


## assistant

**🔧 edit** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/plan/deps_test.go`

```
Edit applied successfully.
```


## assistant

**🔧 bash** `export GOTMPDIR=/home/spencer/.cache/gotmp TMPDIR=/home/spencer/.cache/gotmp CGO_ENABLED=0
SRC=/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo
cd "$SRC"
go test ./internal/plan/... 2>&1 | tail -20`

```
ok  	github.com/spencerharmon/beehive/internal/plan	0.002s

```


## assistant

> 💭 Now I'm moving on to write the select tests, focusing on cross-submodule gating where a task in one submodule depends on another submodule's task, and the dependency is only satisfied when that task reaches DONE status. I also need to test what happens when cross-module dependencies aren't properly linked.
> 
> I'll also need to handle cyclic dependencies across submodules—where A depends on B and B depends back on A—which should exclude both from selection. The `Validate` function should catch these cycles and reject them while accepting acyclic graphs.
> 
> The tricky part is that to reach the candidate stage in selection, each submodule needs ROI.md and PLAN.md files, with PLAN.md stamped to match ROI.md's commit. I'm going to replicate the pattern from TestSelectWork where they commit, read the last commit of ROI.md, and write that stamp into PLAN.md to avoid triggering reconcile. I should probably write a helper to set this up cleanly for multiple submodules.
> 
> Writing helper function... I'm setting up a test case to verify cross-submodule dependency gating. When submodule b's task is still TODO, selecting a task should return b's task (not a's blocked task), proving that a's task is properly gated. When b's task is marked DONE, then a's task becomes selectable since its dependency is satisfied.
> 
> For the unlinked case, even if b's task is DONE, if a doesn't list b in its SUBMODULE-LINKS.yaml, the dependency remains unresolved and a's task stays blocked—Select returns nil.
> 
> The cyclic case tests that when a and b depend on each other, both tasks get excluded and Select returns nil, confirming the cycle detection works without deadlocking.
> 
> For the Validate test, I'm checking that a cyclic setup triggers an error while an acyclic one passes. Validate doesn't need task stamps since it only reads the PLAN files and links to build the dependency graph and check for cycles.
> 
> Now I'm writing the select tests in a new graph_test.go file. I need to be careful with the SUBMODULE-LINKS.yaml format—it has a `submodules` list and a `deps` list. For the cross-gating test, I'll need a stamp helper to mark tasks as complete. Both a and b's PLAN files need stamping so they don't trigger reconciliation.
> 
> Writing test file...

Plan tests pass. Now the select package tests (cross-submodule gating, cyclic skip, Validate).

**🔧 write** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/select/graph_test.go`

```
Wrote file successfully.
```


## assistant

> 💭 I need to verify the `Commit` method signature in the `git.Repo` type to ensure it matches what my `stampPlans` helper expects. Looking at the test usage, `Commit` is called without capturing a return value, which suggests it might return an error that's being ignored.

Let me verify the `git.Repo` method signatures match my interface in the helper.

**🔧 bash** `export GOTMPDIR=/home/spencer/.cache/gotmp TMPDIR=/home/spencer/.cache/gotmp
SRC=/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo
cd "$SRC"
rg -n "func \(.*Repo\) (Commit|LastCommit)" internal/git/git.go`

```
51:func (r *Repo) Commit(ctx context.Context, msg string) error {
96:func (r *Repo) LastCommit(ctx context.Context, path string) (string, error) {

```


## assistant

> 💭 I'm reconsidering whether the interface approach is necessary here — I could simplify by using `*git.Repo` directly since that's what the helper returns anyway, which would be cleaner and more consistent with the existing test patterns.

The interface works but is awkward. Let me simplify to use `*git.Repo` directly for consistency.

**🔧 edit** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/select/graph_test.go`

```
Edit applied successfully.
```


## assistant

**🔧 bash** `export GOTMPDIR=/home/spencer/.cache/gotmp TMPDIR=/home/spencer/.cache/gotmp CGO_ENABLED=0
SRC=/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo
cd "$SRC"
gofmt -w internal/select/graph_test.go internal/plan/deps_test.go internal/links/cycle_test.go
go test ./internal/select/... 2>&1 | tail -30`

```
ok  	github.com/spencerharmon/beehive/internal/select	0.234s

```


## assistant

> 💭 I'm adding a new test to verify the hook content includes the specific guards we need—the ROI check and the dependency cycle lint invocation—to lock in the expected behavior.

Select tests pass. Now add content assertions to the config hook test (currently it only checks existence/executable).

**🔧 edit** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/config/hook_test.go`

```
Edit applied successfully.
```


## assistant

**🔧 edit** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/internal/config/hook_test.go`

```
Edit applied successfully.
```


## assistant

> 💭 Running the full test suite with the environment variables to verify everything passes.

Now run the full test suite plus vet.

**🔧 bash** `export GOTMPDIR=/home/spencer/.cache/gotmp TMPDIR=/home/spencer/.cache/gotmp CGO_ENABLED=0
SRC=/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo
cd "$SRC"
go test ./... 2>&1 | tail -40
echo "=== VET ==="
go vet ./... 2>&1 | tail -20`

```
?   	github.com/spencerharmon/beehive/cmd/beehive	[no test files]
?   	github.com/spencerharmon/beehive/cmd/beehived	[no test files]
?   	github.com/spencerharmon/beehive/cmd/honeybee	[no test files]
ok  	github.com/spencerharmon/beehive/internal/claim	0.190s
ok  	github.com/spencerharmon/beehive/internal/config	0.004s
ok  	github.com/spencerharmon/beehive/internal/git	0.086s
ok  	github.com/spencerharmon/beehive/internal/links	(cached)
ok  	github.com/spencerharmon/beehive/internal/plan	(cached)
ok  	github.com/spencerharmon/beehive/internal/repo	0.006s
ok  	github.com/spencerharmon/beehive/internal/secrets	0.345s
ok  	github.com/spencerharmon/beehive/internal/select	(cached)
ok  	github.com/spencerharmon/beehive/internal/swarm	0.187s
ok  	github.com/spencerharmon/beehive/internal/web	0.107s
?   	github.com/spencerharmon/beehive/prompts	[no test files]
=== VET ===

```


## assistant

> 💭 All tests are passing and the code is clean, so now I need to do a full end-to-end verification by actually building the binary and testing the cycle rejection in a real scenario—setting up a test repo with a dependency cycle, installing the hook, and attempting to commit to confirm the hook properly invokes the lint check and blocks the commit as expected. Let me just build the binary with `go build` and set GOBIN, then run the end-to-end test.

All green, vet clean. Now let me do an end-to-end verification of the acceptance criteria: build the binary and prove `beehive lint` + the pre-commit hook actually reject a cycle-forming commit.

**🔧 bash** `export GOTMPDIR=/home/spencer/.cache/gotmp TMPDIR=/home/spencer/.cache/gotmp CGO_ENABLED=0
SRC=/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo
cd "$SRC"
mkdir -p /home/spencer/.cache/gotmp
go build -o /home/spencer/.cache/gotmp/beehive ./cmd/beehive && echo "BUILT"
/home/spencer/.cache/gotmp/beehive --help 2>&1 | rg -i "lint|hook"`

```
BUILT
  hook        git hook management
  init        scaffold a beehive repo and install the ROI-protect hook
  lint        validate the cross-submodule dependency graph (reject wait cycles)

```


## assistant

**🔧 bash** `export PATH="/home/spencer/.cache/gotmp:$PATH"
WORK=$(mktemp -d /home/spencer/.cache/gotmp/e2e.XXXXXX)
cd "$WORK"
git init -q -b main
git config user.email t@t; git config user.name t
beehive init . >/dev/null 2>&1 || true
ls -a
echo "=== AGENTS present? ==="; test -f AGENTS.md && echo yes || echo no
echo "=== hook present? ==="; test -f .git/hooks/pre-commit && echo yes || echo no
echo "WORK=$WORK"`

```
.
..
AGENTS.md
.git
INFRASTRUCTURE.md
submodules
=== AGENTS present? ===
yes
=== hook present? ===
yes
WORK=/home/spencer/.cache/gotmp/e2e.QKkekY

```


## assistant

> 💭 Now I'm setting up a test scenario with the `beehive init` command to scaffold the project structure, then creating two submodules that depend on each other cyclically to verify the linting catches this issue, followed by attempting a commit as the honeybee user to confirm the pre-commit hook rejects it, and finally fixing the dependency and confirming the commit succeeds.

Now create a cyclic cross-submodule dependency and verify lint + the hook reject it.

**🔧 bash** `export PATH="/home/spencer/.cache/gotmp:$PATH"
WORK=$(ls -d /home/spencer/.cache/gotmp/e2e.* | head -1)
cd "$WORK"
mkdir -p submodules/a submodules/b
printf '## at [TODO] <!-- attempts=0 deps=b:bt -->\n' > submodules/a/PLAN.md
printf 'submodules:\n  - b\n' > submodules/a/SUBMODULE-LINKS.yaml
printf '## bt [TODO] <!-- attempts=0 deps=a:at -->\n' > submodules/b/PLAN.md
printf 'submodules:\n  - a\n' > submodules/b/SUBMODULE-LINKS.yaml

echo "=== lint (expect FAIL, cycle) ==="
beehive lint; echo "lint_exit=$?"

echo "=== commit as honeybee (expect REJECT) ==="
git add -A
BEEHIVE_HONEYBEE=1 git commit -q -m "add cyclic deps" 2>&1; echo "commit_exit=$?"`

```
=== lint (expect FAIL, cycle) ===
beehive: dependency wait cycle: a:at -> b:bt -> a:at
lint_exit=1
=== commit as honeybee (expect REJECT) ===
beehive: dependency wait cycle: a:at -> b:bt -> a:at
beehive: commit rejected: dependency wait cycle
commit_exit=1

```


## assistant

> 💭 Now I'm testing the happy path by removing the cycle and verifying that both lint and the commit hook pass successfully.

Cycle correctly rejected by both `lint` and the hook. Now verify the happy path: break the cycle and confirm commit succeeds.

**🔧 bash** `export PATH="/home/spencer/.cache/gotmp:$PATH"
WORK=$(ls -d /home/spencer/.cache/gotmp/e2e.* | head -1)
cd "$WORK"
# Break the cycle: b:bt no longer depends on a:at.
printf '## bt [TODO] <!-- attempts=0 deps= -->\n' > submodules/b/PLAN.md

echo "=== lint (expect OK) ==="
beehive lint; echo "lint_exit=$?"

echo "=== commit as honeybee (expect SUCCESS) ==="
git add -A
BEEHIVE_HONEYBEE=1 git commit -q -m "acyclic deps" 2>&1; echo "commit_exit=$?"

echo "=== ROI-protect still works: honeybee edits ROI.md (expect REJECT) ==="
printf 'intent\n' > submodules/a/ROI.md
git add -A
BEEHIVE_HONEYBEE=1 git commit -q -m "touch ROI" 2>&1; echo "commit_exit=$?"
# cleanup
cd /; rm -rf "$WORK"`

```
=== lint (expect OK) ===
beehive: dependency graph OK
lint_exit=0
=== commit as honeybee (expect SUCCESS) ===
beehive: dependency graph OK
commit_exit=0
=== ROI-protect still works: honeybee edits ROI.md (expect REJECT) ===
beehive: honeybee identity may not modify ROI.md
commit_exit=1

```


## assistant

> 💭 I've verified all the acceptance criteria across the test suite: the linked-submodule dependency gates are working correctly, commits that would create cycles are being rejected at both the hook and lint stages, cyclic tasks are properly excluded from selection to prevent deadlocks, and the ROI protection mechanism remains intact.
> 
> Now I need to clean up the e2e binary and then move on to updating the documentation and marking the test todos as complete.

All acceptance criteria verified end-to-end. Let me update todos and write the change doc.

**🔧 todowrite** `{"todos":[{"content":"Verify internal packages build green (baseline)","priority":"high","status":"completed"},{"content":"Create cmd/beehive/cmd_lint.go + wire lintCmd in main.go","priority":"high","…`

```
[
  {
    "content": "Verify internal packages build green (baseline)",
    "status": "completed",
    "priority": "high"
  },
  {
    "content": "Create cmd/beehive/cmd_lint.go + wire lintCmd in main.go",
    "status": "completed",
    "priority": "high"
  },
  {
    "content": "Extend roiHook in config/hook.go with dep-cycle guard",
    "status": "completed",
    "priority": "high"
  },
  {
    "content": "Write links tests (CyclicNodes, Cycle, HasCycleWith)",
    "status": "completed",
    "priority": "high"
  },
  {
    "content": "Write plan tests (SplitDep, Qualify, SelectableExt, CandidatesExt)",
    "status": "completed",
    "priority": "high"
  },
  {
    "content": "Write select tests (cross-sub gating, cyclic skip, Validate)",
    "status": "completed",
    "priority": "high"
  },
  {
    "content": "Write config hook_test content assertions",
    "status": "completed",
    "priority": "medium"
  },
  {
    "content": "go test ./... green + e2e lint/hook verified",
    "status": "completed",
    "priority": "high"
  },
  {
    "content": "Write change doc under docs/",
    "status": "in_progress",
    "priority": "high"
  },
  {
    "content": "Commit source (stamped) + bump outer pointer; flip PLAN.md to NEEDS-REVIEW",
    "status": "pending",
    "priority": "high"
  }
]
```


## assistant

> 💭 I need to figure out the correct naming convention for my change documentation. Based on the protocol, the format should be `<branch>-<taskid>`, so with my branch being `bee-links-graph-enforcement` and task ID `links-graph-enforcement`, the doc should be named `docs/bee-links-graph-enforcement-links-graph-enforcement.md`. Let me look at the existing docs structure and task doc to confirm this is right.

Let me check the docs structure and the existing task design doc for context/format.

**🔧 bash** `SRC=/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo
cd "$SRC"
echo "=== docs/ ==="; ls docs/ 2>/dev/null
echo "=== docs/tasks (sample) ==="; ls docs/tasks/ 2>/dev/null | head
echo "=== any change docs at docs root? ==="; ls docs/*.md 2>/dev/null | head`

```
=== docs/ ===
cli.md
frontend-components.md
honeybee.md
install.md
opencode.md
README.md
RELEASE-NOTES-TEMPLATE.md
repo-layout.md
secrets.md
=== docs/tasks (sample) ===
=== any change docs at docs root? ===
docs/cli.md
docs/frontend-components.md
docs/honeybee.md
docs/install.md
docs/opencode.md
docs/README.md
docs/RELEASE-NOTES-TEMPLATE.md
docs/repo-layout.md
docs/secrets.md

```


## assistant

> 💭 I'm noticing that the `docs/tasks/` directory doesn't actually exist yet, even though PLAN.md references task files there. I mentioned in my summary that `submodules/beehive/docs/tasks/links-graph-enforcement.md` exists as a task design doc, but I should verify whether it's actually there.

**🔧 bash** `SRC=/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo
cd "$SRC"
test -d docs/tasks && echo "docs/tasks EXISTS" || echo "docs/tasks MISSING"
test -f docs/tasks/links-graph-enforcement.md && echo "task doc EXISTS" || echo "task doc MISSING"
echo "=== README link style ==="; rg -n "Beehive:|change doc|docs/" README.md 2>/dev/null | head`

```
docs/tasks MISSING
task doc MISSING
=== README link style ===

```


## assistant

> 💭 I see that the `docs/tasks/` directory doesn't exist and the design doc was never created—the PLAN.md just references it as a future item. According to the protocol, each plan item needs a terse design doc under `docs/`, so I'll use my change doc as that documentation. The protocol specifies the doc should be named with the branch and task ID, covering the how/why, tests, follow-ups, and caveats, so I'm creating `docs/bee-links-graph-enforcement-links-graph`.
> 
> Now I'm writing a concise, LLM-targeted change doc that covers the dependency reference convention, the architecture and layering approach, the selection changes with cyclic exclusion, cycle detection using Tarjan's algorithm, the commit guard with linting and pre-commit hooks, test coverage, and any caveats or follow-up work needed.

Now write the change doc. The protocol names it `<branch>-<taskid>` under `docs/`.

**🔧 write** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo/docs/bee-links-graph-enforcement-links-graph-enforcement.md`

```
Wrote file successfully.
```


## assistant

> 💭 Running a final test suite and code formatting check before committing everything.

Doc written. Final verification: gofmt check + full test run.

**🔧 bash** `export GOTMPDIR=/home/spencer/.cache/gotmp TMPDIR=/home/spencer/.cache/gotmp CGO_ENABLED=0
SRC=/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo
cd "$SRC"
echo "=== gofmt diff (should be empty) ==="
gofmt -l internal/ cmd/
echo "=== go test ./... ==="
go test ./... 2>&1 | rg -v "no test files" | tail -20
echo "=== go vet ==="
go vet ./... 2>&1 | tail -5
echo "DONE"`

```
=== gofmt diff (should be empty) ===
=== go test ./... ===
ok  	github.com/spencerharmon/beehive/internal/claim	(cached)
ok  	github.com/spencerharmon/beehive/internal/config	(cached)
ok  	github.com/spencerharmon/beehive/internal/git	(cached)
ok  	github.com/spencerharmon/beehive/internal/links	(cached)
ok  	github.com/spencerharmon/beehive/internal/plan	(cached)
ok  	github.com/spencerharmon/beehive/internal/repo	(cached)
ok  	github.com/spencerharmon/beehive/internal/secrets	(cached)
ok  	github.com/spencerharmon/beehive/internal/select	(cached)
ok  	github.com/spencerharmon/beehive/internal/swarm	(cached)
ok  	github.com/spencerharmon/beehive/internal/web	(cached)
=== go vet ===
DONE

```


## assistant

> 💭 Everything's formatted correctly, so now I'm reviewing the full diff to make sure I'm only committing the intended changes and that the version stamp is accurate before staging anything.

Clean. Let me review the full source diff before committing.

**🔧 bash** `SRC=/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo
cd "$SRC"
echo "=== STATUS ==="; git status --short
echo "=== DIFFSTAT (tracked) ==="; git diff --stat
echo "=== UNTRACKED ==="; git ls-files --others --exclude-standard`

```
=== STATUS ===
 M cmd/beehive/main.go
 M internal/config/hook.go
 M internal/config/hook_test.go
 M internal/links/links.go
 M internal/plan/compat.go
 M internal/repo/repo.go
 M internal/select/select.go
?? cmd/beehive/cmd_lint.go
?? docs/bee-links-graph-enforcement-links-graph-enforcement.md
?? internal/links/cycle_test.go
?? internal/plan/deps.go
?? internal/plan/deps_test.go
?? internal/select/graph.go
?? internal/select/graph_test.go
=== DIFFSTAT (tracked) ===
 cmd/beehive/main.go          |  1 +
 internal/config/hook.go      | 47 ++++++++++++++++------
 internal/config/hook_test.go | 24 ++++++++++++
 internal/links/links.go      | 93 ++++++++++++++++++++++++++++++++++++++++++++
 internal/plan/compat.go      | 22 ++---------
 internal/repo/repo.go        |  3 ++
 internal/select/select.go    | 53 +++++++++++++++++++++++--
 7 files changed, 209 insertions(+), 34 deletions(-)
=== UNTRACKED ===
cmd/beehive/cmd_lint.go
docs/bee-links-graph-enforcement-links-graph-enforcement.md
internal/links/cycle_test.go
internal/plan/deps.go
internal/plan/deps_test.go
internal/select/graph.go
internal/select/graph_test.go

```


## assistant

> 💭 I'm ready to stage all the changes and create a commit with the required stamp line format. Let me first check the git log to understand the commit message conventions used in this repository.

All intended. Let me check the commit message style, then commit with the required stamp.

**🔧 bash** `SRC=/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo
cd "$SRC"
git log --oneline -8
echo "=== full messages sample ==="
git log -2 --format='%B' | head -20`

```
082d38c fix: init writes full embedded AGENTS.md protocol, not placeholder
d5e2095 fix: build static binaries (CGO_ENABLED=0)
15c71de Merge branch 'cave/agent/implementer-mqytmztc-3'
9c9e5a8 Merge branch 'cave/agent/implementer-mqytmzt9-2'
eb9fb24 Merge branch 'cave/agent/implementer-mqytmzt6-1'
2745304 P2: wire honeybee runner
48139d0 P2: honeybee turn loop + opencode session client
52d366f P2: commit-race claim, re-verify, heartbeat, GC, reject counter
=== full messages sample ===
fix: init writes full embedded AGENTS.md protocol, not placeholder

repo.Init now embeds prompts.Agents; scripts/init.sh delegates to beehive init.

Beehive: init-agents-fix

fix: build static binaries (CGO_ENABLED=0)

cmd packages pull net (cgo); pure-Go beehive needs no cgo. Force static
in install.sh, CI build/release, and smoke. Resolves -latomic_asneeded link error.

Beehive: cgo-static-fix


```


## assistant

> 💭 I need to follow the commit format with a summary, body, and the Beehive stamp including the doc path as specified in the AGENTS.md protocol. Let me check if there's a pre-commit hook in the shared gitdir at `.git/modules/submodules/beehive/repo`.
> 
> The hook would only trigger on ROI.md or PLAN.md/SUBMODULE-LINKS.yaml, but I'm staging Go files and a doc that doesn't match those names, so it shouldn't block the commit. Let me go ahead and stage everything, then commit.

All intended changes, no secrets. Staging and committing the source with the required stamp line.

**🔧 bash** `SRC=/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo
cd "$SRC"
git add -A
git commit -q -F - <<'EOF'
feat: enforce cross-submodule dep gating + wait-cycle guard in selection

Selection now resolves linked-submodule deps and excludes cycle-entangled tasks
(Tarjan SCC) instead of deadlocking; cross deps gate only when the linked
submodule is declared in SUBMODULE-LINKS. Adds `beehive lint` (select.Validate)
and extends the pre-commit hook so a honeybee PLAN.md dep-tag commit forming a
wait cycle is rejected, mirroring links.AddDep. plan stays a leaf (no links
import); select owns graph construction. Tests + e2e cover gating, cyclic
exclusion, Validate, and both hook guards.

Beehive: links-graph-enforcement docs/bee-links-graph-enforcement-links-graph-enforcement.md
EOF
echo "exit=$?"
git log --oneline -1
git show --stat HEAD | tail -20`

```
exit=0
3266f49 feat: enforce cross-submodule dep gating + wait-cycle guard in selection
    import); select owns graph construction. Tests + e2e cover gating, cyclic
    exclusion, Validate, and both hook guards.
    
    Beehive: links-graph-enforcement docs/bee-links-graph-enforcement-links-graph-enforcement.md

 cmd/beehive/cmd_lint.go                            |  39 ++++++
 cmd/beehive/main.go                                |   1 +
 ...ks-graph-enforcement-links-graph-enforcement.md |  97 ++++++++++++++
 internal/config/hook.go                            |  47 +++++--
 internal/config/hook_test.go                       |  24 ++++
 internal/links/cycle_test.go                       |  98 ++++++++++++++
 internal/links/links.go                            |  93 ++++++++++++++
 internal/plan/compat.go                            |  22 +---
 internal/plan/deps.go                              |  93 ++++++++++++++
 internal/plan/deps_test.go                         | 143 +++++++++++++++++++++
 internal/repo/repo.go                              |   3 +
 internal/select/graph.go                           |  94 ++++++++++++++
 internal/select/graph_test.go                      | 140 ++++++++++++++++++++
 internal/select/select.go                          |  53 +++++++-
 14 files changed, 913 insertions(+), 34 deletions(-)

```


## assistant

> 💭 The source commit is at `3266f49`, and I need to update the outer submodule pointer on the `bee-links-graph-enforcement` branch, then mark the task as NEEDS-REVIEW on main. Looking back at the protocol, the sequence should be setting the task to NEEDS-REVIEW on main first, then handling the branch with the submodule patch.
> 
> Now I'm working through the repository topology — the outer worktree is at the current directory, and the source worktree is nested several levels deep through submodule paths. I need to clarify exactly how the worktree structure maps to the outer and inner repos so I can correctly update the submodule pointer.

Source committed as `3266f49`. Now I need to bump the submodule pointer on the outer `bee-links-graph-enforcement` branch and flip PLAN.md to NEEDS-REVIEW on main. Let me first understand the worktree topology and helper script.

**🔧 bash** `MAIN=/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318
WT=$MAIN/submodules/beehive/worktrees/bee-links-graph-enforcement
echo "=== worktree dir is outer-repo checkout? ==="
git -C "$WT" rev-parse --show-toplevel 2>/dev/null
echo "=== branch of WT ==="; git -C "$WT" branch --show-current 2>/dev/null
echo "=== WT status ==="; git -C "$WT" status --short 2>/dev/null | head
echo "=== submodule pointer line ==="; git -C "$WT" diff --submodule=short -- submodules/beehive/repo 2>/dev/null | head`

```
=== worktree dir is outer-repo checkout? ===
/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/worktrees/bee-links-graph-enforcement
=== branch of WT ===
bee-links-graph-enforcement
=== WT status ===
 M submodules/beehive/repo
=== submodule pointer line ===
diff --git a/submodules/beehive/repo b/submodules/beehive/repo
index 082d38c..3266f49 160000
--- a/submodules/beehive/repo
+++ b/submodules/beehive/repo
@@ -1 +1 @@
-Subproject commit 082d38cbd1f52f07b154ebe82b57c2899afe7207
+Subproject commit 3266f493ce6db7879966e154450e188ae09e0718

```


## assistant

> 💭 The submodule pointer in the worktree has been updated from `082d38c` to `3266f49`, and I need to commit this change on the `bee-links-graph-enforcement` branch. I'm checking the worktree.sh script to understand the commit conventions and whether the outer commit needs any special annotation, since the protocol indicates the branch should carry the submodule patch.

Confirmed: the pointer moved `082d38c → 3266f49`. Let me check the worktree helper and ROI-hook constraints before committing the pointer bump on the branch.

**🔧 bash** `MAIN=/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318
cd "$MAIN"
echo "=== scripts ==="; ls scripts/ 2>/dev/null
echo "=== claim commit d954fdf ==="; git show -s --format='%B' d954fdf | head -15
echo "=== is there a pre-commit hook on outer repo? ==="; ls -la .git/hooks/pre-commit 2>/dev/null || git -C "$MAIN" rev-parse --git-dir`

```
=== scripts ===
=== claim commit d954fdf ===
plan: claim links-graph-enforcement

Beehive: links-graph-enforcement plan

=== is there a pre-commit hook on outer repo? ===
/home/spencer/git-repos/spencerharmon/infra-beehive/.git/worktrees/bee-1782767318

```


## assistant

> 💭 I notice the scripts directory isn't present in the outer worktree, so I'll handle the git operations manually instead. For outer-repo commits, I need to follow the format `plan: <action> <task>` with the stamp `Beehive: links-graph-enforcement plan`. Now I'm reconsidering the protocol sequence, particularly step 5 which involves updating PLAN.md and NEEDS-REVIEW on main while managing the submodule patch branch.
> 
> I need to make two outer-repo updates: flip PLAN.md to NEEDS-REVIEW on main, and commit the submodule pointer bump on the `bee-links-graph-enforcement` branch. The reviewer will then evaluate the branch against the task and either merge to main (marking it DONE) or reject it. Since the WT is already on the `bee-links-graph-enforcement` branch, I'll commit the pointer bump there first, then switch to main and update PLAN.md. I've confirmed the outer worktree only has submodule changes, not any ROI.md modifications, which is correct. For the stamp, I should use the coordination-level reference rather than a submodule-relative path.

No `scripts/` in this worktree, so I'll do the git ops directly. The WT is already on the `bee-links-graph-enforcement` branch with the pointer bump staged-ready. Let me commit the pointer bump there.

**🔧 bash** `MAIN=/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318
WT=$MAIN/submodules/beehive/worktrees/bee-links-graph-enforcement
cd "$WT"
git add submodules/beehive/repo
git commit -q -F - <<'EOF'
plan: bump beehive pointer for links-graph-enforcement

Submodule beehive -> 3266f49: cross-submodule dep gating + wait-cycle exclusion
in selection, `beehive lint`, and pre-commit dep-cycle guard. Branch patch for
review against task links-graph-enforcement.

Beehive: links-graph-enforcement submodules/beehive/repo/docs/bee-links-graph-enforcement-links-graph-enforcement.md
EOF
echo "exit=$?"
git log --oneline -3
echo "=== pointer now ==="; git ls-tree HEAD submodules/beehive/repo`

```
exit=0
9eee2a0 plan: bump beehive pointer for links-graph-enforcement
d954fdf plan: claim links-graph-enforcement
fe6ab9f session: bee-bootstrap-1782766865
=== pointer now ===
160000 commit 3266f493ce6db7879966e154450e188ae09e0718	submodules/beehive/repo

```


## assistant

> 💭 I've updated the branch pointer and now I need to mark the links-graph-enforcement task as NEEDS-REVIEW in the main worktree's PLAN.md file, changing its status from IN-PROGRESS.
> 
> Since NEEDS-REVIEW tasks don't need a heartbeat (only IN-PROGRESS tasks use it for staleness detection), I'll clear that field to match the canonical state machine. I should also add a note to the task body pointing reviewers to the branch where the patch lives so they know where to look.

Branch now at `9eee2a0` with the pointer bump. Now flip the PLAN.md task to NEEDS-REVIEW on main (cwd) and record the review pointer. Let me read the current task block first.

**🔧 read** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/PLAN.md`

```
<path>/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/PLAN.md</path>
<type>file</type>
<content>
82: ## links-graph-enforcement [IN-PROGRESS] <!-- attempts=0 deps= weight=3 heartbeat=2026-06-29T21:27:22Z -->
83: Cross-submodule deps and wait-cycle detection are unused in selection. Resolve linked-
84: submodule deps and run links.HasCycle during candidate selection (select.go fromSubmodule /
85: plan.Candidates). The cycle check must also run on honeybee dep-tag commits (PLAN.md writes),
86: not only the CLI (links.AddDep).
87: Files: internal/select/select.go, internal/links/links.go, internal/plan, commit-path guard.
88: Doc: docs/tasks/links-graph-enforcement.md
89: Accept: linked-submodule dep gates selection; a dep-tag commit forming a cycle is rejected.
90: 

(Showing lines 82-90 of 302. Use offset=91 to continue.)
</content>
```


## assistant

**🔧 edit** `/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318/submodules/beehive/PLAN.md`

```
Edit applied successfully.
```


## assistant

> 💭 I'm checking whether the PLAN.md file still parses correctly with the beehive plan parser, verifying that the header format with the NEEDS-REVIEW status is valid. I'd like to test this against the outer repo's parser, but there's no direct CLI tool available for validation.
> 
> The regex pattern for the header is straightforward, and my entry matches it perfectly — the empty deps field is acceptable. Now I need to stage and commit this PLAN.md change to main. Since the current worktree branch `bee-1782767318` acts as the main-equivalent in this beehive setup (where earlier heartbeat commits were also made), I'll commit the NEEDS-REVIEW status flip here.

Now commit the NEEDS-REVIEW flip to main (cwd).

**🔧 bash** `MAIN=/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318
cd "$MAIN"
git add submodules/beehive/PLAN.md
git commit -q -F - <<'EOF'
plan: links-graph-enforcement -> NEEDS-REVIEW

Implementation complete on branch bee-links-graph-enforcement (beehive -> 3266f49).
Awaiting review.

Beehive: links-graph-enforcement submodules/beehive/repo/docs/bee-links-graph-enforcement-links-graph-enforcement.md
EOF
echo "exit=$?"
git log --oneline -4
echo "=== status clean? ==="; git status --short`

```
exit=0
81cfd33 plan: links-graph-enforcement -> NEEDS-REVIEW
5019147 plan: heartbeat links-graph-enforcement
3ba80bd plan: heartbeat links-graph-enforcement
fdfceff plan: heartbeat links-graph-enforcement
=== status clean? ===
 M submodules/beehive/worktrees/bee-links-graph-enforcement
?? submodules/beehive/sessions/bee-links-graph-enforcement-1782767318.md

```


## assistant

> 💭 I'm looking at the main worktree status and noticing that `submodules/beehive/worktrees/bee-links-graph-enforcement` is showing as modified, which is puzzling at first—but then it clicks that this is a nested git worktree, so from the main worktree's perspective it appears as a gitlink or directory that's changed.
> 
> The real question is whether `submodules/beehive/worktrees/` is actually tracked by the outer repo or if it's gitignored. The ` M` prefix suggests it's a tracked path that's been modified, which would be unusual for a worktree directory unless it's stored as a gitlink or submodule itself. I need to dig into what these entries actually are to understand what's going on.

Let me investigate the leftover status entries (the worktree dir and session file) before concluding — I want to be sure I'm not leaving the tree in a bad state.

**🔧 bash** `MAIN=/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318
cd "$MAIN"
echo "=== is worktrees path tracked / ignored? ==="
git ls-files submodules/beehive/worktrees/ | head
echo "--- check-ignore ---"
git check-ignore -v submodules/beehive/worktrees/bee-links-graph-enforcement submodules/beehive/sessions/bee-links-graph-enforcement-1782767318.md
echo "=== what git thinks changed about the worktree path ==="
git diff -- submodules/beehive/worktrees/bee-links-graph-enforcement | head
echo "=== .gitignore (outer) ==="
git ls-files .gitignore && sed -n '1,40p' .gitignore 2>/dev/null`

```
=== is worktrees path tracked / ignored? ===
submodules/beehive/worktrees/bee-bootstrap
submodules/beehive/worktrees/bee-links-graph-enforcement
--- check-ignore ---
=== what git thinks changed about the worktree path ===
diff --git a/submodules/beehive/worktrees/bee-links-graph-enforcement b/submodules/beehive/worktrees/bee-links-graph-enforcement
index d954fdf..9eee2a0 160000
--- a/submodules/beehive/worktrees/bee-links-graph-enforcement
+++ b/submodules/beehive/worktrees/bee-links-graph-enforcement
@@ -1 +1 @@
-Subproject commit d954fdfec8b5226a9a3108d41ed7b89730141771
+Subproject commit 9eee2a030e831911fa4a033ebc52cc55f91d9dfe
=== .gitignore (outer) ===

```


## assistant

> 💭 I'm seeing the core issue now: the outer repository has a gitlink pointing to `submodules/beehive/worktrees/bee-links-graph-enforcement` as a submodule, but it's referencing commits from the `bee-links-graph-enforcement` branch itself—essentially creating a self-referential structure where the submodule points back to its own history.
> 
> The workflow is becoming clearer: when I claimed the task, the gitlink was pinned at `d954fdf`, and now that I've made changes on the branch (reaching `9eee2a0`), the gitlink shows as modified in the main worktree. The real question is whether I should commit this gitlink advance now or wait for the reviewer to handle it, since the protocol expects the branch (via the submodule patch) to be evaluated before merging to main. shouldn't commit the session file since it's runner-managed, so let me check those earlier commits to see what the bootstrap flow actually committed to understand the expected end state.

This is the key topology: `submodules/beehive/worktrees/bee-links-graph-enforcement` is a gitlink in main tracking my review branch (moved `d954fdf → 9eee2a0`). Let me check how the bootstrap/claim flow handled these gitlinks + session files to match the expected end state.

**🔧 bash** `MAIN=/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318
cd "$MAIN"
echo "=== claim commit d954fdf: what changed ==="
git show --stat d954fdf | tail -15
echo "=== session commit fe6ab9f: what changed ==="
git show --stat fe6ab9f | tail -15`

```
=== claim commit d954fdf: what changed ===
commit d954fdfec8b5226a9a3108d41ed7b89730141771
Author: spencer <the.spencer.harmon@gmail.com>
Date:   Mon Jun 29 16:08:38 2026 -0500

    plan: claim links-graph-enforcement
    
    Beehive: links-graph-enforcement plan

 submodules/beehive/PLAN.md | 2 +-
 1 file changed, 1 insertion(+), 1 deletion(-)
=== session commit fe6ab9f: what changed ===
commit fe6ab9f0585677d2a3e56d08ad119309854b407e
Author: spencer <the.spencer.harmon@gmail.com>
Date:   Mon Jun 29 16:01:06 2026 -0500

    session: bee-bootstrap-1782766865
    
    Beehive: session bee-bootstrap-1782766865

 .../beehive/sessions/bee-bootstrap-1782766865.md   | 2913 ++++++++++++++++++++
 1 file changed, 2913 insertions(+)

```


## assistant

> 💭 I'm realizing there's a puzzle here: the commit d954fdf only modified PLAN.md, yet the gitlink in the submodules directory is pointing to that exact commit. That means the gitlink reference must have been recorded somewhere, but if d954fdf didn't touch the gitlink itself, how did it get created or tracked in the main branch?
> 
> Looking at the current state, the gitlink shows as modified (unstaged) between d954fdf and 9eee2a0, which tells me main's HEAD currently has it at d954fdf. The real question is whether I should be committing this gitlink advance myself or if the runner handles it. Let me trace back through the git history to see which commit originally introduced the gitlink at d954fdf — that'll tell me who's responsible for managing it.

Let me trace who manages the worktree gitlink (when it was recorded at d954fdf) to know whether I should commit its advance.

**🔧 bash** `MAIN=/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318
cd "$MAIN"
echo "=== history of the worktree gitlink ==="
git log --oneline -- submodules/beehive/worktrees/bee-links-graph-enforcement | head
echo "=== commit that added it: show stat ==="
git log --oneline --diff-filter=A -- submodules/beehive/worktrees/bee-links-graph-enforcement
echo "=== HEAD gitlink value ==="
git ls-tree HEAD submodules/beehive/worktrees/bee-links-graph-enforcement
echo "=== how is this worktree registered? (gitlink vs worktree) ==="
cat submodules/beehive/worktrees/bee-links-graph-enforcement/.git 2>/dev/null | head -1
git config -f .gitmodules --get-regexp 'worktrees' 2>/dev/null | head`

```
=== history of the worktree gitlink ===
fdfceff plan: heartbeat links-graph-enforcement
=== commit that added it: show stat ===
fdfceff plan: heartbeat links-graph-enforcement
=== HEAD gitlink value ===
160000 commit d954fdfec8b5226a9a3108d41ed7b89730141771	submodules/beehive/worktrees/bee-links-graph-enforcement
=== how is this worktree registered? (gitlink vs worktree) ===
gitdir: /home/spencer/git-repos/spencerharmon/infra-beehive/.git/worktrees/bee-links-graph-enforcement

```


## assistant

> 💭 I'm seeing how the gitlink was initially added in that heartbeat commit, pointing to a specific commit hash at that time. The worktree exists both as an actual git worktree directory and as a tracked gitlink in the repository, which creates this dual registration situation.
> 
> Since I'm the one managing this gitlink through my commits, I need to advance it to reflect the current state of the review branch. The inconsistency is that I already committed the PLAN.md flip to NEEDS-REVIEW without updating the gitlink to match, so right now main shows NEEDS-REVIEW status but the gitlink still points to the old claim commit. I should commit the gitlink advance now to make them consistent and give the next reviewer a clear picture of what commit they're evaluating. The branch bee-links-graph-enforcement is at 9eee2a0, which points to the actual work at 3266f49. For a clean handoff to review, I should advance main's gitlink to match the branch tip. I'm weighing whether this is my responsibility or if it should stay stale until the reviewer merges — but since I introduced gitlink tracking in the first place, keeping it accurate makes sense. Let me check how the bee-bootstrap gitlink is managed to confirm whether the convention is to track the branch tip.

Now I understand: the gitlink tracks my review branch's tip and I introduced it earlier this session (commit `fdfceff`). It's currently stale at `d954fdf` while PLAN says NEEDS-REVIEW. Let me confirm the convention against the `bee-bootstrap` gitlink, then advance it for a coherent handoff.

**🔧 bash** `MAIN=/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318
cd "$MAIN"
echo "=== bee-bootstrap gitlink value in HEAD ==="
git ls-tree HEAD submodules/beehive/worktrees/bee-bootstrap
echo "=== bee-bootstrap branch tip ==="
git rev-parse bee-bootstrap 2>/dev/null
echo "=== bee-links branch tip ==="
git rev-parse bee-links-graph-enforcement
echo "=== confirm fdfceff added gitlink via which staging ==="
git show --stat fdfceff | tail -8`

```
=== bee-bootstrap gitlink value in HEAD ===
160000 commit 082d38cbd1f52f07b154ebe82b57c2899afe7207	submodules/beehive/worktrees/bee-bootstrap
=== bee-bootstrap branch tip ===
bee-bootstrap
=== bee-links branch tip ===
9eee2a030e831911fa4a033ebc52cc55f91d9dfe
=== confirm fdfceff added gitlink via which staging ===
Date:   Mon Jun 29 16:08:38 2026 -0500

    plan: heartbeat links-graph-enforcement
    
    Beehive: links-graph-enforcement plan

 submodules/beehive/worktrees/bee-links-graph-enforcement | 1 +
 1 file changed, 1 insertion(+)

```


## assistant

> 💭 I'm noticing that the `bee-bootstrap` gitlink in HEAD points to commit 082d38c, but when I try to resolve the bootstrap branch directly, it doesn't exist—the worktree was already cleaned up after the merge completed, leaving the gitlink orphaned at that commit which represents the submodule's remote tip.
> 
> The real issue is that the gitlink for `bee-links-graph-enforcement` is stale—it's pointing at d954fdf (the branch tip when I created the worktree), but the actual branch has advanced to 9eee2a0. I should update the gitlink to match the current branch tip to ensure a clean NEEDS-REVIEW handoff on main. Before I commit this change, I need to verify it won't interfere with the runner's completion check, since my task is to reach NEEDS-REVIEW on main with the branch holding the submodule patch.
> 
> I'll stage and commit just the gitlink advance with a clear message, being careful not to include the untracked session file that the runner manages separately.

Confirmed: the gitlink tracks the review branch tip (I added it in `fdfceff`). It's stale at `d954fdf` vs branch tip `9eee2a0`. Advancing it for a coherent review handoff. I'll stage only the gitlink (leaving the runner's session file untracked).

**🔧 bash** `MAIN=/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318
cd "$MAIN"
git add submodules/beehive/worktrees/bee-links-graph-enforcement
git commit -q -F - <<'EOF'
plan: advance links-graph-enforcement review pointer -> 9eee2a0

Sync the review-branch gitlink to its tip (bumps beehive -> 3266f49) so main's
NEEDS-REVIEW record points at the patch under review.

Beehive: links-graph-enforcement submodules/beehive/repo/docs/bee-links-graph-enforcement-links-graph-enforcement.md
EOF
echo "exit=$?"
echo "=== HEAD gitlink now ==="; git ls-tree HEAD submodules/beehive/worktrees/bee-links-graph-enforcement
echo "=== status ==="; git status --short
echo "=== recent main log ==="; git log --oneline -5`

```
exit=0
=== HEAD gitlink now ===
160000 commit 9eee2a030e831911fa4a033ebc52cc55f91d9dfe	submodules/beehive/worktrees/bee-links-graph-enforcement
=== status ===
?? submodules/beehive/sessions/bee-links-graph-enforcement-1782767318.md
=== recent main log ===
e6cd81e plan: advance links-graph-enforcement review pointer -> 9eee2a0
81cfd33 plan: links-graph-enforcement -> NEEDS-REVIEW
5019147 plan: heartbeat links-graph-enforcement
3ba80bd plan: heartbeat links-graph-enforcement
fdfceff plan: heartbeat links-graph-enforcement

```


## assistant

> 💭 The main branch is now in a stable state with the links-graph-enforcement review marked in PLAN.md, the gitlink pointing to the latest review branch commit that updates beehive, and only the runner's session transcript remaining as an untracked file.
> 
> I'm doing a final holistic verification: confirming the source worktree is clean with HEAD at 3266f49 on the bee-links-graph-enforcement branch, the outer branch gitlink matches, main's PLAN.md shows the correct status, and tests are passing. Since the task is NEEDS-REVIEW rather than DONE, no dependent unlocking is required, and I've confirmed no other tasks depend on this one.

Main is now coherent: PLAN=NEEDS-REVIEW, gitlink→9eee2a0 (which bumps beehive→3266f49). The remaining untracked file is the runner's session transcript (left for the runner, matching the bootstrap pattern). Let me do a final holistic verification.

**🔧 bash** `export GOTMPDIR=/home/spencer/.cache/gotmp TMPDIR=/home/spencer/.cache/gotmp CGO_ENABLED=0
MAIN=/home/spencer/git-repos/spencerharmon/infra-beehive/.worktrees/bee-1782767318
SRC=$MAIN/submodules/beehive/worktrees/bee-links-graph-enforcement/submodules/beehive/repo
echo "=== SOURCE worktree clean + HEAD ==="
git -C "$SRC" status --short; git -C "$SRC" log --oneline -1
echo "=== SOURCE final test run ==="
cd "$SRC" && go test ./... 2>&1 | rg -v "no test files|^ok" ; echo "test_rc=${PIPESTATUS[0]} (empty above = all ok)"
echo "=== OUTER branch tip + its submodule pointer ==="
git -C "$MAIN/submodules/beehive/worktrees/bee-links-graph-enforcement" log --oneline -1
git -C "$MAIN/submodules/beehive/worktrees/bee-links-graph-enforcement" ls-tree HEAD submodules/beehive/repo
echo "=== MAIN: PLAN status line ==="
rg -n "links-graph-enforcement \[" "$MAIN/submodules/beehive/PLAN.md"`

```
=== SOURCE worktree clean + HEAD ===
3266f49 feat: enforce cross-submodule dep gating + wait-cycle guard in selection
=== SOURCE final test run ===
test_rc=0 (empty above = all ok)
=== OUTER branch tip + its submodule pointer ===
9eee2a0 plan: bump beehive pointer for links-graph-enforcement
160000 commit 3266f493ce6db7879966e154450e188ae09e0718	submodules/beehive/repo
=== MAIN: PLAN status line ===
82:## links-graph-enforcement [NEEDS-REVIEW] <!-- attempts=0 deps= weight=3 -->

```


## assistant

> 💭 Everything's in good shape across the board—the source worktree is clean with all tests passing, the review branch is tracking the right commit, and the main task is ready for review. Now I need to clean up that temporary binary I built in the cache directory since the e2e directories were already removed.
> 
> I should also verify the documentation naming follows the protocol correctly—the doc is named `bee-links-graph-enforcement-links-graph-enforcement.md`, which matches the `<branch>-<taskid>` format. Once I confirm that's right, I'll mark everything as complete and remove the temp binary to keep things tidy.

All three layers are coherent and green. Let me mark todos complete and clean up the temp build artifact.

**🔧 todowrite** `{"todos":[{"content":"Verify internal packages build green (baseline)","priority":"high","status":"completed"},{"content":"Create cmd/beehive/cmd_lint.go + wire lintCmd in main.go","priority":"high","…`

```
[
  {
    "content": "Verify internal packages build green (baseline)",
    "status": "completed",
    "priority": "high"
  },
  {
    "content": "Create cmd/beehive/cmd_lint.go + wire lintCmd in main.go",
    "status": "completed",
    "priority": "high"
  },
  {
    "content": "Extend roiHook in config/hook.go with dep-cycle guard",
    "status": "completed",
    "priority": "high"
  },
  {
    "content": "Write links tests (CyclicNodes, Cycle, HasCycleWith)",
    "status": "completed",
    "priority": "high"
  },
  {
    "content": "Write plan tests (SplitDep, Qualify, SelectableExt, CandidatesExt)",
    "status": "completed",
    "priority": "high"
  },
  {
    "content": "Write select tests (cross-sub gating, cyclic skip, Validate)",
    "status": "completed",
    "priority": "high"
  },
  {
    "content": "Write config hook_test content assertions",
    "status": "completed",
    "priority": "medium"
  },
  {
    "content": "go test ./... green + e2e lint/hook verified",
    "status": "completed",
    "priority": "high"
  },
  {
    "content": "Write change doc under docs/",
    "status": "completed",
    "priority": "high"
  },
  {
    "content": "Commit source (stamped) + bump outer pointer; flip PLAN.md to NEEDS-REVIEW",
    "status": "completed",
    "priority": "high"
  }
]
```


## assistant

**🔧 bash** `rm -f /home/spencer/.cache/gotmp/beehive; rm -rf /home/spencer/.cache/gotmp/e2e.* 2>/dev/null; echo "cleaned temp build artifacts"`

```
cleaned temp build artifacts

```


## assistant

The task is complete. Here's the summary.

## links-graph-enforcement → NEEDS-REVIEW

Implemented cross-submodule dependency gating + wait-cycle enforcement in beehive selection, plus a commit-time guard. All work is committed and the task is flipped to NEEDS-REVIEW for the next bee.

### What shipped (source `3266f49`)
- **`internal/links/links.go`**: `Cycle` (DFS, returns offending path), `CyclicNodes` (Tarjan SCC — exact set to exclude), `HasCycleWith` (non-mutating).
- **`internal/plan/deps.go`** (new): the `<sm>:<task>` qualified-dep convention (`Qualify`/`SplitDep`), a `DepStatus` resolver type, and link-aware `SelectableExt`/`CandidatesExt`. `plan` stays a leaf (no `links` import); `Candidates` delegates for back-compat.
- **`internal/select/graph.go`** (new): owns graph construction — `LoadEdges` (all PLAN dep tags + SUBMODULE-LINKS edges, qualified) and `Validate`.
- **`internal/select/select.go`**: builds the combined graph once, excludes cyclic tasks everywhere, and resolves cross deps only for submodules declared in SUBMODULE-LINKS (unlinked/missing ⇒ blocks).
- **`cmd/beehive/cmd_lint.go`** (new) + **`internal/config/hook.go`**: `beehive lint` runs `Validate`; the pre-commit hook now enforces both ROI-protect (honeybee-only) and the dep-cycle guard (any identity, when PLAN.md/SUBMODULE-LINKS.yaml is staged).

### Verification
- `go test ./...` green; `go vet` clean; gofmt clean.
- New tests in links, plan, select, and config.
- **E2E**: a cyclic dep-tag commit (`a:at → b:bt → a:at`) is rejected by both `beehive lint` and the hook; accepted once broken; ROI-protect still fires.

### State left for review
- Source branch `bee-links-graph-enforcement` @ `3266f49` (clean).
- Outer review branch `bee-links-graph-enforcement` @ `9eee2a0` bumps `submodules/beehive/repo` → `3266f49`.
- Main: PLAN task = `NEEDS-REVIEW`; review gitlink advanced to `9eee2a0`; change doc at `docs/bee-links-graph-enforcement-links-graph-enforcement.md`.

Both acceptance criteria are met: linked-submodule deps gate selection, and a cycle-forming dep-tag commit is rejected. The only untracked item is the runner's in-flight session transcript, intentionally left for the runner to commit.

