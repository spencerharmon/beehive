package web

import (
	"github.com/spencerharmon/beehive/internal/audit"
)

// sessionRef identifies one finalized honeybee transcript for tagging: the
// submodule whose sessions dir owns it and the transcript file path. It carries
// only what the STATELESS tag derivation needs — everything a tag reports is
// read back out of git (the transcript header, its file name, the model header),
// never stored, so a tag can't drift from the session it describes.
type sessionRef struct {
	submodule string // owning submodule (the sessions dir's submodule)
	path      string // path to the transcript .md
}

// builtinFacets are the built-in tag keys sessionTags derives from a transcript,
// and equally the facet keys an install may key config-declared tags off of. The
// SET IS OPEN by design (stats-tag-model): a new built-in is added here + emitted
// in sessionTags, and config tags can already key off any of them — /stats never
// hard-codes a fixed schema. Iterated in this stable order when applying config
// tags so a render is deterministic.
var builtinFacets = []string{"submodule", "kind", "branch", "model"}

// sessionTags is THE stateless accessor from a finalized session to its full tag
// set: the built-in tags {submodule, kind, branch, model} parsed from git ALONE
// — the transcript header line "submodule: X · kind: Y · branch: Z", cross-checked
// against the file name, plus the "· model: <model>" header — reusing the SAME
// audit.ParseFile the session-audit engine uses (so the two never drift), MERGED
// with any config-declared tags the install layered on (config.Tags: a
// facet-value -> label map). Nothing is stored; every tag is re-derived on read.
//
// Leniency is deliberate and matches the rest of /stats. A session whose header
// is legacy/missing/malformed, or that fails the file-name cross-check, simply
// OMITS the built-ins it can't derive — no error, no panic. A session with no
// model header OMITS `model` (rather than guessing a default; the by-model stats
// view keeps its own opus-default policy in computeStats). A config tag attaches
// only where its facet value is present, so an omitted facet just means the
// labels keyed on it don't apply.
//
// This is the FOUNDATION accessor for stats-filter-groupby; it builds only the
// tag model, no filter/group-by UI.
func (s *Server) sessionTags(sess sessionRef) map[string]string {
	tags := map[string]string{}

	// Built-ins: reuse audit.ParseFile (header parse + file-name cross-check +
	// model-header parse). A parse failure is not fatal — leave the built-ins
	// unset and fall through to whatever config tags still match (none, if the
	// session has no derivable facet).
	if a, err := audit.ParseFile(sess.path); err == nil {
		setTag(tags, "submodule", a.Submodule)
		setTag(tags, "kind", a.Kind)
		setTag(tags, "branch", a.Branch)
		setTag(tags, "model", a.Model) // "" when the header carries no model -> omitted
	}

	// Config-declared tags: for each derived built-in facet, merge in the labels
	// the layered config maps its value to. Snapshot the built-in facet values
	// FIRST so a config-declared label can never feed another config label — the
	// built-ins are the only facets, and the result is independent of iteration
	// order.
	built := make(map[string]string, len(builtinFacets))
	for _, facet := range builtinFacets {
		if v, ok := tags[facet]; ok {
			built[facet] = v
		}
	}
	for _, facet := range builtinFacets {
		val, ok := built[facet]
		if !ok {
			continue
		}
		for k, v := range s.cfg.Tags[facet][val] {
			if v != "" {
				tags[k] = v
			}
		}
	}
	return tags
}

// setTag records key=val only when val is non-empty, so a missing facet is
// OMITTED from the tag set rather than mapped to an empty string.
func setTag(tags map[string]string, key, val string) {
	if val != "" {
		tags[key] = val
	}
}
