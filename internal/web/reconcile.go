package web

import (
	"context"
	"log"
	"time"
)

// defaultReconcileInterval is how often beehived's authoritative background
// reconcile checks for — and heals — a local/remote main divergence that the
// viewer's ff-only pullMain cannot cross. Fork healing is not latency-sensitive
// (honeybee passes converge on origin/main via their own worktrees; this only
// reconciles the primary-main projection beehived owns), so a calm cadence avoids
// needless git churn while still recovering a fork within seconds.
const defaultReconcileInterval = 30 * time.Second

// StartMainReconcilers launches one background main-fork reconciler per served
// repo. It is the deterministic recovery for a forked primary main: the viewer's
// pullMain is ff-only BY DESIGN and cannot cross a fork, so without this a
// crash-window fork (a direct-on-primary commit stranded on local main before its
// push) — or any external divergence — freezes local main permanently, exactly
// the failure this defense closes. Model-checked in specs/MainConvergeCrash.tla
// (heal_fixed). Call once at daemon startup; each reconciler runs until ctx done.
func (s *Server) StartMainReconcilers(ctx context.Context) {
	for _, srv := range s.targets() {
		srv.startMainReconcile(ctx)
	}
}

func (s *Server) reconcileInterval() time.Duration {
	if s.reconcileIvl > 0 {
		return s.reconcileIvl
	}
	return defaultReconcileInterval
}

func (s *Server) startMainReconcile(ctx context.Context) {
	go func() {
		// Heal any fork a prior beehived/pass left frozen at boot, before the first
		// tick, so recovery does not wait a full interval on startup.
		s.reconcileMainOnce(ctx)
		t := time.NewTicker(s.reconcileInterval())
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.reconcileMainOnce(ctx)
			}
		}
	}()
}

// reconcileMainOnce heals a local/remote main divergence if one exists. It runs
// under gitMu (shared with the viewer's ff-only pullMain and the frontend's own
// commit/publish) so it never races the primary index. A heal that hits a merge
// conflict — a genuinely divergent fork whose two lines edited the same content —
// is logged LOUDLY and recorded in reconcileErr (never silently swallowed), so an
// unresolvable fork is visible and awaiting resolution rather than frozen unseen.
func (s *Server) reconcileMainOnce(ctx context.Context) {
	remote, err := s.git.Remote(ctx)
	if err != nil || remote == "" {
		return // local-only hive: local main is the sole authority, nothing to reconcile
	}
	s.gitMu.Lock()
	healed, herr := s.git.ReconcileMainFork(ctx, remote)
	s.gitMu.Unlock()

	s.syncMu.Lock()
	s.reconcileErr = herr
	s.syncMu.Unlock()

	name := s.name
	if name == "" {
		name = "default"
	}
	if herr != nil {
		log.Printf("beehived: main reconcile (%s): divergence could not be healed, awaiting resolution: %v", name, herr)
		return
	}
	if healed {
		log.Printf("beehived: main reconcile (%s): healed a local/remote main divergence and republished the union", name)
	}
}
