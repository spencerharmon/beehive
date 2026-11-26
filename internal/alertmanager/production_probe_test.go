package alertmanager

import (
	"bytes"
	"encoding/json"
	"math"
	"os/exec"
	"testing"
	"time"
)

// Alertmanager can return its local RFC3339 zone, not just UTC Z. Use
// different expiry zones in each snapshot to prove comparisons use instants.
func TestProductionRefreshProbeTimezones(t *testing.T) {
	for _, zone := range []struct {
		name   string
		offset int
	}{
		{"utc", 0},
		{"negative", -6 * 3600},
		{"positive-half-hour", 5*3600 + 30*60},
		{"negative-half-hour", -3*3600 - 30*60},
		{"positive-quarter-hour", 12*3600 + 45*60},
	} {
		for _, tc := range []struct {
			name    string
			start   time.Duration
			end     time.Duration
			advance time.Duration
			bad     bool
		}{
			{"refresh", -time.Minute, 3 * time.Minute, time.Minute, false},
			{"subsecond-refresh", -time.Minute, 3 * time.Minute, 500 * time.Millisecond, false},
			{"expired", -time.Minute, -time.Second, time.Minute, true},
			{"future-start", time.Minute, 3 * time.Minute, time.Minute, true},
			{"stale", -time.Minute, 3 * time.Minute, 0, true},
			{"backwards", -time.Minute, 3 * time.Minute, -time.Second, true},
		} {
			t.Run(zone.name+"/"+tc.name, func(t *testing.T) {
				now := time.Now().UTC().Truncate(time.Second).Add(125 * time.Millisecond)
				local := time.FixedZone(zone.name, zone.offset)
				var input bytes.Buffer
				for _, expiry := range []time.Time{now.Add(tc.end).In(local), now.Add(tc.end + tc.advance)} {
					sample := []map[string]any{{
						"labels":   map[string]string{"service": "beehive", "instance": "production", "alertname": AlertNeedsHuman},
						"status":   map[string]string{"state": "active"},
						"startsAt": now.Add(tc.start).In(local).Format(time.RFC3339Nano),
						"endsAt":   expiry.Format(time.RFC3339Nano),
					}}
					if err := json.NewEncoder(&input).Encode(sample); err != nil {
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
					var got []struct{ AdvanceSeconds float64 }
					if err := json.Unmarshal(out, &got); err != nil || len(got) != 1 || math.Abs(got[0].AdvanceSeconds-tc.advance.Seconds()) > 0.000001 {
						t.Fatalf("wrong measurement: %s (%v)", out, err)
					}
				}
			})
		}
	}
}

func TestProductionRefreshProbe(t *testing.T) {
	type snapshot = []map[string]any
	for _, tc := range []struct {
		name string
		bad  bool
		edit func([]snapshot) []snapshot
	}{
		{"refresh", false, func(s []snapshot) []snapshot { return s }},
		{"offset-whole-seconds", false, func(s []snapshot) []snapshot {
			for _, sample := range s {
				for _, key := range []string{"startsAt", "endsAt"} {
					stamp, err := time.Parse(time.RFC3339Nano, sample[0][key].(string))
					if err != nil {
						t.Fatal(err)
					}
					sample[0][key] = stamp.In(time.FixedZone("offset", -6*3600)).Format(time.RFC3339)
				}
			}
			return s
		}},
		{"malformed-timestamp", true, func(s []snapshot) []snapshot { s[0][0]["endsAt"] = "not-a-timestamp"; return s }},
		{"invalid-offset", true, func(s []snapshot) []snapshot { s[0][0]["endsAt"] = "2099-01-01T00:00:00+25:99"; return s }},
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
				var got []struct{ AdvanceSeconds float64 }
				if err := json.Unmarshal(out, &got); err != nil || len(got) != 1 || math.Abs(got[0].AdvanceSeconds-60) > 0.000001 {
					t.Fatalf("wrong measurement: %s (%v)", out, err)
				}
			}
		})
	}
}
