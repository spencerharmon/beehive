package alertmanager

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"

	"github.com/spencerharmon/beehive/internal/config"
)

// Start runs ONE background emitter per registered hive. It re-reads committed
// global config on each poll, including while disabled. Neither config/git reads
// nor HTTP delivery run on the caller's (frontend/pass) path. The returned channel
// closes on shutdown; no signal, timer unit, or second daemon is needed to reload.
func Start(ctx context.Context, root, instance, generatorBase string) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		r := &reloadingNotifier{root: root, instance: instance, generator: generatorBase, logf: log.Printf}
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			r.tick(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return done
}

// Only the background loop touches this state; notifier replacement and Tick
// are serialized. Unrelated config commits must NOT reset startsAt or backoff.
// Bad config retains the last good setting and logs once per distinct error.
// Disabling stops sending (old alerts expire naturally), not a claim that the
// repository's genuine conditions have resolved.
type reloadingNotifier struct {
	root, instance, generator string
	currentURL                string
	initialized               bool
	lastError                 string
	n                         *Notifier
	logf                      func(string, ...any)
}

func (r *reloadingNotifier) reload(ctx context.Context) error {
	cfg, rev, err := config.ResolveCommittedGlobal(ctx, r.root)
	if err != nil {
		return err
	}
	base := strings.TrimSpace(cfg.AlertmanagerURL)
	if base != "" {
		u, err := url.Parse(base)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("invalid alertmanager_url at %s: expected an absolute HTTP(S) base URL without query/fragment", rev)
		}
		base = strings.TrimRight(base, "/")
	}
	if r.initialized && base == r.currentURL {
		return nil
	}
	if base != "" {
		if r.n == nil {
			r.n, err = NewHiveNotifier(ctx, base, r.root, r.instance, r.generator)
			if err != nil {
				return err
			}
		} else {
			// Preserve the sent set (and startsAt) while resetting transport
			// backoff for the newly configured receiver. No parallel Run loop.
			r.n.Poster = NewClient(base, DefaultTimeout)
			r.n.dirty = true
			r.n.failures = 0
			r.n.retryAt = time.Time{}
		}
		u, _ := url.Parse(base) // validated above; redact any password in logs
		r.logf("alertmanager: enabled for %s -> %s/api/v2/alerts (config revision %s)", r.instance, u.Redacted(), rev)
	} else {
		r.logf("alertmanager: disabled for %s (alertmanager_url unset; config revision %s)", r.instance, rev)
	}
	r.currentURL, r.initialized = base, true
	return nil
}

func (r *reloadingNotifier) tick(ctx context.Context) {
	// Bound config git reads independently of receiver latency.
	cctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	err := r.reload(cctx)
	cancel()
	if err != nil {
		if msg := err.Error(); msg != r.lastError {
			r.logf("alertmanager: reload config for %s (retaining last good setting): %v", r.instance, err)
			r.lastError = msg
		}
	} else {
		r.lastError = ""
	}
	if ctx.Err() == nil && r.currentURL != "" && r.n != nil {
		// Tick logs/retries delivery errors with its existing bounded backoff.
		_ = r.n.Tick(ctx)
	}
}
