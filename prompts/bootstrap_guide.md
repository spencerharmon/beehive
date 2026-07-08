# BOOTSTRAP.md — standing up a beehive install

Step-by-step guide to take an empty directory to a running beehive loop. Generic:
every site-specific value you decide here gets recorded in `LOCALS.md`, which this
guide helps you write. Run through it top to bottom; each step is idempotent.

This file is a beehive-managed default (refreshed by `beehive instruction update`).

## 0. Prerequisites

- The `beehive` and `honeybee` binaries built and on `PATH` (or note their install
  path in `LOCALS.md`).
- An agent CLI the runner can drive (e.g. `opencode`) and a model.
- git configured with whatever push credentials the targets need.

## 1. Create the beehive repo

```
beehive init <dir>
```

This scaffolds `submodules/`, installs the managed instruction files
(`AGENTS.md`, `HONEYBEE.md`, `BOOTSTRAP.md`), installs the git hooks (ROI-protect
pre-commit + submodule-sync post-receive), and sets `receive.denyCurrentBranch=
updateInstead` so honeybee worktrees can publish to the checked-out `main` with no
remote. Make `<dir>` a git repo (`git init`) if it is not already.

## 2. Author LOCALS.md

Create `LOCALS.md` at the repo root capturing the facts unique to THIS install. At
minimum:

- **Source & build**: where the loop's source lives, the exact build command and
  environment (cache/tmp dirs, flags, toolchain version), and where binaries land.
- **Deploy**: the command that rebuilds/installs binaries and what it restarts.
- **Scheduler**: how passes are launched (e.g. a systemd timer/service, cron, or a
  manual launcher) and how to start/inspect/stop them.
- **Observability**: how to read logs (per-pass and the frontend), and the frontend
  URL/port.
- **Topology**: hostnames, whether targets have remotes or the hive is local-only,
  and the single source of truth for code.
- **Safety rules**: any local "do not do X without approval" rules (e.g. never halt
  a running pass — halting discards in-flight work and leaves a zombie claim until
  TTL GC).
- **Config**: location and key values of the runner config (see step 4).

`LOCALS.md` is yours: `beehive instruction update` never overwrites it.

## 3. Add targets and write their intent

For each target you want the swarm to improve:

```
beehive submodule add <name> <git-url>
# then author submodules/<name>/ROI.md  (the human record of intent)
```

Record cross-target dependencies with `beehive submodule link <a> <b>`. A bootstrap
pass (ROI present, PLAN absent) will decompose each `ROI.md` into a weighted
`PLAN.md`. Do not write `PLAN.md` by hand.

## 4. Configure the runner

Point the runner at its config (location is your choice — record it in `LOCALS.md`;
a common spot is `/etc/beehive/config.yaml`). Typical keys: `agent_cmd`, `model`,
`max_turns`, `reject_limit`, `ttl_minutes`, `turn_timeout_minutes`. These bound a
pass and the claim TTL.

## 5. Schedule passes

Wire your scheduler to launch one honeybee pass per tick against the repo root.
Passes are independent and worktree-isolated; running several concurrently is safe
(claims + the singleton reconcile/bootstrap lock prevent duplication). Keep the
cadence high enough to keep the swarm busy. Record the exact units/commands in
`LOCALS.md`.

## 6. Verify

- `beehive instruction update` reports the managed files clean.
- A manual pass selects a task, creates a worktree, and either reconciles, bootstraps,
  or works a task, then publishes to `main` with a clean tree afterward.
- The frontend lists the submodules, their plans, and live/idle sessions.

## 7. Keep instructions current

After upgrading the binaries, run `beehive instruction update` to refresh the managed
instruction files (it backs up any you have customized). `LOCALS.md` and your
`ROI.md`s are never touched.

## 8. Deploy script (recommended recipe)

A merged fix reaches a live honeybee pass only once TWO independent things happen:
the binaries are rebuilt from the fixed source (**Axis A**), and the hive root's
on-disk managed prompts — `HONEYBEE.md`/`AGENTS.md`/`BOOTSTRAP.md` — are refreshed to
match via `beehive instruction update` (**Axis B**). A deploy script that only does
the former leaves every pass reading stale prompts indefinitely even though the fixed
code is already running. Write yours (or adapt this template) to do both, every time,
and build atomically (temp-file-then-rename per binary, so a mid-build crash never
leaves a half-written binary in place):

