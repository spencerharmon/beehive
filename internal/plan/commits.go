package plan

import (
	"fmt"
	"strings"
)

// The `commits=` tag on a PLAN.md task and the `<!-- Beehive-Commits: ... -->`
// header on its change doc are the SAME record from two sides: the submodule
// commit shas a terminal handoff produced. The runner's handoff gate refuses a
// flip unless the two agree, so the honeybee-facing CLI (`beehive task status`)
// and the runner (verify.go / autoBookkeep) must serialize and parse them
// identically. These helpers are that single source of truth; verify.go delegates
// to them so the format can never drift between producer and verifier.

// CommitsTagValue renders a sha list the way the `commits=` tag serializes it,
// or "none" for an empty set.
func CommitsTagValue(commits []string) string {
	if len(commits) == 0 {
		return "none"
	}
	return strings.Join(commits, ",")
}

// ParseDocCommits extracts the `<!-- Beehive-Commits: <sha>,<sha> | none -->`
// header from a change doc. Returns the sha list (empty for `none`) and whether
// a well-formed header was found at all.
func ParseDocCommits(doc string) ([]string, bool) {
	for _, line := range strings.Split(doc, "\n") {
		line = strings.TrimSpace(line)
		_, rest, ok := strings.Cut(line, "Beehive-Commits:")
		if !ok {
			continue
		}
		rest, _, ok = strings.Cut(rest, "-->")
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		if rest == "" || rest == "none" {
			return nil, true
		}
		var shas []string
		for _, s := range strings.Split(rest, ",") {
			if s = strings.TrimSpace(s); s != "" {
				shas = append(shas, s)
			}
		}
		return shas, true
	}
	return nil, false
}

// SetDocCommitsHeader rewrites (or inserts) doc's first-line
// `<!-- Beehive-Commits: ... -->` header to name exactly commits, leaving every
// other line untouched — the ONLY prose it ever writes, and only when the header
// is absent or disagrees with the commits it mirrors.
func SetDocCommitsHeader(doc string, commits []string) string {
	header := fmt.Sprintf("<!-- Beehive-Commits: %s -->", CommitsTagValue(commits))
	lines := strings.SplitN(doc, "\n", 2)
	first := strings.TrimSpace(lines[0])
	rest := ""
	if len(lines) > 1 {
		rest = lines[1]
	}
	if strings.Contains(first, "Beehive-Commits:") {
		if first == header {
			return doc
		}
		if rest == "" {
			return header
		}
		return header + "\n" + rest
	}
	if rest == "" && len(lines) == 1 {
		if strings.TrimSpace(doc) == "" {
			return header + "\n"
		}
		return header + "\n" + doc
	}
	return header + "\n" + doc
}

// SameCommitSet reports whether a and b hold the same set of commit shas
// (order-insensitive).
func SameCommitSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}
