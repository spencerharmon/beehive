package repo

import "testing"

func TestSessionStubRoundTrip(t *testing.T) {
	stub := SessionStub("bee-reconcile-123-456-session")
	branch, ok := ParseSessionStub(stub)
	if !ok {
		t.Fatalf("stub not recognized: %q", stub)
	}
	if branch != "bee-reconcile-123-456-session" {
		t.Fatalf("branch=%q", branch)
	}
}

func TestParseSessionStubRejectsTranscript(t *testing.T) {
	// A real transcript (no marker) must not be mistaken for a stub, so beehived
	// renders it directly instead of chasing a non-existent branch.
	transcript := "# session bee-T1-1\n\nsubmodule: sm\n\n## assistant\n\nhello world\n"
	if branch, ok := ParseSessionStub(transcript); ok {
		t.Fatalf("transcript misread as stub -> %q", branch)
	}
}
