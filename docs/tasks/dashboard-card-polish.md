# dashboard-card-polish: aesthetics pass on the dashboard submodule card

## Problem

`dashboard-cards` delivered the per-submodule card, but a Frontend-aesthetics
reconcile flagged four rough edges on it:

1. No at-a-glance sense of how many honeybees are actively working a submodule —
   the card carried only a boolean `.active` overlay, not a count.
2. The ROI affordance read as two loose, near-duplicate links (`roi` and
   `edit roi (AI)`) scattered in the flat link row rather than one clear pair.
3. The commit/branch-graph link was labelled `branches`, while the view it opens
   titles itself "<name> commits" — a label/destination mismatch.
4. The ROI stamp (`<code>`) is a raw sha that, at full 40-char length, overflowed
   the ~16rem card body.

## Design

All four are card-surface changes in `internal/web` — the dashboard template, its
one stylesheet, and one new derived field on the card view model. No swarm-state
behaviour (`State`/`Env`/`Pending`/`Human`/`Working`) changes.

### 1. Honeybee count — `internal/web/web.go`

`subView` gains `Bees int`: the COUNT of tasks holding a fresh session+heartbeat
claim (the same `PlanItem.Active` liveness that already drove the boolean
`Working`). `subViews` increments `v.Bees` in the same loop that sets `Working`,
so by construction `Working == (Bees > 0)` — no second pass, no new parse. It
rides the existing cached PLAN.md parse and the same `now`/`ttl` projection, so it
goes stale on TTL expiry exactly like `Working`.

### 2. Template — `internal/web/templates/dashboard.html`

- **🐝 count:** a `badge bees` in the `card-meta` row renders `🐝 {{.Bees}}` on
  EVERY card (0 when idle). When `.Working` it also carries `bees-live` so the
  badge lights up while bees are on the submodule.
- **ROI pair:** the two ROI links are consolidated into a single labelled
  `<span class="roi-links">roi <a .../roi/{name}>view</a> / <a .../edit?path=…ROI.md>edit</a></span>`
  — one view + one edit, no duplicates. Both destinations are unchanged (the
  read-only ROI view and the AI chat-diff editor).
- **Commits rename:** the `/submodule/{name}/branches` link text is now `commits`
  (the route/URL is unchanged — only the label — matching the branch view's own
  "<name> commits" title).
- **Stamp:** wrapped in `<p class="muted small card-stamp">` with the `<code>`
  carrying `title="{{.Stamp}}"` (only when a stamp exists) so the full value is
  reachable on hover.

### 3. Styles — `internal/web/assets/style.css`

- `.badge.bees` uses tabular figures (steady width as the count changes);
  `.badge.bees.bees-live` takes the teal `--hue-active` "actively worked" hue,
  matching the `.active` overlay's semantics, so a working card reads at a glance.
- `.card-stamp` is a baseline flex row; `.card-stamp > code` gets
  `min-width:0; overflow:hidden; text-overflow:ellipsis; white-space:nowrap`, so a
  long sha ELLIPSIZES within the card instead of overflowing — full value via the
  title (hover). `.card-links .roi-links` is `white-space:nowrap` to keep the
  consolidated view/edit pair cohesive.

## Tests — `internal/web/web_test.go`

- `TestDashboardCards` (extended): `subViews` reports `Bees == 1` for alpha (t1
  claimed; t2/t3 unclaimed), `Bees == 0` for the PLAN-less bravo, and `Bees == 0`
  once the claim is past the TTL (`now+48h`). The rendered grid shows the `🐝`
  count (0 at real-now, where the fixture claim is stale).
- `TestDashboardCardPolish` (new): drives idle vs live via the on-disk heartbeat.
  Idle (stale fixture claim): the `🐝 0` badge renders WITHOUT `bees-live`; the
  commit link reads `commits` and no `branches` label remains; exactly one
  `/roi/alpha` view link and one `ROI.md` edit link inside `roi-links`, no leftover
  `edit roi (AI)`; the stamp sits in `card-stamp` with its full value on the code
  `title`. Live (heartbeat rewritten fresh at real now): the card shows `🐝 1` with
  the `badge bees bees-live` modifier.

## Acceptance mapping

- *each card shows a 🐝 honeybee count* → `subView.Bees` + the `badge bees`;
  `TestDashboardCards` (count) + `TestDashboardCardPolish` (rendered 🐝).
- *exactly one ROI view/edit pair per card (no duplicates)* → the `roi-links`
  span; `TestDashboardCardPolish` asserts each of the view/edit links appears
  exactly once and the old duplicate label is gone.
- *the commit/branch-graph link reads "Commits"* → the relabelled link;
  `TestDashboardCardPolish` (`commits` present, `branches` absent).
- *the ROI stamp never overflows the card* → `.card-stamp` ellipsis + the code
  `title` (full value on hover); `TestDashboardCardPolish`.
- *existing state/env/pending/human behaviour unchanged* → those fields/markup are
  untouched; the pre-existing `TestDashboardCards` assertions still pass.
- *tests cover the new markup* → the two tests above.
