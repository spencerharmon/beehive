package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spencerharmon/beehive/internal/alertmanager"
	"github.com/spencerharmon/beehive/internal/swarm"
)

// passHealth records pass/preflight/publish outcome evidence for the
// swarm-health alerts. Every method is best-effort: a failure to persist is
// surfaced as a stderr WARNING and never replaces or alters the outcome it was
// recording (the caller returns its original error unchanged).
type passHealth struct {
	rec  *alertmanager.Recorder
	warn io.Writer
}

func newHealth(ctx context.Context, root string) *passHealth {
	h := &passHealth{warn: os.Stderr}
	rec, err := alertmanager.NewRecorder(ctx, root)
	if err != nil {
		h.warnf("init", err)
		return h
	}
	h.rec = rec
	return h
}

func (h *passHealth) warnf(what string, err error) {
	if err != nil {
		fmt.Fprintf(h.warn, "honeybee: WARNING could not persist %s health evidence (alerting may miss it): %v\n", what, err)
	}
}

func (h *passHealth) preflight(session string, outcome error) {
	if h.rec == nil {
		return
	}
	h.warnf("preflight", h.rec.Preflight(session, outcome))
}

// wrapPublish records each publish attempt's outcome and returns it unchanged.
func (h *passHealth) wrapPublish(session string, fn func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		err := fn(ctx)
		if h.rec != nil {
			h.warnf("publish", h.rec.Publish(session, err))
		}
		return err
	}
}

// passOutcome classifies a finished Run: progress (completed), failure (error,
// timeout/turn cap/no progress) or neutral (lost claim race).
func passOutcome(res swarm.Result, err error) (record, progress bool, cause error) {
	switch {
	case err != nil:
		return true, false, err
	case res.Lost:
		return false, false, nil
	case res.Completed:
		return true, true, nil
	}
	msg := fmt.Sprintf("pass ended without progress after %d turn(s)", res.Turns)
	if res.GCMarked {
		msg += " (timed out / capped; claim left for GC)"
	}
	if res.Warning != "" {
		msg += ": " + res.Warning
	}
	return true, false, errors.New(msg)
}

func (h *passHealth) pass(submodule, session string, res swarm.Result, err error) {
	if h.rec == nil {
		return
	}
	record, progress, cause := passOutcome(res, err)
	if !record {
		return
	}
	h.warnf("pass", h.rec.Pass(submodule, session, progress, cause))
}
