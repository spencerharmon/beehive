# stats: extensible, stateless, git-derived session tags (foundation for filter/group-by)

## Problem

`/stats` (`internal/web/stats.go`) reports per-submodule delivered(✅)/honeybees(🐝)/
✅-per-🐝/stranded plus a "by model" breakdown, but every one of those facets was
wired as a bespoke, fixed field: honeybee counting matched the transcript file name
against `sessionNameRE`, and the by-model split had its own private `sessionModel`/
`modelStampRE` regex reading just the `· model: <model>` fragment of the header.
Neither derivation had any notion of a general session "tag", so the ROI's
"Tag-based stats grouping & filtering" goal (`stats-filter-groupby`) had nothing to
filter or group BY — there was no single accessor that exposed a session's facets
as an open key→value set, and no way for an install to declare its own labels
(cohorts, experiments) without a code change.

## Fix

### The tag model (`internal/web/tags.go`, new)

A session's tags are a `map[string]string`, derived entirely from git — nothing is
persisted, so a tag can never drift from the session it describes.

- `sessionRef{submodule, path}` is the only input: which submodule's `sessions/`
  dir owns the transcript, and its file path.
- **Built-in tags** are `{submodule, kind, branch, model}`, listed in
  `builtinFacets` — an OPEN set: a future built-in is added there and emitted in
  `sessionTags`, and config tags can already key off any of them. `/stats` never
  hard-codes a fixed schema.
- Built-ins are parsed by `audit.ParseFile` — the SAME parser
  `audit-parse-model-header` gave the session-audit engine (the
  `submodule · kind · branch` header line, cross-checked against the file name, plus
  the trailing `· model: <model>` field). Reusing it single-sources the parse: the
  tag model and the audit engine can never disagree about what a session is.

### The accessor

`(*Server).sessionTags(sessionRef) map[string]string` is the one entry point.

1. Parse the built-ins via `audit.ParseFile`. A parse failure (legacy/missing/
   malformed header, or a file name that disagrees with the header's branch) is NOT
   fatal — the built-ins it can't derive are simply omitted, no error/panic. A
   session with no `model:` header omits `model` (the accessor never guesses a
   default — that policy stays in `computeStats`, see below).
2. Layer config-declared tags: for each built-in facet the session actually has, merge
   in the labels the layered config maps that facet value to. The built-in facet
   values are snapshotted BEFORE applying config tags, so a config-declared label
   can never itself feed another config lookup; the result is independent of map
   iteration order.

### Config-declared tags (`internal/config/config.go`)

`Config.Tags` is the open, three-level map `facet -> facet-value -> (tag-key ->
tag-value)`, e.g.

```yaml
tags:
  submodule:
    frontend: { cohort: A }
  model:
    github-copilot/claude-opus-4.8: { tier: frontier }
```

It layers through the existing config precedence (Defaults -> host -> in-repo
global -> per-submodule) via `mergeTags`, a three-level deep merge: a more specific
layer overrides a single LEAF label while its siblings fall through, and a blank
leaf is treated as unset — the same "zero == unset" rule `mergeEnv`/`Models` already
use, one level deeper. `mergeTags` never mutates a lower layer's map (base is
deep-copied before layering `over` on top).

### Behavior preservation (`internal/web/stats.go`)

`computeStats`'s per-session model now comes from
`sessionTags(sessionRef{...})["model"]` instead of the deleted
`sessionModel`/`modelStampRE`. When the tag is absent it still falls back to
`defaultModel` (opus) — that default is the by-model VIEW's policy for unstamped
history, deliberately kept in `computeStats` and NOT pushed into the accessor, which
honestly omits an absent model. Honeybee counting, epoch/pid latest-session
ordering, and delivered-task model attribution are all unchanged. The default
`/stats` output — and `TestComputeStats` / `TestComputeStatsPerModel` — are
byte-identical to before.

`writeTranscript` (stats_test.go) now derives the transcript header's `branch:`
field from the file stem instead of a fixed `bee-x`, so it agrees with the file
name the way a real finalized session does — `audit.ParseFile`'s cross-check
rejects a header/name mismatch, which the tag model now depends on for every test
fixture, not just the model regex the old code used.

## Tests

- `internal/web/web_test.go` `TestSessionTags` — a fully-stamped session yields all
  four built-ins plus two config tags keyed on two DIFFERENT facets (submodule and
  model, proving the schema is open); a no-model-header session omits `model` (and
  the tag keyed on it) while the submodule-keyed tag still attaches; an unparsable/
  headerless transcript yields an empty tag set with no error or panic.
- `internal/config/config_test.go` `TestResolveTagsLayering` — the three-level deep
  merge across host/global/submodule layers: override-wins on a shared leaf, an
  untouched sibling leaf falls through from a lower layer, a facet/value present in
  only one layer survives, and a blank leaf at the most specific layer never wipes a
  lower layer's value.
- `internal/web/stats_test.go` / `internal/web/web_test.go` `TestComputeStats*` —
  unchanged assertions, confirming the refactor is behavior-preserving.

Out of scope (that is `stats-filter-groupby`): any filter bar, group-by selector, or
tag-rendering UI. This task ships only the tag model, the accessor, and the config
schema it reads from.

`CGO_ENABLED=0 go build ./...` and `go test ./...` green.
