package alertmanager

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"testing"
	"time"
)

func TestProductionRefreshProbe(t *testing.T) {
	type snapshot = []map[string]any
	for _, tc := range []struct {
		name string
		bad  bool
		edit func([]snapshot) []snapshot
	}{
		{"refresh", false, func(s []snapshot) []snapshot { return s }},
		{"empty", true, func(s []snapshot) []snapshot { return []snapshot{{}, {}} }},
		{"one-response", true, func(s []snapshot) []snapshot { return s[:1] }},
		{"stale", true, func(s []snapshot) []snapshot { s[1][0]["endsAt"] = s[0][0]["endsAt"]; return s }},
		{"new-start", true, func(s []snapshot) []snapshot {
			s[1][0]["startsAt"] = time.Now().Add(-time.Second).UTC().Format(time.RFC3339)
			return s
		}},
		{"expired", true, func(s []snapshot) []snapshot { s[0][0]["endsAt"] = "2020-01-01T00:00:00Z"; return s }},
		{"wrong-instance", true, func(s []snapshot) []snapshot { s[1][0]["labels"].(map[string]string)["instance"] = "fixture"; return s }},
		{"duplicate", true, func(s []snapshot) []snapshot { s[1] = append(s[1], s[1][0]); return s }},
		{"suppressed", true, func(s []snapshot) []snapshot { s[1][0]["status"] = map[string]string{"state": "suppressed"}; return s }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now().UTC()
			var samples []snapshot
			for _, expiry := range []time.Time{now.Add(3 * time.Minute), now.Add(4 * time.Minute)} {
				samples = append(samples, snapshot{{
					"labels":   map[string]string{"service": "beehive", "instance": "production", "alertname": AlertNeedsHuman},
					"status":   map[string]string{"state": "active"},
					"startsAt": now.Add(-time.Minute).Format(time.RFC3339Nano),
					"endsAt":   expiry.Format(time.RFC3339Nano),
				}})
			}
			var input bytes.Buffer
			for _, s := range tc.edit(samples) {
				if err := json.NewEncoder(&input).Encode(s); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("jq", "-e", "-s", "--arg", "instance", "production", "-f", "../../tests/alertmanager-production-refresh.jq")
			cmd.Stdin = &input
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.bad {
				t.Fatalf("probe err=%v want failure=%v: %s", err, tc.bad, out)
			}
			if !tc.bad {
				var got []struct{ AdvanceSeconds int }
				if err := json.Unmarshal(out, &got); err != nil || len(got) != 1 || got[0].AdvanceSeconds != 60 {
					t.Fatalf("wrong measurement: %s (%v)", out, err)
				}
			}
		})
	}
}
