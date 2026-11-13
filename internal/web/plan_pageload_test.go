package web

import (
	"context"
	"testing"
)

// Rendering claims must not project the entire plan again for each claimed row.
func TestPlanViewDataSharesClaimProjection(t *testing.T) {
	s, _ := seedPerfServer(t, 120, 120)
	ctx := context.Background()
	if err := s.git.Commit(ctx, "seed claimed plan"); err != nil {
		t.Fatal(err)
	}
	sm, err := s.submodule("perf")
	if err != nil {
		t.Fatal(err)
	}
	before := s.cache.Lookups()
	p, err := s.planViewData(ctx, sm)
	if err != nil {
		t.Fatal(err)
	}
	// One plan parse-cache lookup and one change-doc lookup, independent of rows.
	if lookups := s.cache.Lookups() - before; lookups != 2 {
		t.Fatalf("plan view cache lookups = %d, want 2 (one projection per request)", lookups)
	}
	var running int
	for _, item := range p.Items {
		if item.Running {
			running++
			if item.SessionHref == "" {
				t.Fatalf("running task %s has no session link", item.ID)
			}
		}
	}
	if running != 40 {
		t.Fatalf("running tasks = %d, want 40 fresh claims", running)
	}
}
