package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
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
	async   map[string]*asyncEnt // key -> last value + gen for the stale-while-revalidate path
	ttlc    map[string]*ttlEnt   // key -> last value + expiry for the short-TTL memo (cachedTTL)
}

// asyncEnt is one stale-while-revalidate cache slot (cachedViewAsync): the LAST
// successfully-computed value, the HEAD generation it was computed at, and
// whether a background recompute for a newer generation is already running. It
// deliberately survives a generation change (unlike ents, which cachedView drops
// wholesale) so a request at a new HEAD is served the previous generation's value
// IMMEDIATELY while a single background goroutine recomputes — the value is
// coarse/expensive traceability, not correctness-critical, so serving it a
// commit or two stale for a moment is far better than blocking the whole page on
// a multi-second history walk.
type asyncEnt struct {
	val      any    // last computed value ("" of T until the first refresh completes)
	gen      string // HEAD the val was computed at
	inflight bool   // a background refresh is already running (single-flight)
}


// ttlEnt is one cachedTTL slot: the last computed value, its expiry, and a
// single-flight guard so only one background goroutine ever refreshes it.
type ttlEnt struct {
	val      any
	exp      time.Time
	inflight bool
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

// cachedViewAsync is the stale-while-revalidate variant of cachedView for a
// value that is EXPENSIVE and only SECONDARY (traceability, not correctness):
// it NEVER blocks the request on the loader. On the first ever call for a key
// (no stored value yet) it returns the zero value of T and the second return
// false ("not yet warm"), kicking off a single background recompute; every
// later call returns the LAST successfully-computed value immediately (true),
// and — if HEAD has advanced past the value's generation and no refresh is
// already running — launches one background goroutine to recompute for the new
// generation. The slot deliberately survives generation changes (unlike ents,
// which cachedView wipes wholesale), so a commit or two of staleness is traded
// for never paying the loader's cost on the request path.
//
// The background load runs on a DETACHED context (context.Background), NOT the
// request context: the request that triggered the refresh returns immediately,
// so its ctx is cancelled the moment it finishes — a refresh bound to it would
// be killed before it could store anything, and every request would re-trigger
// a doomed refresh. head=="" (no commit to key on) bypasses entirely and
// returns the zero value, false — the same no-HEAD bypass cachedView uses.
func cachedViewAsync[T any](head string, c *viewCache, key string, load func(context.Context) T) (T, bool) {
	var zero T
	if head == "" {
		return zero, false
	}
	c.mu.Lock()
	if c.async == nil {
		c.async = map[string]*asyncEnt{}
	}
	e := c.async[key]
	if e == nil {
		e = &asyncEnt{}
		c.async[key] = e
	}
	haveVal := e.gen != ""
	stale := e.gen != head
	if stale && !e.inflight {
		e.inflight = true
		go func() {
			v := load(context.Background())
			c.mu.Lock()
			e.val = v
			e.gen = head
			e.inflight = false
			c.mu.Unlock()
		}()
	}
	var val T
	if haveVal {
		val = e.val.(T)
	}
	c.mu.Unlock()
	return val, haveVal
}

// cachedTTL memoizes an EXPENSIVE, git-subprocess-backed value that is NOT
// HEAD-keyable — a whole-hive `git for-each-ref` liveness snapshot, a hygiene
// sweep — for a short wall-clock window, so a frequently-polled dashboard pays
// the subprocess cost at most once per ttl instead of on every render. Unlike
// cachedView (keyed on HEAD, dropped on any commit), these values turn on git
// state that changes with NO commit (a branch appears, a worktree is pruned),
// so HEAD is the wrong key; a brief TTL is exactly right because the value is a
// coarse operator-facing gauge, not a correctness input, and a second or two of
// staleness is invisible next to the claim TTL (minutes).
//
// The FIRST call for a key (no value yet) computes synchronously under the lock
// (single-flight, like cachedView) so the first render is real, not empty.
// Every later call returns the last value IMMEDIATELY and — once it is past its
// expiry and no refresh is already running — launches ONE background goroutine
// to recompute on a DETACHED context (context.Background, not the request ctx,
// which is cancelled the moment the triggering request returns). So after the
// first render no request ever blocks on the subprocess again.
func cachedTTL[T any](c *viewCache, key string, ttl time.Duration, load func(context.Context) T) T {
	c.mu.Lock()
	if c.ttlc == nil {
		c.ttlc = map[string]*ttlEnt{}
	}
	e := c.ttlc[key]
	if e != nil && e.val != nil {
		val := e.val.(T)
		if time.Now().After(e.exp) && !e.inflight {
			e.inflight = true
			go func() {
				v := load(context.Background())
				c.mu.Lock()
				e.val = v
				e.exp = time.Now().Add(ttl)
				e.inflight = false
				c.mu.Unlock()
			}()
		}
		c.mu.Unlock()
		return val
	}
	// No value yet: compute synchronously (holding the lock single-flights
	// concurrent cold misses on this key, exactly like cachedView's loader).
	if e == nil {
		e = &ttlEnt{}
		c.ttlc[key] = e
	}
	v := load(context.Background())
	e.val = v
	e.exp = time.Now().Add(ttl)
	c.mu.Unlock()
	return v
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
