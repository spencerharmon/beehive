# chat-editor-status-poll-failing-test

Failing-test-first reproduction of the chat-diff editor's stuck-status bug on the
**polling path** (no product code changed by this task).

## The bug

The editor panel's live status indicator ("editing…"/"working…") is driven by
`editor.Session.Busy()`: the `/editor` state poll in `internal/web/editor.go`
reports `"busy": sess.Busy()` verbatim, and the template renders the working
indicator from it.

`Session.runTurn` (`internal/editor/editor.go`) keeps `busy == true` across ALL
post-reply work — the local `CommitPaths`, the durability transcript commit, and
`PushBranchReconciled` — clearing it only at the very end. So the moment the
assistant's reply is appended to the log and ready to read, the status is STILL
"working…" until that publish work finishes. When a trusted remote's push is slow
(or stalled), the indicator sticks long after the reply is ready — the recurring
stuck-status the ROI calls out (the prior `chat-editor-working-indicator-clear`
fix addressed only the idle-swap, not this polling window).

## The reproduction

`TestStatusClearsWhenReplyReadyWhilePublishing` in
`internal/editor/status_poll_test.go`:

1. Sets up a repo-own bare remote (reusing `remoteSetup`) so `sess.remote ==
   "origin"` and the durability push runs.
2. Installs a `pre-receive` hook on the bare remote that stalls every push until a
   sentinel file is removed (capped at ~20s so a crashed test cannot hang the
   suite) — giving a deterministic window where the reply is recorded but the push
   is still in flight.
3. Drives a turn via `StartChat` (the async path the UI uses).
4. Waits until the assistant reply is present in `sess.Log()` (reply ready).
5. Asserts `sess.Busy() == false` (status cleared).

It FAILS today because `Busy()` is still true (stuck on "working…") while the
stalled push holds the turn open. On the failure path it releases the sentinel and
drains the background turn before returning, so temp-dir teardown never races the
in-flight push.

## Status

This is the intended failing gate for the follow-up fix task
(`chat-editor-status-poll-fix`). The rest of `go test ./...` stays green under
`CGO_ENABLED=0`; only this reproduction test is red.
