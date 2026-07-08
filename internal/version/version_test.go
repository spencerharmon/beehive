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

// A stamped build (as the -ldflags path sets it) surfaces the exact commit in
// both the human line and Build().
func TestStamped(t *testing.T) {
	orig := SHA
	defer func() { SHA = orig }()

	SHA = "0123456789abcdef0123456789abcdef01234567"
	if got, want := String(), "beehive "+SHA; got != want {
		t.Fatalf("String() stamped = %q, want %q", got, want)
	}
	if sha, ok := Build(); !ok || sha != SHA {
		t.Fatalf("Build() stamped = (%q,%v), want (%q,true)", sha, ok, SHA)
	}
}
