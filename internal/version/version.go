// Package version records the git commit the beehive/beehived/honeybee binaries
// were built from, stamped at BUILD time via -ldflags. It is the anchor for the
// prompt-embed drift guard: the prompts (HONEYBEE.md, AGENTS.md, …) and all code
// are go:embed'd/compiled into the binaries, so a change merged to main only
// reaches a live pass once an operator rebuilds and redeploys. Knowing which
// commit a running binary was built from lets the honeybee preflight compare it
// against the tracked-main tip and warn when the deployed binaries are stale.
package version

// SHA is the full git commit the binaries were built from. It is DELIBERATELY
// empty by default and is set only by the release/install build path via:
//
//	go build -ldflags "-X github.com/spencerharmon/beehive/internal/version.SHA=<sha>" ./cmd/<bin>
//
// An empty SHA is the honest "dev" signal for a plain `go build` (no stamp): the
// value is never guessed, so a running binary never reports a wrong commit. The
// drift guard treats an empty SHA as "cannot compare" and stays silent, so an
// unstamped build is simply inert rather than noisy or misleading.
var SHA = ""

// String is the human version line printed by `beehive version`: the stamped
// commit when present, else the honest "beehive dev" (no stamp available). It is
// never a fabricated SHA.
func String() string {
	if SHA == "" {
		return "beehive dev"
	}
	return "beehive " + SHA
}

// Build returns the stamped build commit and whether one is present. ok is false
// for an unstamped (dev) build, so callers (e.g. the drift guard) can distinguish
// "no build SHA to compare" from a real commit without string-matching "dev".
func Build() (sha string, ok bool) {
	return SHA, SHA != ""
}
