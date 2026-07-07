# stats-tag-model — extensible, stateless, git-derived session tags

## Goal

Recast the hard-coded, single-facet way `/stats` looks at a session (a bespoke
`model:` header parse) into an **extensible, stateless, git-derived key→value TAG
set** per session, exposed behind ONE accessor, and refactor the existing by-model
math onto it with no change to the default `/stats` output. This is the foundation
for `stats-filter-groupby`; it ships only the tag model, no filter/group-by UI.

## The tag model

A session's tags are a `map[string]string` derived entirely from git — never
stored, so they cannot drift from the session they describe.

- **Input** is `sessionRef{submodule, path}`: which submodule owns the transcript
  and its file path. That is all the derivation needs.
- **Built-in tags** are `{submodule, kind, branch, model}`, and the set is OPEN:
  `builtinFacets` lists them, a new one is added there + emitted in the accessor,
  and config tags can already key off any of them. `/stats` hard-codes no fixed
  schema.
- Built-ins are parsed by **`audit.ParseFile`** — the exact parser the
  session-audit engine already uses (the `submodule · kind · branch` header,
  cross-checked against the file name, plus the trailing `· model:` header). Reusing
  it single-sources the parse: the tag model and the audit engine can never disagree
  about what a session is.

## The accessor

`(*Server).sessionTags(sessionRef) map[string]string` is the single entry point.

1. Parse the built-ins via `audit.ParseFile`. A parse failure is NOT fatal — the
   built-ins it can't derive are simply omitted (no error, no panic), matching the
   leniency the rest of `/stats` already relies on. A session with no `model:`
   header omits `model` (the accessor never guesses a default).
2. Layer config-declared tags: for each derived built-in facet value, merge in the
   labels the layered config maps it to. Built-in facet values are snapshotted
   BEFORE applying config tags, so a config-declared label can never feed another
   config label; the result is independent of iteration order.

Nothing is persisted; every call re-derives from git.

## Config-declared tags

`config.Config.Tags` is `facet → facet-value → (tag-key → tag-value)`, e.g.

```yaml
tags:
  submodule:
    frontend: { cohort: A }
  model:
    github-copilot/claude-opus-4.8: { tier: frontier }
```

It is an OPEN schema (arbitrary facet + label keys) and layers through the existing
config precedence (Defaults → host → in-repo global → per-submodule) via `mergeTags`,
a three-level deep merge: a more specific layer overrides a single leaf label while
its siblings fall through, and a blank leaf is treated as unset (the same
"zero == unset" rule as every scalar and `mergeEnv`).

## Behavior preservation

`computeStats` now takes each session's model from `sessionTags(...)["model"]`
instead of its own `sessionModel`/`modelStampRE` (both deleted). When the tag is
absent it still falls back to `defaultModel` (opus) — that default is the by-model
VIEW's policy for unstamped history and stays in `computeStats`, deliberately NOT in
the accessor, which honestly omits an absent model. Honeybee counting, epoch/pid
ordering, and delivery attribution are untouched. `TestComputeStats` /
`TestComputeStatsPerModel` are unchanged and green.

## Tests / out of scope

- `TestSessionTags` (web): built-ins from git, config tags layered on two different
  facets, absent-model omission, unparsable→empty.
- `TestResolveTagsLayering` (config): three-level deep merge across host/global/
  submodule, including override-wins, sibling-fall-through, blank-never-overrides,
  and single-layer survival.
- Out of scope (belongs to `stats-filter-groupby`): any filter bar, group-by, or
  tag-rendering UI. This task is the accessor + config schema only.
