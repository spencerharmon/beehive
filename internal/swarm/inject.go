// Injection trimming. Every turn the runner re-sends the honeybee protocol as the
// opencode system prompt, so any byte in it is paid for on EVERY turn of EVERY
// pass. scopeProtocol/workPreamble/workContinueHint cut that recurring cost when
// the opt-in `trim_injection` config is set, WITHOUT losing a protocol rule:
//
//   - stripFileMeta drops the managed provenance paragraph (where the file comes
//     from, that `beehive instruction update` refreshes it) — pure boilerplate the
//     agent never acts on.
//   - scopeSteps drops only the numbered Protocol steps that belong to OTHER kinds
//     (a Work bee never arbitrates/reviews/reconciles), and only after verifying
//     the step still carries its expected marker, so a reworded protocol is never
//     mangled. The Absolute rules, the shared steps, and this kind's own phase step
//     are always retained.
//   - workPreamble(trim) stops front-loading the completion mechanics that retained
//     Protocol step 4 already carries; workContinueHint re-surfaces them with the
//     CONCRETE resolved doc path at the decision point (the `continue` turns), where
//     the agent is actually about to finish, instead of in turn 1.
//
// Anything the trimmer does not positively recognize is returned unchanged, so an
// operator-customized HONEYBEE.md degrades to a no-op rather than a silent edit.
package swarm

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	selectt "github.com/spencerharmon/beehive/internal/select"
)

// fileMetaMarker starts the managed provenance paragraph in HONEYBEE.md. It is the
// only place this sentence appears, so it is a safe anchor for stripFileMeta.
const fileMetaMarker = "**This file is the authoritative honeybee protocol."

// protocolHeading is the section whose numbered steps scopeSteps gates by kind.
const protocolHeading = "\n## Protocol\n"

// protocolStepHead matches a top-level numbered Protocol step head ("0. ", "1. "),
// anchored at column 0 so indented sub-items ("   a.") and prose are never matched.
var protocolStepHead = regexp.MustCompile(`(?m)^(\d+)\. `)

// kindStepMarker maps a phase-specific Protocol step number to a keyword that MUST
// still appear in that step before scopeSteps will drop it. This is a content guard:
// if an operator reworded the protocol so the keyword is gone, we keep the step
// rather than risk removing the wrong paragraph. Steps 1, 5, 6, 7, 8 are shared by
// every kind and are absent here, so they are never candidates for dropping.
var kindStepMarker = map[int]string{
	0: "ROI reconcile",     // reconcile phase
	2: "Arbitration first", // arbitrate phase
	3: "Review next",       // review phase
	4: "Main task last",    // work phase
}

// kindDropSteps lists the phase-specific Protocol steps that belong to OTHER kinds
// and may be dropped from this kind's injected protocol. The step this kind DOES
// perform, and every shared step, is retained. Bootstrap (and any unmapped kind)
// drops nothing.
func kindDropSteps(kind selectt.Kind) map[int]bool {
	switch kind {
	case selectt.Work:
		return map[int]bool{0: true, 2: true, 3: true}
	case selectt.Review:
		return map[int]bool{0: true, 2: true, 4: true}
	case selectt.Arbitrate:
		return map[int]bool{0: true, 3: true, 4: true}
	case selectt.Reconcile:
		return map[int]bool{2: true, 3: true, 4: true}
	default:
		return nil
	}
}

// scopeProtocol trims the injected honeybee protocol to the running kind: it drops
// the managed provenance paragraph and the numbered phase steps that belong to
// OTHER kinds, while ALWAYS retaining the Absolute rules, the shared steps, and
// this kind's own phase step. Structure it does not recognize is left untouched.
func scopeProtocol(protocol string, kind selectt.Kind) string {
	out := stripFileMeta(protocol)
	if drop := kindDropSteps(kind); len(drop) > 0 {
		out = scopeSteps(out, drop)
	}
	return out
}

// stripFileMeta removes the managed provenance paragraph (from fileMetaMarker
// through the blank line that terminates it) plus that one blank line, leaving the
// surrounding text otherwise byte-for-byte intact. If the marker is absent the
// input is returned unchanged.
func stripFileMeta(protocol string) string {
	i := strings.Index(protocol, fileMetaMarker)
	if i < 0 {
		return protocol
	}
	lineStart := 0
	if nl := strings.LastIndexByte(protocol[:i], '\n'); nl >= 0 {
		lineStart = nl + 1
	}
	end := strings.Index(protocol[lineStart:], "\n\n")
	if end < 0 {
		// No terminating blank line: drop from the paragraph to end of file.
		return protocol[:lineStart]
	}
	// Remove the paragraph and its single trailing blank line ("\n\n" -> drop both).
	return protocol[:lineStart] + protocol[lineStart+end+2:]
}

