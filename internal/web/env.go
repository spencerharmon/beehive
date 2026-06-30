package web

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/spencerharmon/beehive/internal/artifacts"
)

// roiStamp matches the PLAN.md reconcile marker: <!-- Beehive-ROI: <sha> -->
var roiStamp = regexp.MustCompile(`Beehive-ROI:\s*([0-9a-f]+)`)

// Env is the deployment state shown by the env badge/panel. It mirrors
// artifacts.Deployment (the resolved blue/green state) for the templates, which
// reference .Env.Active and range over .Env.Envs.
type Env struct {
	Active string
	Envs   []string
}

// parseEnv reads INFRASTRUCTURE.md blue/green state through internal/artifacts
// (the typed model) instead of local regexes. A missing file or absent markers
// yields the defaults (blue active, {blue,green} available); a non-NotExist read
// error is surfaced alongside the defaults.
func parseEnv(path string) (Env, error) {
	in, err := artifacts.LoadInfra(path)
	d := in.Deployment()
	return Env{Active: d.Active, Envs: d.Envs}, err
}

// deploy switches the active environment in INFRASTRUCTURE.md through the typed
// model: it rewrites/inserts the Active: marker while preserving the rest of the
// document verbatim, then writes the round-tripped serialization. A missing file
// is created with just the marker.
func deploy(path, target string) error {
	in, err := artifacts.LoadInfra(path)
	if err != nil {
		return err
	}
	in.SetActive(target)
	return os.WriteFile(path, []byte(in.String()), 0o644)
}

// docFromMessage formats a commit's Beehive change-doc stamp ("<taskid> <path>")
// or "" when the message carries no stamp.
func docFromMessage(ctx context.Context, msg string) string {
	for _, l := range strings.Split(msg, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "Beehive:") {
			parts := strings.Fields(strings.TrimPrefix(l, "Beehive:"))
			if len(parts) == 2 {
				return fmt.Sprintf("%s %s", parts[0], parts[1])
			}
		}
	}
	return ""
}
