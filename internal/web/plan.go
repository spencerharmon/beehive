package web

import (
	"bufio"
	"os"
	"strings"
)

// Known task states from the internal/plan state machine.
const (
	StatusTODO        = "TODO"
	StatusInProgress  = "IN-PROGRESS"
	StatusNeedsReview = "NEEDS-REVIEW"
	StatusDone        = "DONE"
	StatusArbitration = "NEEDS-ARBITRATION"
	StatusHuman       = "NEEDS-HUMAN"
)

var knownStatuses = []string{
	StatusInProgress, StatusNeedsReview, StatusArbitration, StatusHuman,
	StatusTODO, StatusDone,
}

// PlanItem is one parsed task row from a submodule PLAN.md.
type PlanItem struct {
	ID        string
	Status    string
	Desc      string
	Deps      []string
	Heartbeat string // in-progress TTL stamp, "" if none
	Doc       string // linked change-doc path, "" if none
}

// Plan is the parsed PLAN.md for one submodule.
type Plan struct {
	ROIStamp string
	Items    []PlanItem
}

// parsePlan reads and parses PLAN.md tasks. Missing file => empty plan.
// Each task is a markdown bullet:
//
//   - TODO <id> <desc> [deps: a,b] [hb: <ts>] [doc: <path>]
//
// Status is the first known-status token; id is the next token; tags are
// optional and order-independent. Lines without a status are ignored.
func parsePlan(path string) (Plan, error) {
	var p Plan
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return p, nil
		}
		return p, err
	}
	if m := roiStamp.FindSubmatch(b); m != nil {
		p.ROIStamp = string(m[1])
	}
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "-") {
			continue
		}
		body := strings.TrimSpace(strings.TrimPrefix(line, "-"))
		fields := strings.Fields(body)
		if len(fields) < 2 {
			continue
		}
		status := ""
		for _, ks := range knownStatuses {
			if fields[0] == ks {
				status = ks
				break
			}
		}
		if status == "" {
			continue
		}
		it := PlanItem{ID: fields[1], Status: status}
		var desc []string
		for _, tok := range fields[2:] {
			switch {
			case strings.HasPrefix(tok, "deps:"):
				it.Deps = splitCSV(strings.TrimPrefix(tok, "deps:"))
			case strings.HasPrefix(tok, "hb:"):
				it.Heartbeat = strings.TrimPrefix(tok, "hb:")
			case strings.HasPrefix(tok, "doc:"):
				it.Doc = strings.TrimPrefix(tok, "doc:")
			default:
				desc = append(desc, tok)
			}
		}
		it.Desc = strings.Join(desc, " ")
		p.Items = append(p.Items, it)
	}
	return p, sc.Err()
}

func splitCSV(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}
