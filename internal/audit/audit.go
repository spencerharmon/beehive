// Package audit is the deterministic metrics engine for the recurring session
// self-audit (the session-audit-NNN series). It mines the raw honeybee
// transcripts under submodules/<sm>/sessions/<branch>-<epoch>[-<pid>].md into
// reproducible per-session and per-task numbers, selects the next un-audited
// batch in an N-2 window, and persists an append-only ledger under
// submodules/<sm>/docs/audit/ that doubles as the audited-marker and the
// tracked-trend record.
//
// Everything here is pure Go (CGO-free) and reads ONLY the beehive coordination
// layer (sessions/, docs/audit/, PLAN.md). It never reads or writes a target's
// repo/ checkout. The audit PASS only diagnoses and enqueues fix tasks; it must
// not recompute these values by hand, or the "trend is improving" claim is
// meaningless.
//
// # Turn-count rule (PINNED)
//
// A "turn" is one assistant message, counted as the number of lines exactly
// equal to "## assistant" (the producer marker emitted by
// internal/swarm.renderTranscript, "\n## %s\n\n" with role "assistant").
// Matching the exact line — not a "## " prefix — is required: assistant output
// embeds its own level-2 markdown headers (e.g. "## Notes", "## Goal") which
// would otherwise be miscounted. Validated against the real corpus this rule
// reproduces the ROI-cited spread exactly: min 14 turns (bee-reconcile-1782772649)
// to max 102 (bee-links-graph-enforcement-1782767318), inside 60–330 KB.
//
// # Heuristics are conservative flags, not inferences
//
// The protocol prompt itself (echoed into every transcript's user turn) contains
// the strings "lost the race", "ErrLost", and "STOP", so a whole-file text scan
// for lost-claim language false-positives on essentially every session. The only
// deterministic, producer-emitted abort signal is the trailing "## ⚠️ warning"
// block that internal/swarm.recorder.appendWarning writes when the runner aborts
// a session (wall/turn cap, lost claim, task removed). Abort/lost-race detection
// therefore keys off that block ONLY — and only the LAST exact occurrence of the
// header line, and only when it is genuinely trailing (no further turn occurs
// after it): a session's own work routinely greps/quotes a PRIOR session's
// transcript as evidence (the session-audit series' explicit charter), which can
// embed the exact header line mid-body, and such an occurrence must never gate
// the turn count or seed the abort reason. reconcile-loop is a corpus-level
// property (adjacent reconcile sessions in epoch order) set by ParseDir, not by
// a single file.
package audit

// Session kinds, mirroring internal/swarm selection kinds as written into the
// transcript header line "submodule: <sm> · kind: <kind> · branch: <branch>[
// · model: <model>]".
const (
	KindBootstrap   = "bootstrap"
	KindWork        = "work"
	KindReview      = "review"
	KindArbitration = "arbitration"
	KindReconcile   = "reconcile"
)

// Session is the extracted, reproducible metric record for one transcript file.
// All fields are derived deterministically from the file name and bytes; no
// value is eyeballed.
type Session struct {
	ID        string // file stem, e.g. "bee-git-remote-ops-1782789603-253372" (may carry a trailing -<pid> suffix)
	Epoch     int64  // first numeric segment after the "<branch>-" prefix in the file name
	Submodule string // header "submodule:" field
	Kind      string // header "kind:" field
	Branch    string // header "branch:" field (authoritative; the file name's prefix)
	TaskID    string // Branch minus the "bee-" prefix (the -<pid> suffix is never folded in)
	Model     string // header "model:" field (commit 248e967); "" for a legacy/unstamped session
	Bytes     int64  // file size in bytes
	Turns     int    // count of "## assistant" lines (PINNED turn rule)
	UserTurns int    // count of "## user" lines (informational)

	Heuristics Heuristics
}

// Heuristics are cheap, conservative review flags. A false "abandoned" is cheap;
// a fake "delivered" is not — so these only ever flag, never assert delivery.
type Heuristics struct {
	// Aborted is true iff the runner appended a "## ⚠️ warning" block, the only
	// deterministic on-disk signal that the session ended abnormally.
	Aborted bool
	// AbortReason is the first non-empty line of the warning block, "" if none.
	AbortReason string
	// LostRace is true when Aborted and the warning text (scoped to the block,
	// never the prompt-polluted body) names a lost claim / forced reselect.
	LostRace bool
	// CompletionMiss flags a work session the runner cut off before handoff
	// (Kind == work and Aborted): it never reached NEEDS-REVIEW.
	CompletionMiss bool
	// ReconcileLoop marks a reconcile session that is adjacent (in epoch order)
	// to another reconcile session with no other-kind work between them — the
	// reconcile-fired-repeatedly-without-progress waste pattern. Set by ParseDir.
	ReconcileLoop bool
}
