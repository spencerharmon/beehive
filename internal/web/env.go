package web

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// roiStamp matches the PLAN.md reconcile marker: <!-- Beehive-ROI: <sha> -->
var roiStamp = regexp.MustCompile(`Beehive-ROI:\s*([0-9a-f]+)`)

// activeRe and envsRe derive blue/green env state from INFRASTRUCTURE.md lines:
//
//	Active: blue
//	Environments: blue, green
var (
	activeRe = regexp.MustCompile(`(?mi)^Active:\s*(\S+)`)
	envsRe   = regexp.MustCompile(`(?mi)^Environments:\s*(.+)$`)
)

// Env is the parsed deployment state.
type Env struct {
	Active string
	Envs   []string
}

// parseEnv reads INFRASTRUCTURE.md for blue/green state. Defaults: blue active,
// {blue,green} available, when the file omits the markers.
func parseEnv(path string) (Env, error) {
	e := Env{Active: "blue", Envs: []string{"blue", "green"}}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return e, nil
		}
		return e, err
	}
	if m := activeRe.FindSubmatch(b); m != nil {
		e.Active = string(m[1])
	}
	if m := envsRe.FindSubmatch(b); m != nil {
		e.Envs = splitCSV(string(m[1]))
	}
	return e, nil
}

// deploy switches Active to target, rewriting/inserting the Active: line.
func deploy(path, target string) error {
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var lines []string
	found := false
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		l := sc.Text()
		if activeRe.MatchString(l) {
			lines = append(lines, "Active: "+target)
			found = true
			continue
		}
		lines = append(lines, l)
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if !found {
		lines = append(lines, "Active: "+target)
	}
	out := strings.Join(lines, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return os.WriteFile(path, []byte(out), 0o644)
}

// shortStamp formats a commit's Beehive change-doc stamp link or "".
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
