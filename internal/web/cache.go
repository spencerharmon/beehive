package web

import (
	"sync"
	"time"

	"github.com/spencerharmon/beehive/internal/plan"
)

// planCache memoizes the file-read + structural parse of each submodule's
// PLAN.md, keyed by the beehive repo's HEAD commit. The frontend re-reads and
// re-parses PLAN.md on every dashboard/plan/human request; once a hive grows past
// a handful of submodules that repeated parse is the dominant per-request cost.
//
// Invalidation is by HEAD. The checked-out main is a pure projection of committed
// history (honeybees publish by pushing, the frontend commits its own writes), so
// the parsed STRUCTURE of any PLAN.md can change only when HEAD advances. Each
// request resolves HEAD once (a cheap `rev-parse`, shared across every submodule
// it renders) and passes it in; when it differs from the cached generation every
// entry is dropped and re-parsed. A cached plan therefore can never outlive the
// commit it was parsed from — including a honeybee's out-of-process merge to main,
// which advances HEAD just like the frontend's own commits.
//
// Only the wall-clock-INDEPENDENT structural parse (internal/plan.Parse) is
// cached. The claim projection (active/stale vs the TTL), which depends on the
// current time, is recomputed on every read — so a cached entry can never report
// an expired claim as still active between commits. Correctness over hit-rate, as
// the task's caveat requires; honeybees commit (and re-stamp heartbeats)
// frequently, so the structural cache still absorbs the bulk of repeated reads.
//
// # Supported-submodule ceiling
//
// The cache holds one parsed plan per submodule for the current HEAD in memory,
// behind a single mutex, and is rebuilt wholesale on every commit. It is sized
// for the low hundreds of submodules a single beehived realistically serves:
//   - Memory is O(submodules) parsed plans for ONE HEAD (bounded — the previous
//     generation is dropped, not accumulated).
//   - The one lock serializes parses, so a cold cache right after a commit
//     re-parses under contention (and a load is held under the lock, which also
//     collapses a thundering herd on the same path). Fine at this scale, a
//     bottleneck far beyond it.
//   - Invalidation is whole-cache on each commit and honeybees commit frequently,
//     so the steady-state hit rate falls as submodule count rises.
//
// Past that range, switch to per-submodule HEADs (a submodule's gitlink moves
// only when ITS plan/pointer changes, so an unrelated commit need not evict it)
// and/or sharded locks. This ceiling is also stated in docs/frontend-components.md
// and the change doc, per the task's "document the ceiling" requirement.
type planCache struct {
	// load is the structural read+parse (loadPlan in production). It is a field so
	// tests can substitute a counting/fake loader; production never swaps it.
	load func(path string) (*plan.Plan, error)

	mu      sync.Mutex
	head    string                // HEAD generation the cached entries were parsed at
	entries map[string]*plan.Plan // path -> structural parse for head
	loads   int                   // count of real load()s performed (cache-miss work); a test signal
}

// newPlanCache builds an empty cache backed by the production structural loader.
func newPlanCache() *planCache {
	return &planCache{load: loadPlan, entries: map[string]*plan.Plan{}}
}

// view returns path's projected view plan for the beehive HEAD `head`. The
// structural parse is served from cache when head matches the cached generation
// and path was already parsed; otherwise it is (re)parsed and cached. The
// active/stale projection is always recomputed against now/ttl, so claim
// freshness is never cached. An empty head (a repo with no commits yet, where
// HEAD cannot key the cache) is treated as uncacheable and parsed fresh every
// call, so the view still renders correctly before the first commit.
func (c *planCache) view(head, path string, now time.Time, ttl time.Duration) (Plan, error) {
	parsed, err := c.structural(head, path)
	if err != nil {
		return Plan{}, err
	}
	return projectPlan(parsed, now, ttl), nil
}

// structural returns path's parsed PLAN.md for head, from cache when possible. On
// a HEAD change it drops the whole previous generation before parsing (the
// invalidate-on-commit contract). A load error is returned and NOT cached, so a
// transiently bad read/parse is retried on the next request rather than memoized.
func (c *planCache) structural(head, path string) (*plan.Plan, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if head == "" {
		// HEAD unknown: cannot detect invalidation, so never cache — always fresh.
		return c.loadOnce(path)
	}
	if head != c.head {
		c.head = head
		c.entries = map[string]*plan.Plan{}
	}
	if p, ok := c.entries[path]; ok {
		return p, nil
	}
	p, err := c.loadOnce(path)
	if err != nil {
		return nil, err
	}
	c.entries[path] = p
	return p, nil
}

// loadOnce performs one real structural load and counts it. The caller holds mu;
// holding the lock across the parse collapses concurrent misses for the same path
// into a single load.
func (c *planCache) loadOnce(path string) (*plan.Plan, error) {
	p, err := c.load(path)
	if err != nil {
		return nil, err
	}
	c.loads++
	return p, nil
}
