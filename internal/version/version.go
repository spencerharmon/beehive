// Package version records the release version and git commit the
// beehive/beehived/honeybee binaries were built from, stamped at BUILD time via
// -ldflags. It is the anchor for two things: the operator/user-facing `--version`
// (and `beehive version`) line, and the prompt-embed drift guard — the prompts
// (HONEYBEE.md, AGENTS.md, …) and all code are go:embed'd/compiled into the
// binaries, so a change merged to main only reaches a live pass once an operator
// rebuilds and redeploys. Knowing which release + commit a running binary was
// built from lets the honeybee preflight compare it against the tracked-main tip
// and warn when the deployed binaries are stale.
package version

// Version is the release SEMVER the binary was cut from (e.g. "v1.4.2"), stamped
// at build time by the release/deploy path via:
//
//	go build -ldflags "-X github.com/spencerharmon/beehive/internal/version.Version=<tag>" ./cmd/<bin>
//
// It is DELIBERATELY empty by default: a plain `go build` with no stamp reports
// the honest "beehive dev" rather than guessing a version. The tag is the single
// source of truth (see LOCALS.md "Versioning discipline" and the release CI),
// never hand-edited here.
var Version = ""

// SHA is the full git commit the binaries were built from. Like Version it is
// DELIBERATELY empty by default and set only by the release/install build path:
//
//	go build -ldflags "-X github.com/spencerharmon/beehive/internal/version.SHA=<sha>" ./cmd/<bin>
//
// An empty SHA is the honest "dev" signal for a plain `go build` (no stamp): the
// value is never guessed, so a running binary never reports a wrong commit. The
// drift guard treats an empty SHA as "cannot compare" and stays silent, so an
// unstamped build is simply inert rather than noisy or misleading.
var SHA = ""

// shortSHA is the abbreviated commit for the human version line (12 hex chars, or
// the whole thing if shorter). It never fabricates: an empty SHA stays empty.
func shortSHA() string {
	if len(SHA) <= 12 {
		return SHA
	}
	return SHA[:12]
}

// String is the human version line printed by `beehive --version` / `beehive
// version`: the precise "<release> (<short-commit>)" when both are stamped, the
// release or the commit alone when only one is, else the honest "beehive dev".
// It is never a fabricated version or SHA — every field shown is a real stamp.
func String() string {
	switch {
	case Version != "" && SHA != "":
		return "beehive " + Version + " (" + shortSHA() + ")"
	case Version != "":
		return "beehive " + Version
	case SHA != "":
		return "beehive " + SHA
	default:
		return "beehive dev"
	}
}

// Release returns the stamped release semver and whether one is present. ok is
// false for a build with no version stamp (a dev build or a SHA-only install),
// so callers can distinguish "no release to show" from a real tag without
// string-matching.
func Release() (semver string, ok bool) {
	return Version, Version != ""
}

// Build returns the stamped build commit and whether one is present. ok is false
// for an unstamped (dev) build, so callers (e.g. the drift guard) can distinguish
// "no build SHA to compare" from a real commit without string-matching "dev".
func Build() (sha string, ok bool) {
	return SHA, SHA != ""
}