// scopeSteps removes the numbered Protocol steps whose numbers are in drop, but
// only when the step still contains its expected marker keyword (kindStepMarker).
// It confines itself to the "## Protocol" section so numbered lists elsewhere are
// untouched, and returns the input unchanged if that section or any step head can
// not be recognized.
func scopeSteps(protocol string, drop map[int]bool) string {
	hi := strings.Index(protocol, protocolHeading)
	if hi < 0 {
		return protocol
	}
	secStart := hi + len(protocolHeading)
	secEnd := len(protocol)
	if rel := strings.Index(protocol[secStart:], "\n## "); rel >= 0 {
		secEnd = secStart + rel + 1 // keep the newline that precedes the next heading
	}
	section := protocol[secStart:secEnd]
	locs := protocolStepHead.FindAllStringSubmatchIndex(section, -1)
	if len(locs) == 0 {
		return protocol
	}
	var b strings.Builder
	b.Grow(len(protocol))
	b.WriteString(protocol[:secStart])
	b.WriteString(section[:locs[0][0]]) // any text between the heading and step 0
	for i, loc := range locs {
		stepEnd := len(section)
		if i+1 < len(locs) {
			stepEnd = locs[i+1][0]
		}
		step := section[loc[0]:stepEnd]
		num, _ := strconv.Atoi(section[loc[2]:loc[3]])
		if drop[num] {
			if kw, ok := kindStepMarker[num]; ok && strings.Contains(step, kw) {
				continue // verified phase step for another kind: drop it
			}
		}
		b.WriteString(step)
	}
	b.WriteString(protocol[secEnd:])
	return b.String()
}

// workPreamble builds the runner's injected Context preamble for a Work task. With
// trim=false it reproduces the historical inline preamble byte-for-byte (the
// byte-stable default). With trim=true it omits the completion-mechanics block
// (doc path, stamp, pointer bump, NEEDS-REVIEW flip): that guidance is already
// carried verbatim by retained Protocol step 4 in the system prompt and is
// re-surfaced with the concrete resolved path at the decision point by
// workContinueHint, so front-loading it in turn 1 is redundant.
func workPreamble(smName, branch, taskID string, trim bool) string {
	head := fmt.Sprintf(
		"# Context\nYou are working from the beehive repo root (cwd). Submodule: %[1]s.\n"+
			"Coordination files (the beehive layer): submodules/%[1]s/ROI.md (read-only), "+
			"submodules/%[1]s/PLAN.md, submodules/%[1]s/docs/.\n"+
			"Code worktree (already created and checked out for you): submodules/%[1]s/worktrees/%[2]s/ "+
			"on branch %[2]s. Edit the submodule's CODE there; never write submodules/%[1]s/repo (the shared checkout).\n",
		smName, branch)
	tail := "Act autonomously: do not ask for confirmation; make the edits and commits the protocol requires.\n\n"
	if trim {
		return head + tail
	}
	mechanics := fmt.Sprintf(
		"On completion of a Work task: PLAN.md -> NEEDS-REVIEW on main; commit the code on branch %[2]s "+
			"with a `Beehive: %[3]s <doc-path>` stamp and ensure that commit is pushed to the submodule's origin; "+
			"bump the submodule pointer.\n"+
			"REQUIRED change doc path: submodules/%[1]s/docs/%[2]s-%[3]s.md (the beehive layer — NOT inside the code "+
			"worktree). The runner's completion check looks for it exactly there; a doc elsewhere reads as 'not done'.\n",
		smName, branch, taskID)
	return head + mechanics + tail
}

// workContinueHint is the per-turn "continue" prompt for a trimmed Work session. It
// puts the completion mechanics — with the CONCRETE resolved change-doc path and
// commit stamp — at the decision point, i.e. the turns where the agent is iterating
// toward finishing, rather than front-loaded in the turn-1 preamble.
func workContinueHint(smName, branch, taskID string) string {
	doc := fmt.Sprintf("submodules/%s/docs/%s-%s.md", smName, branch, taskID)
	return fmt.Sprintf(
		"continue. Once the code change is made and TESTED, complete the task: write the change doc at EXACTLY "+
			"%[1]s (the beehive layer, NOT inside the worktree — the completion check requires that exact path); "+
			"commit the code on branch %[2]s with a `Beehive: %[3]s %[1]s` stamp and push that commit to the "+
			"submodule's origin; bump the submodule pointer; then flip the PLAN.md task to NEEDS-REVIEW on main.",
		doc, branch, taskID)
}
