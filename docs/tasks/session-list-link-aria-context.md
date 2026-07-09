# session-list-link-aria-context: name the session-list deep links for screen readers

## Problem

`session-list-links-labels` added three deep links per session-list row —
`session_list_body.html:8-10` render `<a href="{{.TaskHref}}">task</a>`, `doc`,
and `commit` — but each carries the bare, identical word `task`/`doc`/`commit` on
EVERY row with no accessible name. A screen-reader user navigating link-by-link
(links list / rotor) hears "task, doc, commit" repeated all the way down the list
with no indication which session each set belongs to.

WCAG 2.4.4 (Link Purpose In Context, A) is borderline — the enclosing `<li>` opens
with the session-name link, so it may pass at Level A — and 2.4.9 (Link Only, AAA)
fails outright. More concretely it breaks the codebase's own convention of giving
repeated/ambiguous controls a per-instance accessible name:
`stats.html:27` (`aria-label="remove filter …"`), the copy buttons in
`branch_view.html:25` / `commit_view.html:3` (`aria-label="Copy commit …"`), and
the bee badges in `dashboard.html:28` / `stats.html:78`.

## Design

Add an `aria-label` to each of the three links naming the session it belongs to,
reusing `.Display` (the shortened session name already in scope in the
`{{range .Sessions}}` block):

```html
{{if .TaskHref}}<a href="{{.TaskHref}}" aria-label="task for {{.Display}}">task</a>{{end}}
{{if .DocHref}}<a href="{{.DocHref}}" aria-label="change doc for {{.Display}}">doc</a>{{end}}
{{if .FlipHref}}<a href="{{.FlipHref}}" aria-label="commit for {{.Display}}">commit</a>{{end}}
```

- `session_list_body.html` only. No visual change: `aria-label` is invisible to
  sighted users and the visible link text (`task`/`doc`/`commit`) and layout are
  untouched.
- `.Display` is a plain string, so `html/template` attribute-escapes it in the new
  attribute context automatically (same value already rendered as the name link's
  text node on line 4).
- `session_view.html` is intentionally out of scope: its `<h1>` already names the
  subject, so its links are not context-free.

## Tests — `internal/web/web_test.go`

`TestSessionListLinksTaskDocCommit`'s session-list-body assertions now require the
context-specific aria-label on each of the three links
(`aria-label="task for t1"`, `"change doc for t1"`, `"commit for t1"` for the
`bee-t1-1700000000-11111` fixture whose `.Display` is `t1`), asserting the label is
present while the visible text is unchanged. The session-view assertions in the
same test are left as-is (that page is out of scope), and
`TestSessionListLinksDegradeGracefully`'s negative controls are unaffected (orphan/
legacy sessions render no links at all).
