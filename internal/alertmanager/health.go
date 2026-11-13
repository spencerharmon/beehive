package alertmanager

// Swarm-health alerts (BeehiveSwarmWedged, BeehivePassFailing).
//
// Evidence: short-lived honeybee processes RECORD pass/preflight/publish
// outcomes into a small repo-local state file in the hive's git COMMON dir
// (<git-common-dir>/beehive/health.json — shared by every worktree of the hive,
// never committed, never on main, so recording never churns history or races a
// publish). beehived's notifier READS that file as a Source; nothing here ever
// queries a host journal, an agent DB or Alertmanager. Recording performs no
// network I/O at all, so no notification result can change pass behavior.
//
// Thresholds / reset rules (deliberately small, see docs/alertmanager.md):
//   - BeehiveSwarmWedged (critical, one per instance) fires once EITHER
//     consecutive preflight failures (dirty-tree heal failure, ff-only pull /
//     fetch failure) OR consecutive non-conflict publish failures reach
//     WedgedThreshold (3). A successful preflight resets the preflight counter; a
//     successful publish resets BOTH (a landed publish proves preflight works).
//     An ordinary merge conflict is a peer race, not a wedge, and is not counted.
//   - BeehivePassFailing (warning, one per instance+submodule) fires once a
//     submodule's consecutive no-progress passes (error, wall-clock/turn-cap
//     timeout, or ended without completing) reach PassFailingThreshold (3). A
//     completed pass resets it. A lost claim race or "no workable task" is
//     neutral (neither progress nor failure).
// One failing pass never alerts. Counters, last error and timestamps go in
// annotations only; labels stay the stable identity.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spencerharmon/beehive/internal/fsatomic"
	"github.com/spencerharmon/beehive/internal/git"
)

const (
	AlertSwarmWedged = "BeehiveSwarmWedged"
	AlertPassFailing = "BeehivePassFailing"

	WedgedThreshold      = 3
	PassFailingThreshold = 3

	healthDir  = "beehive"
	healthFile = "health.json"
	maxErrLen  = 400
)

// Stage names a recorded outcome's origin.
type Stage string

const (
	StagePreflight Stage = "preflight"
	StagePublish   Stage = "publish"
	StagePass      Stage = "pass"
)