```sh
#!/usr/bin/env sh
# <site>-beehive-rebuild — rebuild beehive/beehived/honeybee from $SRC's
# origin/main, stamp the build commit (Axis A), then refresh this hive's
# on-disk managed instructions (Axis B) so neither ever lags the other.
set -eu
SRC="$HOME/git-repos/<you>/beehive"          # beehive source checkout; origin/main is truth
BIN="$HOME/.local/bin"                       # or /usr/local/bin for a --system install
HIVE="$HOME/git-repos/<you>/infra-beehive"   # this hive's repo root

git -C "$SRC" fetch origin
git -C "$SRC" merge --ff-only origin/main
SHA=$(git -C "$SRC" rev-parse HEAD)

changed=0
for c in beehive beehived honeybee; do
  go -C "$SRC" build -trimpath \
    -ldflags "-X github.com/spencerharmon/beehive/internal/version.SHA=$SHA" \
    -o "$BIN/.$c.new" "./cmd/$c"
  if ! cmp -s "$BIN/.$c.new" "$BIN/$c" 2>/dev/null; then
    mv -f "$BIN/.$c.new" "$BIN/$c"
    [ "$c" = beehived ] && changed=1
  else
    rm -f "$BIN/.$c.new"
  fi
done

# Axis B: refresh the hive root's on-disk managed prompts to match what the
# binary just built above would install. Run this EVERY time, not only when a
# binary changed above, so a hand-edited or previously-skipped refresh never lingers.
( cd "$HIVE" && beehive instruction update >/dev/null )

[ "$changed" = 1 ] && systemctl --user restart beehived.service || true
```

Two details a plain `go build && install` skips:

- **The `-ldflags` stamp.** Without
  `-X github.com/spencerharmon/beehive/internal/version.SHA=$(git rev-parse HEAD)` on
  every build, `beehive version` reports the honest-but-useless "beehive dev" and a
  fresh binary is indistinguishable from a stale one — the prompt-embed drift-guard
  preflight warning (`cmd/honeybee`) has nothing to compare against and stays
  silently inert even though the binary IS current. Keep this flag in lockstep with
  the beehive source's own `scripts/build-release-artifacts.sh`/`scripts/install.sh`
  (same variable, same flag) rather than inventing a second convention.
- **The trailing `beehive instruction update`.** Rebuilding binaries alone only ever
  closes Axis A. Nothing else refreshes the hive root's `HONEYBEE.md`/`AGENTS.md`/
  `BOOTSTRAP.md` — chain the call so a single rebuild event closes both axes together,
  every time, rather than relying on remembering to run step 7 by hand.

### Multiple scheduler units: single-cohort-owns-`ExecStartPre` pattern

If more than one systemd timer/service launches honeybee passes on the same host
(e.g. separate units per model-routing "cohort" — one unit dispatching to a larger
model, a sibling to a smaller one), do **not** add `ExecStartPre=<rebuild script>` to
every one of them: the build above is temp-file-then-rename per binary
(`$BIN/.$c.new`), and that temp name is not unique per invocation, so two rebuilds
racing concurrently can stomp each other's in-progress temp file. Instead, get
automatic, race-free freshness with no dedicated rebuild timer at all:

- Put `ExecStartPre=<path-to-rebuild-script>` on exactly ONE cohort's `.service`
  unit.
- Leave every sibling cohort's unit WITHOUT `ExecStartPre=`, and stagger its
  `OnCalendar=` a few minutes after the owning cohort's. By the time a sibling's pass
  launches, the one rebuild that already ran this cycle has settled (binaries
  swapped, `beehived` restarted if it changed, instructions refreshed) — every
  cohort ends up effectively fresh without any unit ever running the rebuild
  concurrently with another.

Record which cohort owns `ExecStartPre=`, the script's real path, and the resulting
stagger in your own `LOCALS.md` (step 2) — this section is the portable pattern, not
a substitute for that site-specific record.
