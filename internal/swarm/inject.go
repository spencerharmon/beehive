package swarm

import (
	"strings"

	selectt "github.com/spencerharmon/beehive/internal/select"
)

// The runner injects the honeybee protocol as the opencode `system` prompt on
// EVERY turn (see ocSession.Prompt), so anything an agent never acts on is paid
// for on every turn of every pass. trimProtocol derives the LEAN per-pass system
// prompt from the full HONEYBEE.md: it keeps exactly what a pass of `kind` needs
// — the absolute rules, the claim model, the tooling + turn-loop notes, and the
// protocol steps that kind acts on — and drops managed boilerplate (the
// file-governance intro paragraphs, the "read once" topology, the dispatch
// framing) plus the protocol steps OWNED by other kinds (a Work pass never
// carries review/arbitration/reconcile prose, and vice-versa).
//
// SAFETY: trimming is anchored on the "## Absolute rules" heading. A protocol
// that lacks it (an operator rewrote the file past recognition) is returned
// UNCHANGED, so a rule we cannot account for is never silently dropped. Only
// positively-identified boilerplate sections are removed; any section we do not
// recognize is kept, and every shared (unmarked) protocol step is kept for all
// kinds — so no cross-cutting rule is lost.
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
		case strings.HasPrefix(s.title, "Protocol"):
			kept = append(kept, filterProtocolSteps(s.text, kind))
		default:
			kept = append(kept, s.text)
		}
	}
	return strings.Join(kept, "\n\n") + "\n"
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
// file-governance paragraphs that follow (how the file is managed/refreshed — an
// agent never acts on it). Paragraphs are blank-line separated.
func trimIntro(intro string) string {
	paras := strings.Split(intro, "\n\n")
	if len(paras) > 2 {
		paras = paras[:2]
	}
	return strings.TrimRight(strings.Join(paras, "\n\n"), "\n")
}

// stepOwner maps a numbered protocol step to the single kind that acts on it, by
// a leading bold marker. A step with no recognized marker is shared and kept for
// every kind.
var stepOwners = []struct {
	marker string
	kind   selectt.Kind
}{
	{"**ROI reconcile", selectt.Reconcile},
	{"**Arbitration first", selectt.Arbitrate},
	{"**Review next", selectt.Review},
	{"**Main task last", selectt.Work},
}

// filterProtocolSteps keeps the "## Protocol" heading and every step EXCEPT those
// a different kind owns: a Work pass drops the reconcile/arbitration/review steps,
// a Review pass keeps only its review step, etc. Shared (unmarked) steps — the
// claim check, human escalation, DONE-unlock, the docs requirement, the ROI ban —
// are kept for every kind, so no cross-cutting rule is lost.
func filterProtocolSteps(section string, kind selectt.Kind) string {
	lines := strings.Split(section, "\n")
	var out []string
	keep := true // the heading + any lead-in before the first numbered step
	for _, ln := range lines {
		if isStepStart(ln) {
			keep = keepStep(ln, kind)
		}
		if keep {
			out = append(out, ln)
		}
	}
	return strings.Join(out, "\n")
}

// isStepStart reports whether a line begins a top-level numbered protocol step
// ("0. ", "1. ", ...). Indented continuation and sub-items ("   a. ") never match
// because the leading digit test fails on a leading space.
func isStepStart(line string) bool {
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	return i > 0 && i+1 < len(line) && line[i] == '.' && line[i+1] == ' '
}

// keepStep decides whether a numbered step belongs in a pass of `kind`. A step
// marked as owned by one kind is kept only for that kind; an unmarked (shared)
// step is always kept.
func keepStep(stepLine string, kind selectt.Kind) bool {
	body := stepLine
	if i := strings.IndexByte(body, ' '); i >= 0 {
		body = body[i+1:] // strip the "N. " label
	}
	for _, o := range stepOwners {
		if strings.HasPrefix(body, o.marker) {
			return o.kind == kind
		}
	}
	return true
}
