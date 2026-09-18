package version

import "testing"

// The default build is unstamped: String() must be the honest "beehive dev" and
// Build() must report ok=false, never a fabricated SHA.
func TestUnstamped(t *testing.T) {
	orig := SHA
	defer func() { SHA = orig }()

	SHA = ""
	if got := String(); got != "beehive dev" {
		t.Fatalf("String() unstamped = %q, want %q", got, "beehive dev")
	}
	if sha, ok := Build(); ok || sha != "" {
		t.Fatalf("Build() unstamped = (%q,%v), want (\"\",false)", sha, ok)
	}
}

// A SHA-only stamped build (an install-path build that stamps the commit but no
// release tag) surfaces the exact commit in both the human line and Build(), and
// reports no release.
func TestStamped(t *testing.T) {
	origSHA, origVer := SHA, Version
	defer func() { SHA, Version = origSHA, origVer }()

	Version = ""
	SHA = "0123456789abcdef0123456789abcdef01234567"
	if got, want := String(), "beehive "+SHA; got != want {
		t.Fatalf("String() stamped = %q, want %q", got, want)
	}
	if sha, ok := Build(); !ok || sha != SHA {
		t.Fatalf("Build() stamped = (%q,%v), want (%q,true)", sha, ok, SHA)
	}
	if ver, ok := Release(); ok || ver != "" {
		t.Fatalf("Release() SHA-only = (%q,%v), want (\"\",false)", ver, ok)
	}
}

// A fully-stamped release build (the release/deploy path: version tag + commit)
// prints the precise "<release> (<short-commit>)" line and reports both.
func TestStampedRelease(t *testing.T) {
	origSHA, origVer := SHA, Version
	defer func() { SHA, Version = origSHA, origVer }()

	Version = "v1.4.2"
	SHA = "0123456789abcdef0123456789abcdef01234567"
	if got, want := String(), "beehive v1.4.2 (0123456789ab)"; got != want {
		t.Fatalf("String() release = %q, want %q", got, want)
	}
	if ver, ok := Release(); !ok || ver != "v1.4.2" {
		t.Fatalf("Release() = (%q,%v), want (%q,true)", ver, ok, "v1.4.2")
	}
	if sha, ok := Build(); !ok || sha != SHA {
		t.Fatalf("Build() = (%q,%v), want (%q,true)", sha, ok, SHA)
	}
}

// A version-only stamp (defensive: release set, no commit) still prints the
// release rather than "dev".
func TestStampedVersionOnly(t *testing.T) {
	origSHA, origVer := SHA, Version
	defer func() { SHA, Version = origSHA, origVer }()

	Version = "v2.0.0"
	SHA = ""
	if got, want := String(), "beehive v2.0.0"; got != want {
		t.Fatalf("String() version-only = %q, want %q", got, want)
	}
}
