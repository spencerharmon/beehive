# Page-load performance & regression testing

`internal/web/pageload_test.go` is the automated gate that measures beehived's
heaviest page renders and FAILS when one regresses past its per-page budget. It
is the target the page-load-optimization ("50ms") work is measured against.

## What it gates

Five pages, each measured end-to-end through `Server.Routes()` with
`httptest` (no network, pure render cost):

| page         | route                              |
|--------------|------------------------------------|
| dashboard    | `GET /`                            |
| stats        | `GET /stats`                       |
| sessions list| `GET /submodule/{name}/sessions`   |
| session page | `GET /submodule/{name}/session/{branch}` |
| plan view    | `GET /submodule/{name}/plan`       |

`measurePage` serves a page best-of-N and returns the fastest sample (jitter is
one-sided, so best-of-N tracks true render cost). `gatePage` fails when the
status is not 200 or the sample exceeds the page's budget.

## Two performance cases

- **Synthetic** (`TestPageLoadBudgetsSynthetic`, always runs): builds a
  session-heavy fixture repo on disk — 400 finished session transcripts + a
  120-task plan — and gates every page best-of-5. This keeps the gate meaningful
  even when the live hive is not mounted (standalone submodule CI).
- **Live** (`TestPageLoadBudgetsLiveHive`): exercises the real, session-heavy
  `infra-beehive` hive — the case the ROI names. It locates the hive from
  `BEEHIVE_LIVE_REPO` or by walking up from the test's working directory to a
  beehive repo (`AGENTS.md` + `submodules/`) that actually has session files, and
  **skips** (never fails) when none is reachable. Each live page is measured
  ONCE because a single render already dominates wall-clock. Set
  `BEEHIVE_PAGELOAD_SKIP_LIVE=1` to opt out of the (slow) live case.

Everything is git/repo-derived and stateless per the submodule invariant: no
opencode-db, no out-of-repo cache, no machine-local state is read.

## Budgets — the regression ceiling, meant to be tightened

Two scales, both set so CURRENT behavior passes (the budget is the ceiling, not
the goal). The live `/stats` budget is large ON PURPOSE: today `/stats` over the
real hive scans every session transcript and takes ~60–70s — exactly the
regression this gate pins and the optimization work exists to fix. When that
work lands, ratchet the ceilings down (toward the 50ms target) via the env
overrides or a follow-up edit.

- synthetic: dashboard 2s · stats 5s · sessions 2s · session 3s · plan 2s
- live: dashboard 5s · stats 180s · sessions 30s · session 30s · plan 15s

Per-page, per-scale overrides (milliseconds), no code change needed:

- `BEEHIVE_PAGELOAD_BUDGET_MS_<PAGE>` (synthetic)
- `BEEHIVE_PAGELOAD_LIVE_BUDGET_MS_<PAGE>` (live)

e.g. `BEEHIVE_PAGELOAD_LIVE_BUDGET_MS_STATS=200` asserts the 50ms-class target.

## Running

    CGO_ENABLED=0 go test ./internal/web/ -run TestPageLoad -v      # both cases
    BEEHIVE_PAGELOAD_SKIP_LIVE=1 CGO_ENABLED=0 go test ./...        # fast, synthetic only
