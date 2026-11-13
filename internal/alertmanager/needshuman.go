package alertmanager

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
)

// AlertNeedsHuman is the alertname for a committed NEEDS-HUMAN task.
const AlertNeedsHuman = "BeehiveNeedsHuman"

// severityFor maps an escalation category to a stable severity: a blocker only
// the operator can clear out-of-band (secret, external permission) is critical;
// decisions (contradiction, architecture) and uncategorized escalations warn.
func severityFor(c plan.Category) string {
	switch c {
	case plan.CatSecret, plan.CatExternalPermission:
		return "critical"
	}
	return "warning"
}

// NeedsHumanSource reads COMMITTED PLAN.md files at ref (e.g. "HEAD") in the
// hive repo at root — never the working tree, never Alertmanager — and returns
// one Desired alert per NEEDS-HUMAN task. instance is the stable repo identity
// (the registry repo name); generatorBase, when set, is beehived's external base
// URL used to link each alert to its plan view.
func NeedsHumanSource(root, ref, instance, generatorBase string) Source {
	g := git.New(root)
	return func(ctx context.Context) ([]Desired, error) {
		ls, err := g.Run(ctx, "ls-tree", "--name-only", ref, "submodules/")
		if err != nil {
			return nil, err
		}
		var out []Desired
		for _, dir := range strings.Fields(ls) {
			sm := strings.TrimPrefix(dir, "submodules/")
			txt, err := g.Run(ctx, "show", ref+":"+dir+"/PLAN.md")
			if err != nil {
				continue // no committed plan for this submodule
			}
			p, err := plan.Parse(txt)
			if err != nil {
				return nil, fmt.Errorf("parse committed %s/PLAN.md: %w", dir, err)
			}
			out = append(out, NeedsHumanAlerts(p, sm, instance, generatorBase)...)
		}
		return out, nil
	}
}

// NeedsHumanAlerts maps one parsed plan to its desired NEEDS-HUMAN alerts.
func NeedsHumanAlerts(p *plan.Plan, submodule, instance, generatorBase string) []Desired {
	var out []Desired
	for _, t := range p.Tasks {
		if t.Status != plan.StatusHuman {
			continue
		}
		cat := string(t.HumanCategory)
		if cat == "" {
			cat = "uncategorized"
		}
		summary := firstLine(t.HumanReason())
		if summary == "" {
			summary = "task escalated NEEDS-HUMAN"
		}
		d := Desired{
			Labels: map[string]string{
				"alertname": AlertNeedsHuman,
				"service":   "beehive",
				"severity":  severityFor(t.HumanCategory),
				"instance":  instance,
				"submodule": submodule,
				"task":      t.ID,
				"category":  cat,
			},
			Annotations: map[string]string{
				"summary":            fmt.Sprintf("beehive %s:%s needs a human (%s)", submodule, t.ID, cat),
				"escalation_summary": summary,
				"description":        t.HumanReason(),
			},
		}
		if generatorBase != "" {
			d.GeneratorURL = strings.TrimRight(generatorBase, "/") + "/submodule/" + url.PathEscape(submodule) + "/plan#" + url.PathEscape(t.ID)
		}
		out = append(out, d)
	}
	return out
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// NewHiveNotifier builds the production notifier for one hive: all three alert
// families (NEEDS-HUMAN, swarm wedged, pass failing) through ONE emitter, with
// the sent-set persisted beside the health evidence. Used by beehived and the
// `beehive alertmanager emit` command alike.
func NewHiveNotifier(ctx context.Context, baseURL, root, instance, generatorBase string) (*Notifier, error) {
	hp, err := HealthPath(ctx, root)
	if err != nil {
		return nil, err
	}
	return &Notifier{
		Poster: NewClient(baseURL, DefaultTimeout),
		Source: MultiSource(
			NeedsHumanSource(root, "HEAD", instance, generatorBase),
			HealthSource(root, instance, generatorBase),
		),
		StatePath: filepath.Join(filepath.Dir(hp), "alertmanager-sent-"+url.PathEscape(instance)+".json"),
	}, nil
}

// Start launches the background notifier for one hive when baseURL is set and
// returns immediately; unset logs one line and does nothing. It never blocks the
// caller and never touches the honeybee pass path.
func Start(ctx context.Context, baseURL, root, instance, generatorBase string) *Notifier {
	if strings.TrimSpace(baseURL) == "" {
		log.Printf("alertmanager: disabled for %s (alertmanager_url unset)", instance)
		return nil
	}
	n, err := NewHiveNotifier(ctx, baseURL, root, instance, generatorBase)
	if err != nil {
		log.Printf("alertmanager: disabled for %s: %v", instance, err)
		return nil
	}
	log.Printf("alertmanager: enabled for %s -> %s/api/v2/alerts", instance, strings.TrimRight(baseURL, "/"))
	go n.Run(ctx, 15*time.Second)
	return n
}
