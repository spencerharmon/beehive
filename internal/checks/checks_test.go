package checks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `# Checks — demo

Approved DoD frameworks.

## go-test <!-- category=unit -->
Run the Go unit suite; exit 0 = all tests pass.
Match: (^|&&|\|\||;|\s)go\s+test\b
Example: go test ./... 

## go-build <!-- category=compile -->
Compile the static binaries.
Match: (^|&&|\|\||;|\s)go\s+build\b
Example: CGO_ENABLED=0 go build ./...

## flux-ready <!-- category=pipeline -->
Match: kubectl\s+.*\bwait\b|flux\s+get\b
`

func TestParseRoundTrip(t *testing.T) {
	c, err := Parse(sample)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(c.Stubs) != 3 {
		t.Fatalf("want 3 stubs, got %d", len(c.Stubs))
	}
	if c.Stubs[0].ID != "go-test" || c.Stubs[0].Category != "unit" {
		t.Fatalf("stub0 = %+v", c.Stubs[0])
	}
	if c.Stubs[0].Example != "go test ./..." {
		t.Fatalf("example trim: %q", c.Stubs[0].Example)
	}
	// Round-trip must re-parse to the same stub set.
	c2, err := Parse(c.String())
	if err != nil {
		t.Fatalf("reparse: %v\n---\n%s", err, c.String())
	}
	if len(c2.Stubs) != len(c.Stubs) {
		t.Fatalf("round-trip changed stub count %d -> %d", len(c.Stubs), len(c2.Stubs))
	}
	for i := range c.Stubs {
		if c.Stubs[i].ID != c2.Stubs[i].ID || c.Stubs[i].MatchRaw != c2.Stubs[i].MatchRaw {
			t.Fatalf("round-trip drift at %d: %+v vs %+v", i, c.Stubs[i], c2.Stubs[i])
		}
	}
}

func TestMatchAndUnapproved(t *testing.T) {
	c, err := Parse(sample)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		cmd      string
		approved bool
	}{
		{"go test ./...", true},
		{"cd repo && CGO_ENABLED=0 go test ./internal/...", true},
		{"CGO_ENABLED=0 go build ./cmd/...", true},
		{"flux get kustomizations -n flux-system", true},
		{"grep -q SomeSymbol repo/internal/x.go", false},
		{"test -f repo/specs/run.sh", false},
		{"", true}, // empty = nothing to constrain
	}
	for _, tc := range cases {
		reason := c.Unapproved(tc.cmd)
		if tc.approved && reason != "" {
			t.Errorf("cmd %q: expected approved, got %q", tc.cmd, reason)
		}
		if !tc.approved && reason == "" {
			t.Errorf("cmd %q: expected UNAPPROVED, got approved", tc.cmd)
		}
	}
}

func TestNilRegistryRejectsRealCheck(t *testing.T) {
	var c *Checks
	if r := c.Unapproved("go test ./..."); r == "" {
		t.Fatal("nil registry must reject a non-empty command")
	}
	if r := c.Unapproved(""); r != "" {
		t.Fatal("nil registry must accept an empty command (nothing to constrain)")
	}
}

func TestParseRejectsMissingCategory(t *testing.T) {
	_, err := Parse("## x\nMatch: go\\s+test\n")
	if err == nil || !strings.Contains(err.Error(), "category") {
		t.Fatalf("want missing-category error, got %v", err)
	}
}

func TestParseRejectsMissingMatch(t *testing.T) {
	_, err := Parse("## x <!-- category=unit -->\ndesc only\n")
	if err == nil || !strings.Contains(err.Error(), "Match") {
		t.Fatalf("want missing-Match error, got %v", err)
	}
}

func TestParseRejectsEmptyMatchingRegexp(t *testing.T) {
	_, err := Parse("## x <!-- category=unit -->\nMatch: .*\n")
	if err == nil || !strings.Contains(err.Error(), "empty string") {
		t.Fatalf("want empty-match rejection, got %v", err)
	}
}

func TestParseRejectsDuplicateID(t *testing.T) {
	doc := "## x <!-- category=unit -->\nMatch: a\n\n## x <!-- category=unit -->\nMatch: b\n"
	_, err := Parse(doc)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("want duplicate error, got %v", err)
	}
}

func TestParseRejectsInvalidRegexp(t *testing.T) {
	_, err := Parse("## x <!-- category=unit -->\nMatch: [unclosed\n")
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("want invalid-regexp error, got %v", err)
	}
}

func TestWarningsFlagUnknownCategory(t *testing.T) {
	c, err := Parse("## x <!-- category=frobnicate -->\nMatch: go\\s+test\n")
	if err != nil {
		t.Fatal(err)
	}
	w := c.Warnings()
	if len(w) != 1 || !strings.Contains(w[0], "frobnicate") {
		t.Fatalf("want unknown-category warning, got %v", w)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "CHECKS.md"))
	if !errors.Is(err, ErrNoChecksFile) {
		t.Fatalf("want ErrNoChecksFile, got %v", err)
	}
}

func TestLoadOK(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "CHECKS.md")
	if err := os.WriteFile(p, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(c.Stubs) != 3 {
		t.Fatalf("want 3 stubs, got %d", len(c.Stubs))
	}
}
