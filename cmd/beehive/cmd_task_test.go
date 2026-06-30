package main

import "testing"

func TestHumanReason(t *testing.T) {
	got, err := humanReason(" Need\noperator\tinput ", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "Need operator input" {
		t.Fatalf("reason = %q", got)
	}
	if _, err := humanReason("", ""); err == nil {
		t.Fatal("empty reason allowed")
	}
	if _, err := humanReason("x", "y"); err == nil {
		t.Fatal("reason and reason-file both allowed")
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
