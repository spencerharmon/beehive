// Package artifacts models the two structured-but-markdown per-submodule docs the
// frontend reads: ARTIFACTS.md (the build/deploy artifacts a submodule produces)
// and INFRASTRUCTURE.md (its environment/topology, including the blue/green deploy
// markers the env badge and deploy action key off).
//
// Each doc parses to a typed model and serializes back verbatim, so
// Parse(s).String() round-trips a representative (newline-terminated) file. The
// web reads both through this package instead of raw text or ad-hoc regexes, and
// writers (the deploy action, honeybee doc updates) get one stable API. Pure Go,
// no LLM — the same deterministic, round-trippable contract as internal/plan: the
// document body is preserved line-for-line while the structured fields are derived
// from it.
package artifacts

import (
	"bufio"
	"os"
	"regexp"
	"strings"
)

// Blue/green deployment markers in INFRASTRUCTURE.md:
//
//	Active: blue
//	Environments: blue, green
//
// activeRe/envsRe match them anywhere in the document (multiline,
// case-insensitive) — the exact patterns the frontend env badge historically used,
// so extraction is unchanged.
var (
	activeRe = regexp.MustCompile(`(?mi)^Active:\s*(\S+)`)
	envsRe   = regexp.MustCompile(`(?mi)^Environments:\s*(.+)$`)
)

// DefaultActive and the default environment set are the blue/green fallback the
// frontend assumes when INFRASTRUCTURE.md omits the markers (preserving the
// historical env badge: blue active, {blue,green} available).
const DefaultActive = "blue"

// defaultEnvs returns a fresh copy so callers can never mutate a shared slice.
func defaultEnvs() []string { return []string{"blue", "green"} }

// Infra is a parsed INFRASTRUCTURE.md: the verbatim document plus the blue/green
// deployment markers extracted from it. Active is "" and Envs nil when the file
// omits the markers; Deployment applies the defaults. The body is preserved
// line-for-line so String round-trips and SetActive edits in place.
type Infra struct {
	Active  string   // value of the Active: marker, "" if absent
	Envs    []string // values of the Environments: marker, nil if absent
	body    []string // every line verbatim (newline stripped), for round-trip
	present bool     // the document existed (vs a synthesized empty model)
}

// Deployment is the resolved blue/green state: the document's markers with
// defaults filled in for any that are absent. Its fields mirror the frontend env
// view (Active env + selectable Envs).
type Deployment struct {
	Active string
	Envs   []string
}

// ParseInfra parses INFRASTRUCTURE.md source into a typed model. It never fails:
// an unrecognized document parses to a body with no markers (Deployment then
// yields the defaults).
func ParseInfra(s string) Infra {
	in := Infra{present: true}
	in.body = scanLines(s)
	if m := activeRe.FindStringSubmatch(s); m != nil {
		in.Active = m[1]
	}
	if m := envsRe.FindStringSubmatch(s); m != nil {
		in.Envs = splitCSV(m[1])
	}
	return in
}

// LoadInfra reads and parses INFRASTRUCTURE.md at path. A missing file is not an
// error: it returns a zero (absent) model whose Deployment is the defaults — the
// same lenient contract the env badge relied on. Any other read error is returned.
func LoadInfra(path string) (Infra, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Infra{}, nil
		}
		return Infra{}, err
	}
	return ParseInfra(string(b)), nil
}

// Present reports whether the model came from an existing document. LoadInfra of
// a missing file returns Present()==false, so a view can skip an absent doc.
func (in Infra) Present() bool { return in.present }

// Deployment resolves the blue/green state, substituting DefaultActive / the
// default environment set for any marker the document omits.
func (in Infra) Deployment() Deployment {
	d := Deployment{Active: in.Active, Envs: append([]string(nil), in.Envs...)}
	if d.Active == "" {
		d.Active = DefaultActive
	}
	if len(d.Envs) == 0 {
		d.Envs = defaultEnvs()
	}
	return d
}

// SetActive switches the active environment, rewriting every Active: marker line
// in place (or appending one when the document has none). It mutates the in-memory
// body and Active field; call String to serialize for persistence. This is the
// typed replacement for the frontend's old in-place line rewrite.
func (in *Infra) SetActive(target string) {
	in.Active = target
	found := false
	for i, l := range in.body {
		if activeRe.MatchString(l) {
			in.body[i] = "Active: " + target
			found = true
		}
	}
	if !found {
		in.body = append(in.body, "Active: "+target)
	}
	in.present = true
}

// String serializes the document verbatim. ParseInfra(s).String() reproduces s
// for any newline-terminated file (the canonical form); an empty model serializes
// to "". A trailing newline is always ensured for a non-empty body.
func (in Infra) String() string { return joinLines(in.body) }

// Artifact is one build/deploy artifact a submodule produces: a Name and an
// optional Desc, parsed from a top-level markdown bullet `- <name>: <desc>` (a
// bullet without a colon is a name with an empty Desc).
type Artifact struct {
	Name string
	Desc string
}

// Artifacts is a parsed ARTIFACTS.md: the verbatim document plus the structured
// list of artifacts read from its top-level bullets. The body is preserved
// line-for-line so String round-trips a representative file.
type Artifacts struct {
	Items   []Artifact
	body    []string
	present bool
}

// bulletRe matches a top-level markdown list item (`-` or `*` at column 0),
// capturing the item text. An indented bullet is a sub-point of an item, not its
// own artifact, so only column-0 bullets are taken.
var bulletRe = regexp.MustCompile(`^[-*]\s+(.+)$`)

// ParseArtifacts parses ARTIFACTS.md source. Each top-level bullet becomes an
// Artifact: a `name: description` bullet splits on the first colon, otherwise the
// whole item is the name. Non-bullet lines (headings, prose) and indented
// sub-bullets are kept verbatim for round-trip but are not artifacts. Never fails.
func ParseArtifacts(s string) Artifacts {
	a := Artifacts{present: true}
	a.body = scanLines(s)
	for _, line := range a.body {
		m := bulletRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		item := strings.TrimSpace(m[1])
		art := Artifact{Name: item}
		if name, desc, ok := strings.Cut(item, ":"); ok {
			art.Name = strings.TrimSpace(name)
			art.Desc = strings.TrimSpace(desc)
		}
		a.Items = append(a.Items, art)
	}
	return a
}

// LoadArtifacts reads and parses ARTIFACTS.md at path. A missing file returns a
// zero (absent) model with no items, not an error.
func LoadArtifacts(path string) (Artifacts, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Artifacts{}, nil
		}
		return Artifacts{}, err
	}
	return ParseArtifacts(string(b)), nil
}

// Present reports whether the model came from an existing document (LoadArtifacts
// of a missing file returns Present()==false).
func (a Artifacts) Present() bool { return a.present }

// String serializes the document verbatim; ParseArtifacts(s).String() round-trips
// a newline-terminated file.
func (a Artifacts) String() string { return joinLines(a.body) }

// scanLines splits source into lines (newline stripped), the inverse of joinLines.
// A bufio.Scanner is used so a missing trailing newline is tolerated; the canonical
// (round-tripping) form is a newline-terminated document.
func scanLines(s string) []string {
	if s == "" {
		return nil
	}
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var out []string
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

// joinLines rejoins lines with newlines, ensuring a trailing newline for a
// non-empty body and "" for an empty one.
func joinLines(ls []string) string {
	if len(ls) == 0 {
		return ""
	}
	return strings.Join(ls, "\n") + "\n"
}

// splitCSV splits a comma list, trimming each field and dropping empties (matches
// the historical env.go behavior so extracted environment lists are unchanged).
func splitCSV(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}
