# form-input-accessible-labels: give placeholder-only form fields a programmatic name

## Problem

A UI accessibility audit (ui-audit-001, Finding #3) found ~a dozen text/password
inputs across 9 templates that carried only a `placeholder` and no `<label>` /
`aria-label`. A placeholder is **not** a reliable accessible name: it disappears
the moment the field has a value and is inconsistently exposed to assistive tech.
So most forms in the app had no programmatic field identity — a screen-reader user
lands on an unnamed edit box.

`stats.html` already did this correctly for its operator `<select aria-label="filter
operator">` and the filter-remove link's `aria-label`, so this is an established
pattern to extend, not a new one to invent.

## Design

Two paired patterns, chosen per form so each field resolves a non-empty accessible
name via the standard label/aria-label name computation, while every existing
placeholder is retained as supplementary hint text. No field's visible layout
changes.

### `.sr-only` utility — `internal/web/assets/style.css`

The visually-hidden utility already shipped (from `design-system-css`, the standard
`position:absolute; clip:rect(0 0 0 0); 1px` off-screen pattern, already used for the
skeletons' "Loading…" equivalent). Only its doc comment is broadened to name its
second use — sr-only `<label>`s for placeholder-only fields. Because an sr-only
`<label>` is `position:absolute`, it is out-of-flow: inside the `display:flex` forms
it is **not** a flex item and does not participate in `gap`/layout, so adding one is
visually inert (no regression).

### sr-only `<label for>` — structured create/merge/link forms

For forms that render once per page (so a static `id` is unique), a visually-hidden
label is paired to the input by `for`/`id`:

- `dashboard.html` add-submodule: url → `Repository URL`, name → `Submodule name`,
  branch → `Branch` (`add-sub-url` / `add-sub-name` / `add-sub-branch`).
- `merge_panel.html`: the submodule `<select>` → `Submodule` (`merge-name`), the
  branch input → `Branch to merge` (`merge-branch`).
- `branch_view.html`: the inline merge branch input → `Branch to merge`
  (`branch-merge-branch`).
- `links_editor.html`: from → `From submodule` (`link-from`), to → `To submodule`
  (`link-to`).

### `aria-label` — looped / chat / stats fields

Where a static `id` would collide (a field rendered inside a `{{range}}`) or where a
sibling already uses `aria-label`, the name goes directly on the input:

- `secrets_panel.html`: the per-key edit value input is inside `{{range .Keys}}`, so
  `aria-label="new value for {{.}}"` (unique per key, no duplicate id); the add form's
  key/value → `secret key` / `secret value`.
- `stats.html`: `fkey` → `filter tag key`, `fval` → `filter value`, the group-by
  extra-keys input → `extra group-by tag keys` — matching the `fop`
  `<select aria-label="filter operator">` already in the same form.
- `editor.html` / `bootstrap_agent.html` / `human_resolve.html`: the chat-style
  `message` inputs → `message to the editor` / `message to the setup guide` /
  `message to the resolution agent`.

Hidden inputs (`type="hidden" name="name"`) are excluded — they are not focusable and
carry no accessible name.

## Tests — `internal/web/web_test.go`

`TestFormInputsHaveAccessibleNames` (new): renders each of the 9 touched templates
(the two full-page ones — dashboard, stats — through the real GET handler; the rest
via `renderTmpl`) and asserts, per template, the exact accessible-name markup — the
`<label class="sr-only" for=…>` + matching `id=…` for the labelled forms, and the
`aria-label=…` for the aria-labelled fields — plus that a representative placeholder
survives as a hint. The secrets case renders with one key so both the looped edit
input and the add form are exercised.

## Acceptance mapping

- *every listed input resolves a non-empty accessible name via the standard
  label/aria-label computation* → the paired `<label for>`/`id` and `aria-label`
  above, one per listed field; asserted by `TestFormInputsHaveAccessibleNames`.
- *placeholders retained as hints* → every edit kept the original `placeholder`; the
  test asserts a representative placeholder per template still renders.
- *no visual layout regression* → sr-only content is `position:absolute` + clipped
  (out-of-flow, so no flex/gap impact); `aria-label` adds no DOM. No visible node was
  added to any form.
- *tests assert label/aria-label presence for at least one input per touched
  template* → `TestFormInputsHaveAccessibleNames` covers all 9 templates.
