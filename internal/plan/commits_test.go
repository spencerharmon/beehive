package plan

import "testing"

func TestCommitsTagValue(t *testing.T) {
	if got := CommitsTagValue(nil); got != "none" {
		t.Fatalf("empty -> %q, want none", got)
	}
	if got := CommitsTagValue([]string{"a", "b"}); got != "a,b" {
		t.Fatalf("two -> %q, want a,b", got)
	}
}

func TestParseAndSetDocCommitsHeader(t *testing.T) {
	// Insert into a doc lacking a header: preserves the body, header first.
	doc := "# title\nbody line.\n"
	out := SetDocCommitsHeader(doc, []string{"a", "b"})
	shas, ok := ParseDocCommits(out)
	if !ok || !SameCommitSet(shas, []string{"a", "b"}) {
		t.Fatalf("insert: ok=%v shas=%v\n%s", ok, shas, out)
	}
	if got := out; got[:len("<!-- Beehive-Commits: a,b -->")] != "<!-- Beehive-Commits: a,b -->" {
		t.Fatalf("header not first line:\n%s", out)
	}

	// Rewrite an existing header rather than stacking a second one.
	out2 := SetDocCommitsHeader(out, nil)
	shas2, ok2 := ParseDocCommits(out2)
	if !ok2 || len(shas2) != 0 {
		t.Fatalf("rewrite to none: ok=%v shas=%v\n%s", ok2, shas2, out2)
	}
	// Only ONE header line total.
	n := 0
	for len(out2) > 0 {
		i := indexOf(out2, "Beehive-Commits:")
		if i < 0 {
			break
		}
		n++
		out2 = out2[i+len("Beehive-Commits:"):]
	}
	if n != 1 {
		t.Fatalf("expected exactly one header, found %d", n)
	}

	// A doc with no header parses as absent.
	if _, ok := ParseDocCommits("# no header\n"); ok {
		t.Fatal("absent header parsed as present")
	}
}

func TestSameCommitSet(t *testing.T) {
	if !SameCommitSet([]string{"a", "b"}, []string{"b", "a"}) {
		t.Fatal("order-insensitive equality failed")
	}
	if SameCommitSet([]string{"a"}, []string{"a", "b"}) {
		t.Fatal("different lengths reported equal")
	}
	if SameCommitSet([]string{"a", "a"}, []string{"a", "b"}) {
		t.Fatal("multiset mismatch reported equal")
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
