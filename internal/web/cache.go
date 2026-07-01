package web

import (
	"context"
	"sync"
	"time"

	"github.com/spencerharmon/beehive/internal/artifacts"
	"github.com/spencerharmon/beehive/internal/plan"
)

// parseCache memoizes the file parses the read handlers otherwise repeat on every
// request — PLAN.md (internal/plan), INFRASTRUCTURE.md and ARTIFACTS.md
// (internal/artifacts) — keyed by the beehive repo HEAD commit.
//
// Invalidation is conservative BY DESIGN. The hive's checked-out main is a pure
// projection of committed history: honeybees publish by pushing and operator
// edits route through the frontend's own commit path, so nothing mutates a
// tracked file without a commit. Every such change therefore moves HEAD, and a
// HEAD change is a correct SUPERSET of "some cached view is stale" — on any move
// the whole cache is dropped and the next read reparses from disk. Within one
// HEAD (e.g. a burst of HTMX poll refreshes with no intervening commit) repeated
// reads reuse the parse instead of re-reading and re-parsing every file. Because
// honeybees commit frequently the win is bounded to those inter-commit windows;
// that is the intended trade — correctness over hit-rate, exactly as the task
// requires.
//
// Only the HEAD-invariant PARSE is cached. Time-dependent projections (a task's
// claim active/stale vs the TTL, which depend on time.Now()) are applied per
// request on top of the cached parse (see Server.planView), so they never freeze
// at a stale value even when HEAD does not move for a while.
//
// # Supported-submodule ceiling
//
// On a HEAD change the next request rebuilds every submodule's parse under one
// mutex (O(submodules) small file reads), and the cache holds O(submodules ×
// view-kinds) entries in memory. This comfortably serves a single beehive host's
// realistic fleet — on the order of 100 submodules — where each parse is a few KB
// and commits arrive seconds (not milliseconds) apart. Well beyond that (many
// hundreds/thousands of submodules AND a very high commit rate) the
// whole-cache-drop-per-commit amortizes poorly: an unrelated submodule's commit
// still evicts every entry. The scaling path is per-file invalidation (key on
// path + blob sha rather than one repo-HEAD key), intentionally DEFERRED here in
// favor of the simplest correct design at the documented ceiling. See the change
// doc and README for the operator-facing statement of this limit.
type parseCache struct {
	mu     sync.Mutex
	head   string                 // HEAD the entries were parsed at ("" = nothing cached)
	ent    map[string]interface{} // namespaced key -> parsed value (an immutable snapshot)
	builds int                    // real parses run (cache misses); a metric + test seam
}

// newParseCache returns an empty cache.
func newParseCache() *parseCache {
	return &parseCache{ent: map[string]interface{}{}}
}

// get returns the memoized value for key at head, building (and counting) it on a
// miss. A HEAD change drops the whole cache first, so a stale entry is never
// served past a commit. Build errors are NOT cached — a transient read/parse
// failure must not poison later reads. head=="" (no resolvable HEAD, e.g. a
// brand-new repo with no commit) bypasses the cache entirely and always builds
// fresh, so a not-yet-committed tree is never served stale. The build closure
// runs under the lock, which serializes (and de-duplicates) concurrent misses;
// at the documented submodule ceiling the parses are cheap enough that this is
// simpler and safe.
func (c *parseCache) get(head, key string, build func() (interface{}, error)) (interface{}, error) {
	if head == "" {
		return build()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if head != c.head {
		c.ent = map[string]interface{}{}
		c.head = head
	}
	if v, ok := c.ent[key]; ok {
		return v, nil
	}
	v, err := build()
	if err != nil {
		return nil, err
	}
	c.builds++
	c.ent[key] = v
	return v, nil
}

// head resolves the beehive repo's HEAD commit as the cache key, or "" when it
// cannot be resolved (a repo with no commits yet) so the caller bypasses the
// cache and reads fresh. The full SHA (not the short form) avoids any
// cross-history short-sha aliasing. It is one cheap `rev-parse HEAD` per request;
// handlers resolve it ONCE and thread it into every cached read so a dashboard
// over N submodules pays a single rev-parse, not one per file.
func (s *Server) head(ctx context.Context) string {
	h, err := s.git.RevParse(ctx, "HEAD")
	if err != nil {
		return ""
	}
	return h
}

// planView returns the parsed+projected PLAN.md for the views, serving the
// read+parse from the HEAD-keyed cache and applying the now/ttl claim projection
// per call. It is the cached equivalent of parsePlan (the uncached reference);
// both agree by construction (they share loadPlanFile + projectPlan).
func (s *Server) planView(head, path string, now time.Time, ttl time.Duration) (Plan, error) {
	v, err := s.cache.get(head, "plan:"+path, func() (interface{}, error) {
		return loadPlanFile(path)
	})
	if err != nil {
		return Plan{}, err
	}
	return projectPlan(v.(*plan.Plan), now, ttl), nil
}

// infraView returns the parsed INFRASTRUCTURE.md (typed artifacts model) from the
// HEAD-keyed cache. The dashboard env badge, the env panel (via envView), and the
// explorer all share this one cached parse per submodule. A missing file caches a
// not-present model (Present()==false), so an absent doc is a cache hit too.
func (s *Server) infraView(head, path string) (artifacts.Infra, error) {
	v, err := s.cache.get(head, "infra:"+path, func() (interface{}, error) {
		return artifacts.LoadInfra(path)
	})
	if err != nil {
		return artifacts.Infra{}, err
	}
	return v.(artifacts.Infra), nil
}

// artifactsView returns the parsed ARTIFACTS.md (typed model) from the HEAD-keyed
// cache, for the explorer.
func (s *Server) artifactsView(head, path string) (artifacts.Artifacts, error) {
	v, err := s.cache.get(head, "artifacts:"+path, func() (interface{}, error) {
		return artifacts.LoadArtifacts(path)
	})
	if err != nil {
		return artifacts.Artifacts{}, err
	}
	return v.(artifacts.Artifacts), nil
}

// envView resolves the blue/green deployment view from the cached
// INFRASTRUCTURE.md parse (infraView), so the env badge and the env panel reuse
// the same memoized Infra rather than each re-reading the file. It mirrors the
// uncached parseEnv reference exactly (Deployment() supplies the blue/green
// defaults when the markers are absent, and a read error still returns the
// defaults alongside the error, which callers ignore).
func (s *Server) envView(head, path string) (Env, error) {
	in, err := s.infraView(head, path)
	d := in.Deployment()
	return Env{Active: d.Active, Envs: d.Envs}, err
}
