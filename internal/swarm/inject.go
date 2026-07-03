package swarm

import (
	"strings"

	selectt "github.com/spencerharmon/beehive/internal/select"
)

// The runner injects the honeybee protocol as the opencode `system` prompt on
// EVERY turn (see ocSession.Prompt), so anything an agent never acts on is paid
// for on every turn of every pass. trimProtocol derives the LEAN per-pass system
// prompt from the full HONEYBEE.md: it keeps exactly what a pass of `kind` needs
// — the absolute rules, the claim model, the status transitions, the shared
// steps, and the tooling + turn-loop notes — plus the ONE role section that kind
// acts on, and drops managed boilerplate (the file-governance intro paragraph,
// the "read once" topology) plus the role sections OWNED by other kinds (a Work
// pass never carries the Review/Arbitration/Reconcile task sections, and
// vice-versa).
//
// SAFETY: trimming is anchored on the "## Absolute rules" heading. A protocol
// that lacks it (an operator rewrote the file past recognition) is returned
// UNCHANGED, so a rule we cannot account for is never silently dropped. Only
// positively-identified boilerplate and other-kind role sections are removed;
// any section we do not recognize is kept for every kind — so no cross-cutting
// rule is lost.
func trimProtocol(protocol string, kind selectt.Kind) string {
	if !hasAbsoluteRules(protocol) {
		return protocol
	}
	var kept []string
	for _, s := range splitSections(protocol) {
		switch {
		case s.title == "": // the pre-heading intro
			if t := trimIntro(s.text); t != "" {
				kept = append(kept, t)
			}
		case dropSection(s.title):
			// managed boilerplate the agent never acts on: drop it
		default:
			if owner, isRole := roleOwner(s.title); isRole && owner != kind {
				continue // a role section owned by a different kind
			}
			kept = append(kept, s.text)
		}
	}
	return strings.Join(kept, "\n\n") + "\n"
}

// roleSections maps an H2 role-section title prefix to the single kind that acts
// on it. A section whose title matches none of these is shared and kept for every
// kind; a section owned by one kind is dropped for all others.
var roleSections = []struct {
	prefix string
	kind   selectt.Kind
}{
	{"Reconcile task", selectt.Reconcile},
	{"Work task", selectt.Work},
	{"Review task", selectt.Review},
	{"Arbitration task", selectt.Arbitrate},
}

// roleOwner reports the kind that owns a role section, or ok=false when the title
// is not a recognized per-kind role section (a shared section, kept for all).
func roleOwner(title string) (selectt.Kind, bool) {
	for _, r := range roleSections {
		if strings.HasPrefix(title, r.prefix) {
			return r.kind, true
		}
	}
	return "", false
}

// hasAbsoluteRules is the recognition anchor: we only trim a protocol that still
// carries the absolute-rules section, so an unrecognizable custom file is passed
// through verbatim rather than trimmed blind.
func hasAbsoluteRules(protocol string) bool {
	return strings.HasPrefix(protocol, "## Absolute rules") ||
		strings.Contains(protocol, "\n## Absolute rules")
}

// protoSection is one H2 slice of the protocol: title is the heading text without
// the leading "## " ("" for the pre-heading intro); text is the whole section
// (heading line included) with trailing blank lines trimmed.
type protoSection struct {
	title string
	text  string
}

// splitSections cuts the protocol into its intro + H2 sections. Trailing blank
// lines are trimmed from each section so the caller can rejoin the kept ones with
// a single blank-line separator regardless of what was dropped.
func splitSections(doc string) []protoSection {
	lines := strings.Split(doc, "\n")
	var secs []protoSection
	title := ""
	var cur []string
	flush := func() {
		secs = append(secs, protoSection{
			title: title,
			text:  strings.TrimRight(strings.Join(cur, "\n"), "\n"),
		})
	}
	for _, ln := range lines {
		if strings.HasPrefix(ln, "## ") {
			flush() // close the intro or previous H2 section
			title = strings.TrimSpace(strings.TrimPrefix(ln, "## "))
			cur = []string{ln}
			continue
		}
		cur = append(cur, ln)
	}
	flush()
	return secs
}

// dropSection reports whether an H2 section is managed boilerplate a honeybee
// never acts on. Only positively-identified boilerplate is named here; anything
// else (rule sections and any operator-added section) is kept.
func dropSection(title string) bool {
	return strings.HasPrefix(title, "Topology") ||
		strings.HasPrefix(title, "You were started")
}

// trimIntro keeps the H1 and the first framing paragraph and drops the
// file-governance paragraph(s) that follow (how the file is managed/refreshed and
// how the runner dispatches kinds — an agent never acts on it). Paragraphs are
// blank-line separated.
func trimIntro(intro string) string {
	paras := strings.Split(intro, "\n\n")
	if len(paras) > 2 {
		paras = paras[:2]
	}
	return strings.TrimRight(strings.Join(paras, "\n\n"), "\n")
}
