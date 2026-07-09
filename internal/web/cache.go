package web

import (
	"crypto/sha256"
	"encoding/hex"
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
	miss    int            // loader invocations (cache misses); read via Misses()
	lookups int            // cache-participating reads (hit or miss); read via Lookups()/Hits()
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
// metric.
func (c *viewCache) Misses() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.miss
}

// Lookups reports how many cache-participating reads (cachedView calls with a
// non-empty head) have been requested over the cache's life, hit or miss — the
// rate's denominator: Hits()+Misses() == Lookups() always holds. The head==""
// bypass path (no HEAD to key on, e.g. a repo with no commits yet) is NOT
// counted here: it never touches gen/ents/miss at all, so it is neither a hit
// nor a miss of THIS cache. Exposed alongside Misses() as a cheap live metric
// (see hygiene.go's view-cache widget).
func (c *viewCache) Lookups() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lookups
}

// Hits reports cache-participating reads served from the memoized ents map
// without invoking the loader — the complement of Misses() within Lookups().
// Derived as Lookups-Misses (not its own incremented counter) so it can never
// drift out of sync with the other two.
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
// value it cannot key to a commit. Errors are not cached (a transient read must
// not be pinned for the generation). load runs under the lock, giving
// single-flight: concurrent misses on the same key parse once. Every call that
// reaches the lock (i.e. every non-bypassed call) counts one Lookups(), whether
// it then hits or misses.
func cachedView[T any](head string, c *viewCache, key string, load func() (T, error)) (T, error) {
	if head == "" {
		return load()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lookups++
	if head != c.gen {
		c.gen = head
		c.ents = map[string]any{}
	}
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

// fragmentETag returns a strong ETag validator (RFC 7232 §2.3) for a rendered
// fragment's exact bytes, for the conditional-GET fast path on polled htmx
// fragments (Server.renderConditional / writeConditional, web.go). It
// deliberately hashes the FINAL RENDERED OUTPUT rather than a coarser signal
// like the repo HEAD used by cachedView above: this cache's own doc (above)
// notes that some pane content is time-dependent — a claim's active/stale flip
// turns on the wall clock crossing the TTL with NO new commit — so a
// HEAD-only validator would wrongly keep 304-ing a pane whose rendered text
// actually changed. Hashing the bytes actually about to be sent means ANY
// observable change, commit-driven or purely time-driven, changes the digest,
// so a 304 only ever fires when the fragment is truly byte-identical to what
// the client already holds.
func fragmentETag(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}
