# gc-storm-visibility: surface the object-store storm in the hygiene panel

## Problem

The 2026-07-08 corruption (racing auto-gc across the shared checkout, see
`internal/git/gc.go`) was only caught when `main` would not resolve — yet the storm
sat on disk for HOURS first: dozens of orphan `pack-*.pack` and a growing
`tmp_pack_*` pile (a rising temp count == a repack killed mid-flight). Nothing
surfaced that state, so the operator had no early signal.

The two sibling blocker parts already shipped the FIX: `git-disable-auto-gc` turns
stock auto-gc off swarm-wide and `beehive-gc-command` runs a single serialized
runner-owned `git gc` preflight (`git.DisableAutoGC` / `git.MaybeGC` /
`git.MaintainRepos`). This part is the VISIBILITY: make the storm signature legible
in minutes, read-only, so a recurrence (or any interrupted repack) is caught before
it corrupts anything.

## Design

Extend the existing read-only hive-hygiene panel (`internal/web/hygiene.go`, the
health surface UI-audit Finding #16 named) with a per-managed-repo object-store
census, rendered beside the existing stale-worktree / gitlink / checkout / remote
rows. It only STATS `.git/objects/pack`; it NEVER repacks — that is the runner's
deterministic gc (`git.MaybeGC`). Reading `.git/objects/pack` is an IN-repo read
(the object store IS the repo), so it stays within the submodule AGENTS.md
"repo is the only data source" invariant — no out-of-repo read.

### `internal/web/hygiene.go`

- `PackStat` — the census for ONE managed repo: `Name` (`hive` or the submodule
  name), `Path`, live `Packs` count, leftover `Temp` count, total `Bytes`, and
  `Missing` (repo not materialized). Methods: `Warn()` (temp>0 OR packs ≥
  `packCountWarn`, never when Missing), `TempWarn()` / `PackAbnormal()` (the two
  signals, badged distinctly), `Size()` (human-readable).
- `packCountWarn = 24` — the live-pack count at/above which a store is "abnormal".
  A healthy repo holds one pack after gc and only a handful between runs; auto-gc is
  disabled swarm-wide so nothing self-consolidates a pile. 24 (two dozen) sits above
  normal churn yet well under git's own `gc.autoPackLimit` default (50), catching a
  genuine pile early. The panel only reports it; the runner repacks.
- `statPackDir(packDir)` — the pure, unit-testable counter over an already-resolved
  pack dir: counts only `pack-*.pack` as live packs, sums `tmp_pack_*`/`tmp_idx_*`/
  `tmp_rev_*` as leftover repack temps (the same prefixes `git.sweepStaleGCTemp`
  uses), totals every file's size. An absent pack dir (fresh repo, nothing repacked)
  is a clean zero, not an error.
- `packStoreStat(g, name, path)` — resolves one repo's pack dir through git
  (`rev-parse --git-path objects/pack`, the same idiom `gc.go` uses, so a
  submodule's `.git`-file gitdir pointer is followed to `.git/modules/<name>`), then
  calls `statPackDir`. It guards with `os.Stat(<dir>/.git)` FIRST: a repo with no
  own `.git` (an un-materialized submodule checkout) is reported `Missing` without
  running `rev-parse`, so git's upward `.git` search can never misattribute the
  parent hive's store to it.
- `scanPackStores(root, g, declared)` — builds the census for the hive plus every
  declared `submodules/<name>/repo` (reusing the `.gitmodules` `declared` set
  `scanHygiene` already computes — the same set `git.MaintainRepos` maintains), hive
  first then submodules by path.
- `humanBytes(n)` — binary-unit (B/KiB/MiB/…) formatter for the size column.
- `Hygiene` gains `Packs []PackStat` and `PackWarn()` (any repo warns); `scanHygiene`
  populates it; `Clean()` now also requires `!PackWarn()` so a storm with no other
  cruft still defeats the "clean" state.

### `internal/web/templates/hygiene_panel.html`

The shared `hygiene-widget` (embedded on both the dashboard and the standalone
`/hygiene` page) gains an always-on "Object-store packs (per managed repo)" table:
one row per repo with pack-dir size, live pack count, and `tmp_*` leftover count. A
warning row is tinted; the abnormal pack count is badged `abnormal` and a nonzero
temp count is badged `killed-repack residue`; the whole panel shows an
`object-store storm` badge and the `<details>` opens when any repo warns. A caption
states it is a read-only stat that never repacks.

### `internal/web/assets/style.css`

`.badge.warn` (amber `--hue-review`, matching the drift-drift "attention" hue) and a
`.pack-warn td` row tint. Token-driven, no new color token.

## Tests — `internal/web/web_test.go`

`seedPackDir` fabricates a `.git/objects/pack` with N `pack-*.pack` (+ paired `.idx`
that must NOT count), plus `tmp_pack_*`/`tmp_idx_*` leftovers.

- `TestStatPackDirCensus` — counts only `.pack`, sums the temps, nonzero size; an
  absent pack dir is a clean zero, not an error.
- `TestHygienePackStoreCensus` — the core accept: a declared submodule with a
  fabricated N-pack + M-temp store reports packs==N, temp==M, size>0, `Warn()` and
  `PackWarn()` true, `Clean()` false; dropping the temps (normal pack count) reports
  clean.
- `TestPackStoreAbnormalPackCountWarns` — `packCountWarn` live packs, zero temps,
  still warns (the orphan-pile signal).
- `TestPackStoreUnmaterializedRepoMissing` — a declared but un-materialized submodule
  is `Missing` with a zero census and never warns, even with a storm in the HIVE
  store (proves the upward-`.git`-search guard).
- `TestHygienePackPanelRenders` — GET `/hygiene` renders the census table, headers,
  the `object-store storm` badge, and the `killed-repack residue` flag.

## Acceptance mapping

- *panel shows per-repo pack-dir size + live pack count + tmp_* leftover for the hive
  and each submodule* → `scanPackStores` (hive + declared submodules) rendered in the
  `hygiene-widget` table; `TestHygienePackStoreCensus`.
- *fabricated pack dir with N packs + M tmp_* reports pack==N, temp==M, nonzero size,
  surfaces the nonzero temp count as a warning; zero-temp reports clean* →
  `statPackDir` + `PackStat.Warn`/`TempWarn`; `TestStatPackDirCensus`,
  `TestHygienePackStoreCensus`.
- *read-only stat, never repack* → only `rev-parse`/`os.Stat`/`os.ReadDir`; no gc,
  prune, or repack anywhere in the path; existing `TestHygienePanelReadOnly` still
  green.
- *in-repo read, not out-of-repo* → each repo's OWN `.git/objects/pack`, resolved via
  that repo's git dir; the `.git` guard prevents crossing into the parent store.
- *gofmt/go vet/go test ./internal/web green (CGO_ENABLED=0)* → verified.
