package main

import (
	"testing"

	"github.com/spencerharmon/beehive/internal/plan"
)

// TestHumanReason proves humanReason PRESERVES a --reason's embedded line
// structure (needs-human-standard-escalation-format): the standard action-first
// escalation template (summary, then Steps/Links/Technical detail each on their
// own markdown line) is authored with real newlines, so a multi-line --reason
// must survive as multiple lines — only each line's OWN internal whitespace
// (stray tabs/repeated spaces) is normalized — rather than the whole reason
// being collapsed into one unreadable run-on sentence.
func TestHumanReason(t *testing.T) {
	got, err := humanReason(" Need\noperator\tinput ", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "Need\noperator input" {
		t.Fatalf("reason = %q", got)
	}
	multi, err := humanReason("Summary line.\n1. Do the first step.\n2. Do the second step.\nLinks: https://example.com/dash", "")
	if err != nil {
		t.Fatal(err)
	}
	if multi != "Summary line.\n1. Do the first step.\n2. Do the second step.\nLinks: https://example.com/dash" {
		t.Fatalf("multi-line reason not preserved: %q", multi)
	}
	if _, err := humanReason("", ""); err == nil {
		t.Fatal("empty reason allowed")
	}
	if _, err := humanReason("x", "y"); err == nil {
		t.Fatal("reason and reason-file both allowed")
	}
}

func TestHumanCategory(t *testing.T) {
	for _, in := range []string{"secret", "external-permission", "contradiction", "architecture", " secret "} {
		c, err := humanCategory(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if !c.Valid() {
			t.Fatalf("%q -> invalid category %q", in, c)
		}
	}
	for _, in := range []string{"", "  ", "maintenance", "cache-clear", "SECRET"} {
		if _, err := humanCategory(in); err == nil {
			t.Fatalf("category %q accepted", in)
		}
	}
	if got, _ := humanCategory("contradiction"); got != plan.CatContradiction {
		t.Fatalf("category = %q, want %q", got, plan.CatContradiction)
	}
}

func TestTaskSubmoduleName(t *testing.T) {
	for in, want := range map[string]string{
		"alpha":            "alpha",
		"submodules/alpha": "alpha",
		"alpha/":           "alpha",
	} {
		got, err := taskSubmoduleName(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got != want {
			t.Fatalf("%s -> %q, want %q", in, got, want)
		}
	}
	for _, in := range []string{".", "..", "submodules", "../x", "alpha/beta"} {
		if _, err := taskSubmoduleName(in); err == nil {
			t.Fatalf("%s accepted", in)
		}
	}
}