// Counter is one consecutive-failure counter.
type Counter struct {
	Consecutive int       `json:"consecutive"`
	FirstFailAt time.Time `json:"first_fail_at,omitempty"`
	LastFailAt  time.Time `json:"last_fail_at,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
	LastSession string    `json:"last_session,omitempty"`
	LastOKAt    time.Time `json:"last_ok_at,omitempty"`
}

func (c *Counter) fail(now time.Time, session string, err error) {
	if c.Consecutive == 0 {
		c.FirstFailAt = now
	}
	c.Consecutive++
	c.LastFailAt = now
	c.LastSession = session
	msg := "failed"
	if err != nil {
		msg = strings.Join(strings.Fields(err.Error()), " ")
	}
	if len(msg) > maxErrLen {
		msg = msg[:maxErrLen] + "…"
	}
	c.LastError = msg
}

func (c *Counter) ok(now time.Time) { *c = Counter{LastOKAt: now} }

// Health is the persisted evidence.
type Health struct {
	Preflight Counter            `json:"preflight"`
	Publish   Counter            `json:"publish"`
	Passes    map[string]Counter `json:"passes,omitempty"` // submodule -> counter
}

// HealthPath resolves the evidence file for the hive at root (any worktree).
func HealthPath(ctx context.Context, root string) (string, error) {
	out, err := git.New(root).Run(ctx, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	d := strings.TrimSpace(out)
	if !filepath.IsAbs(d) {
		d = filepath.Join(root, d)
	}
	return filepath.Join(d, healthDir, healthFile), nil
}

// LoadHealth reads the evidence; a missing file is an empty (healthy) state.
func LoadHealth(path string) (*Health, error) {
	h := &Health{Passes: map[string]Counter{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return h, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, h); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if h.Passes == nil {
		h.Passes = map[string]Counter{}
	}
	return h, nil
}

// Recorder persists outcomes for one hive. Zero Now = time.Now.
type Recorder struct {
	Path string
	Now  func() time.Time
}

// NewRecorder resolves the evidence path for root.
func NewRecorder(ctx context.Context, root string) (*Recorder, error) {
	p, err := HealthPath(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("resolve health evidence path: %w", err)
	}
	return &Recorder{Path: p}, nil
}

func (r *Recorder) update(fn func(h *Health, now time.Time)) error {
	if r == nil {
		return errors.New("health recorder unavailable")
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now()
	}
	if err := os.MkdirAll(filepath.Dir(r.Path), 0o755); err != nil {
		return err
	}
	lf, err := os.OpenFile(r.Path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
	h, err := LoadHealth(r.Path)
	if err != nil {
		// A corrupt evidence file must not wedge recording forever: start over,
		// but still surface it to the caller.
		h = &Health{Passes: map[string]Counter{}}
		fn(h, now)
		if werr := r.write(h); werr != nil {
			return errors.Join(err, werr)
		}
		return fmt.Errorf("reset corrupt health evidence: %w", err)
	}
	fn(h, now)
	return r.write(h)
}

func (r *Recorder) write(h *Health) error {
	b, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	return fsatomic.WriteFile(r.Path, append(b, '\n'), 0o644)
}

// Preflight records a preflight outcome (err nil = success).
func (r *Recorder) Preflight(session string, err error) error {
	return r.update(func(h *Health, now time.Time) {
		if err == nil {
			h.Preflight.ok(now)
		} else {
			h.Preflight.fail(now, session, err)
		}
	})
}

// Publish records a publish outcome. A merge conflict is a peer race and is
// not evidence of a wedge: it is ignored (neither resets nor counts).
func (r *Recorder) Publish(session string, err error) error {
	if err != nil && errors.Is(err, git.ErrConflict) {
		return nil
	}
	return r.update(func(h *Health, now time.Time) {
		if err == nil {
			h.Publish.ok(now)
			h.Preflight.ok(now)
		} else {
			h.Publish.fail(now, session, err)
		}
	})
}

// Pass records a per-submodule pass outcome: progress=true resets; otherwise
// cause (non-nil) describes the failure/timeout/no-progress.
func (r *Recorder) Pass(submodule, session string, progress bool, cause error) error {
	return r.update(func(h *Health, now time.Time) {
		c := h.Passes[submodule]
		if progress {
			c.ok(now)
		} else {
			c.fail(now, session, cause)
		}
		h.Passes[submodule] = c
	})
}

// RecordOrWarn runs a record call and reports a persistence failure on warn
// without ever replacing the caller's original outcome.
func RecordOrWarn(warn func(format string, args ...any), what string, err error) {
	if err != nil && warn != nil {
		warn("WARNING could not persist %s health evidence (alerting may miss it): %v", what, err)
	}
}

// HealthAlerts maps evidence to desired alerts.
func HealthAlerts(h *Health, instance, generatorBase string) []Desired {
	var out []Desired
	worst, stage := h.Preflight, StagePreflight
	if h.Publish.Consecutive >= WedgedThreshold && h.Publish.Consecutive >= h.Preflight.Consecutive {
		worst, stage = h.Publish, StagePublish
	}
	if worst.Consecutive >= WedgedThreshold {
		d := Desired{
			Labels: map[string]string{
				"alertname": AlertSwarmWedged, "service": "beehive",
				"severity": "critical", "instance": instance,
			},
			Annotations: map[string]string{
				"summary":     fmt.Sprintf("beehive %s swarm wedged: %d consecutive %s failures", instance, worst.Consecutive, stage),
				"stage":       string(stage),
				"consecutive": fmt.Sprint(worst.Consecutive),
				"since":       worst.FirstFailAt.UTC().Format(time.RFC3339),
				"last_error":  worst.LastError,
				"description": fmt.Sprintf("%d consecutive %s failures since %s (threshold %d); last: %s", worst.Consecutive, stage, worst.FirstFailAt.UTC().Format(time.RFC3339), WedgedThreshold, worst.LastError),
			},
		}
		if generatorBase != "" {
			d.GeneratorURL = strings.TrimRight(generatorBase, "/") + "/"
		}
		out = append(out, d)
	}
	subs := make([]string, 0, len(h.Passes))
	for s := range h.Passes {
		subs = append(subs, s)
	}
	sort.Strings(subs)
	for _, sm := range subs {
		c := h.Passes[sm]
		if c.Consecutive < PassFailingThreshold {
			continue
		}
		d := Desired{
			Labels: map[string]string{
				"alertname": AlertPassFailing, "service": "beehive",
				"severity": "warning", "instance": instance, "submodule": sm,
			},
			Annotations: map[string]string{
				"summary":      fmt.Sprintf("beehive %s:%s: %d consecutive passes without progress", instance, sm, c.Consecutive),
				"consecutive":  fmt.Sprint(c.Consecutive),
				"since":        c.FirstFailAt.UTC().Format(time.RFC3339),
				"last_error":   c.LastError,
				"last_session": c.LastSession,
				"description":  fmt.Sprintf("%d consecutive failed/timed-out/no-progress passes since %s (threshold %d); last: %s", c.Consecutive, c.FirstFailAt.UTC().Format(time.RFC3339), PassFailingThreshold, c.LastError),
			},
		}
		if generatorBase != "" {
			d.GeneratorURL = strings.TrimRight(generatorBase, "/") + "/submodule/" + sm + "/plan"
		}
		out = append(out, d)
	}
	return out
}

// HealthSource is the notifier Source over the hive's evidence file.
func HealthSource(root, instance, generatorBase string) Source {
	return func(ctx context.Context) ([]Desired, error) {
		p, err := HealthPath(ctx, root)
		if err != nil {
			return nil, err
		}
		h, err := LoadHealth(p)
		if err != nil {
			return nil, err
		}
		return HealthAlerts(h, instance, generatorBase), nil
	}
}

// MultiSource concatenates sources; any error fails the read (the notifier then
// keeps its last known state rather than resolving on a transient read error).
func MultiSource(srcs ...Source) Source {
	return func(ctx context.Context) ([]Desired, error) {
		var out []Desired
		for _, s := range srcs {
			d, err := s(ctx)
			if err != nil {
				return nil, err
			}
			out = append(out, d...)
		}
		return out, nil
	}
}
