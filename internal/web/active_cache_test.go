package web

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/repo"
)

func TestActiveHoneybeesCandidateCache(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	sm, err := s.submodule("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sm.SessionsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(id, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(sm.SessionsDir(), id+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	commit := func() {
		t.Helper()
		if err := s.git.Commit(ctx, "session fixtures"); err != nil {
			t.Fatal(err)
		}
	}
	branch := func(args ...string) {
		t.Helper()
		if _, err := s.git.Run(ctx, append([]string{"branch"}, args...)...); err != nil {
			t.Fatal(err)
		}
	}
	// The first stub shares a claim ID but not its branch; the second shares
	// only the branch. Neither may be omitted from the cached raw candidates.
	write("bee-1", repo.SessionStub("other-session"))
	write("transcript", repo.SessionStub("bee-1-session"))
	commit()
	head := s.headSHA(ctx)
	branch("other-session")
	branch("bee-1-session")
	now, ttl := time.Now(), time.Hour
	fresh := time.Date(2026, 6, 30, 11, 30, 0, 0, time.UTC)
	p, err := parsePlan(sm.PlanPath(), fresh, ttl)
	if err != nil {
		t.Fatal(err)
	}
	check := func(wantMisses int, want ...activeHoneybee) {
		t.Helper()
		got := s.activeHoneybeesLive(ctx, s.headSHA(ctx), sm, p, nil, now, ttl)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("active bees = %+v, want %+v", got, want)
		}
		if got := s.cache.Misses(); got != wantMisses {
			t.Fatalf("candidate classification loads = %d, want %d", got, wantMisses)
		}
	}
	claim := activeHoneybee{Session: "bee-1", TaskID: "t1", Branch: "bee-1-session"}
	first := activeHoneybee{Session: "bee-1", Branch: "other-session"}
	second := activeHoneybee{Session: "transcript", Branch: "bee-1-session"}
	check(1, claim)
	check(1, claim)
	// Expiring the claim must expose BOTH previously deduped candidates.
	p, err = parsePlan(sm.PlanPath(), fresh.Add(2*ttl), ttl)
	if err != nil {
		t.Fatal(err)
	}
	check(1, first, second)
	// Stream-tip expiry and ref deletion/creation do not advance HEAD.
	now = now.Add(2 * ttl)
	check(1)
	now = now.Add(-2 * ttl)
	branch("-D", "other-session")
	check(1, second)
	branch("other-session")
	check(1, first, second)
	if got := s.headSHA(ctx); got != head {
		t.Fatalf("liveness changes advanced HEAD: %s -> %s", head, got)
	}
	write("added", repo.SessionStub("other-session"))
	commit()
	check(2, activeHoneybee{Session: "added", Branch: "other-session"}, first, second)
	write("bee-1", "# Finished transcript\n")
	commit()
	check(3, activeHoneybee{Session: "added", Branch: "other-session"}, second)
}

func TestActiveHoneybeesCandidateCacheEmptyHEAD(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	sm, err := s.submodule("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if head := s.headSHA(ctx); head != "" {
		t.Fatalf("uncommitted fixture HEAD = %q", head)
	}
	if err := os.MkdirAll(sm.SessionsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	live := map[string]bool{"stream": true}
	for _, stub := range []bool{false, true, false} {
		body := "# Finished transcript\n"
		if stub {
			body = repo.SessionStub("stream")
		}
		if err := os.WriteFile(filepath.Join(sm.SessionsDir(), "bee.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		got := s.activeHoneybeesLive(ctx, "", sm, Plan{}, live, time.Now(), time.Hour)
		if (len(got) == 1) != stub || len(got) > 1 {
			t.Fatalf("stub=%v: active bees = %+v", stub, got)
		}
	}
	if got := s.cache.Lookups(); got != 0 {
		t.Fatalf("empty HEAD cache lookups = %d, want 0", got)
	}
}
