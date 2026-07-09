package web

import (
	"sync"
)

// viewCache is the frontend's parse-once cache. Every request otherwise
// re-reads and re-parses PLAN.md (and the other file-derived views) from disk;
// this memoizes the expensive work and only re-does it when the repo advances.
//
// Generation = the beehive repo HEAD commit, passed in by the caller (resolved
// ONCE per request — see Server.headSHA — and shared across every submodule read
// in that request, so a multi-submodule dashboard pays a single `rev-parse`, not
// one per submodule, and sees a coherent snapshot). The checked-out main is a
// pure projection of committed history — honeybees publish by committing/pushing
// (every claim, heartbeat, status flip, and merge is a commit; see
// internal/claim) and operator edits route through the commit path — so ANY
// change to a tracked file (PLAN.md, ROI.md, INFRASTRUCTURE.md, ...) advances
// HEAD. Keying on HEAD therefore subsumes the per-write-handler invalidation the
// design allows as an alternative: when HEAD moves the whole generation is
// dropped. Invalidation is deliberately coarse (whole-cache on any commit) —
// correctness over hit-rate — because honeybees commit frequently and a
// conservative wipe can never serve data from a different commit than the one it
// was parsed at.
//
// The cache memoizes only the TIME-INDEPENDENT work (the disk read + parse). It
// must NOT cache time-dependent derivations such as a claim's active/stale flip,
// which turns purely on the wall clock crossing the TTL with no new commit (a
// crashed owner stops committing, so HEAD stops advancing, yet the claim must
// still go stale). Callers cache the raw parse here and recompute those
// projections fresh each request against now/ttl.
//
// Supported-submodule ceiling: the cache holds one parsed view per (HEAD, key),
// so live memory is O(submodules) parsed plans for the current generation and a
// HEAD change re-parses every view touched since. It is sized for human-scale
// hives — up to a few hundred submodules with tens-of-KB PLAN.md files — where
// the parsed set fits comfortably in memory and a full re-parse on each commit
// is cheap relative to the request rate. Far beyond that (thousands of
// submodules, or a commit rate so high every read spans a fresh HEAD) the coarse
// whole-generation invalidation degrades toward the uncached cost; that is the
// documented ceiling, not a silent cliff (correctness is unaffected — only the
// hit-rate).
type viewCache struct {
	mu      sync.Mutex
	gen     string         // HEAD the cached entries were parsed at
	ents    map[string]any // key -> parsed value for the current generation
	lookups int            // cachedView calls that engaged the cache (hit or miss); read via Lookups()
	miss    int            // loader invocations (cache misses); read via Misses()
}

// newViewCache builds an empty cache. Generations are supplied per call by the
// caller (the beehive repo HEAD short SHA), not held here.
func newViewCache() *viewCache {
	return &viewCache{ents: map[string]any{}}
}

// Misses reports how many times a loader actually ran (cache misses) over the
// cache's life. Repeated reads within one HEAD generation add zero; a commit
// (a new head passed in) forces the next read to reload and increments it. Used
// by tests to prove parse-once + commit-invalidation, and usable as a cheap
// metric. Unchanged by the Lookups/Hits addition below — same counter, same
// increment site, same semantics existing tests assert on.
func (c *viewCache) Misses() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.miss
}

// Lookups reports how many cachedView calls actually engaged the cache (served
// from an existing entry OR triggered a loader run) over the cache's life. A
// bypassed call (empty head — see cachedView) never touches the cache at all, so
// it counts toward neither Lookups nor Misses; that keeps Hits (below) exactly
// equal to Lookups-Misses with no separate bookkeeping to drift out of sync.
// Lookups is the denominator a hit-rate needs; Misses (or Hits) is the numerator.
func (c *viewCache) Lookups() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lookups
}

// Hits reports how many lookups were served from the cache without invoking the
// loader (Lookups-Misses, read under one lock so the two counters can't be
// observed mid-update). Used, alongside Lookups and Misses, as the frontend's
// process-lifetime cache-health gauge (surfaced on /hygiene).
func (c *viewCache) Hits() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lookups - c.miss
}

// cachedView returns the memoized value for key in generation head, invoking
// load on a miss. When head differs from the cached generation the whole
// generation is dropped first, so a returned value is always the one parsed at
// the caller's head (never cross-generation): under the lock a lookup can only
// hit an entry stored under the current head, so concurrent requests at
// different heads never read each other's data (at worst they reparse — benign,
// self-healing thrash). An empty head (a repo with no commits yet, HEAD
// unresolvable) bypasses the cache and loads fresh — the frontend never serves a
// value it cannot key to a commit; this path also skips the Lookups/Hits/Misses
// counters (see Lookups) since it never touches the cache they describe. Errors
// are not cached (a transient read must not be pinned for the generation). load
// runs under the lock, giving single-flight: concurrent misses on the same key
// parse once.
func cachedView[T any](head string, c *viewCache, key string, load func() (T, error)) (T, error) {
	if head == "" {
		return load()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if head != c.gen {
		c.gen = head
		c.ents = map[string]any{}
	}
	c.lookups++
	if v, ok := c.ents[key]; ok {
		return v.(T), nil
	}
	v, err := load()
	c.miss++
	if err != nil {
		return v, err
	}
	c.ents[key] = v
	return v, nil
}
