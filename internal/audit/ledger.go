package audit

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Ledger filenames under submodules/<sm>/docs/audit/. metrics.tsv is the
// per-session record (one row per audited session; its presence IS the
// audited-marker). trend.tsv is the per-pass tracked metric. Both are
// append-only and tab-separated for clean git diffs and exact round-trips.
const (
	MetricsFile = "metrics.tsv"
	TrendFile   = "trend.tsv"
)

var (
	// metricsHdr is the CURRENT metrics.tsv schema. Columns are ONLY EVER
	// APPENDED (never reordered or removed), keeping the ledger additive:
	// silent_loss is the newest, appended per the same backward-compatible
	// discipline audit-tool-abort-stall-guard used to grow the schema without
	// breaking already-ledgered rows. loadMetrics reads any legacy row that
	// predates a trailing column by defaulting the missing values (see
	// appendedMetricsCols / metricsDefaults).
	metricsHdr = []string{
		"pass", "session_id", "epoch", "submodule", "kind", "branch", "taskid",
		"bytes", "turns", "user_turns", "aborted", "lost_race", "completion_miss",
		"reconcile_loop", "abort_reason", "silent_loss",
	}
	// appendedMetricsCols are the columns added to metrics.tsv AFTER its original
	// 15-wide schema, in metricsHdr order. A metrics file whose header is that
	// original schema (or any prefix ending before these) is still read: each
	// missing trailing column is filled from metricsDefaults, so a row ledgered
	// before the column existed keeps parsing. This is the additive contract:
	// append only, default on read, never break an old row.
	appendedMetricsCols = []string{"silent_loss"}
	// metricsDefaults supplies the value each appended column takes when a legacy
	// row predating it is read. silent_loss → "false": a row ledgered before the
	// column existed carries no verdict and not-flagged is the only safe default;
	// the next full-corpus pass recomputes the real value.
	metricsDefaults = map[string]string{
		"silent_loss": "false",
	}
	trendHdr = []string{"pass", "delivered_tasks", "turns", "bytes", "retries"}
)

// minMetricsCols is the narrowest metrics header loadMetrics will accept: the
// current schema minus its append-only trailing columns (i.e. the original
// 15-wide schema). A header narrower than this is a genuinely broken/truncated
// file, not a clean legacy row, and is rejected.
func minMetricsCols() int { return len(metricsHdr) - len(appendedMetricsCols) }

// MetricRow is one audited session pinned to the pass that recorded it.
type MetricRow struct {
	Pass    int
	Session Session
}

// Ledger is the parsed append-only audit record. It is the audited-state (which
// sessions have been seen) and the trend history (cost per delivered task per
// pass) in one place.
type Ledger struct {
	Metrics []MetricRow
	Trends  []Trend
}

// LoadLedger reads the ledger from dir. A missing dir or missing file is an empty
// (not erroneous) ledger: the first pass starts from nothing.
func LoadLedger(dir string) (*Ledger, error) {
	l := &Ledger{}
	if err := loadMetrics(filepath.Join(dir, MetricsFile), l); err != nil {
		return nil, err
	}
	if err := loadTrend(filepath.Join(dir, TrendFile), l); err != nil {
		return nil, err
	}
	return l, nil
}

// Audited returns the set of session IDs already in the ledger.
func (l *Ledger) Audited() map[string]bool {
	out := make(map[string]bool, len(l.Metrics))
	for _, m := range l.Metrics {
		out[m.Session.ID] = true
	}
	return out
}

// NextPass is the pass number for the next AppendPass: one past the highest
// recorded pass, or 1 for an empty ledger.
func (l *Ledger) NextPass() int {
	max := 0
	for _, m := range l.Metrics {
		if m.Pass > max {
			max = m.Pass
		}
	}
	for _, t := range l.Trends {
		if t.Pass > max {
			max = t.Pass
		}
	}
	return max + 1
}

// AppendPass records sessions and the pass trend under trend.Pass. Sessions are
// appended in the given order (the caller passes the epoch-sorted window).
func (l *Ledger) AppendPass(sessions []Session, trend Trend) {
	for _, s := range sessions {
		l.Metrics = append(l.Metrics, MetricRow{Pass: trend.Pass, Session: s})
	}
	l.Trends = append(l.Trends, trend)
}

// Save writes both TSV files under dir (created if absent), in slice order.
func (l *Ledger) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeTSV(filepath.Join(dir, MetricsFile), metricsHdr, l.metricRows()); err != nil {
		return err
	}
	return writeTSV(filepath.Join(dir, TrendFile), trendHdr, l.trendRows())
}

func (l *Ledger) metricRows() [][]string {
	rows := make([][]string, 0, len(l.Metrics))
	for _, m := range l.Metrics {
		s := m.Session
		h := s.Heuristics
		rows = append(rows, []string{
			strconv.Itoa(m.Pass), s.ID, strconv.FormatInt(s.Epoch, 10),
			s.Submodule, s.Kind, s.Branch, s.TaskID,
			strconv.FormatInt(s.Bytes, 10), strconv.Itoa(s.Turns), strconv.Itoa(s.UserTurns),
			strconv.FormatBool(h.Aborted), strconv.FormatBool(h.LostRace),
			strconv.FormatBool(h.CompletionMiss), strconv.FormatBool(h.ReconcileLoop),
			tsvSafe(h.AbortReason), strconv.FormatBool(h.SilentLoss),
		})
	}
	return rows
}

func (l *Ledger) trendRows() [][]string {
	rows := make([][]string, 0, len(l.Trends))
	for _, t := range l.Trends {
		rows = append(rows, []string{
			strconv.Itoa(t.Pass), strconv.Itoa(t.DeliveredTasks),
			strconv.Itoa(t.Turns), strconv.FormatInt(t.Bytes, 10), strconv.Itoa(t.Retries),
		})
	}
	return rows
}

func loadMetrics(path string, l *Ledger) error {
	recs, err := readMetricsTSV(path)
	if err != nil || recs == nil {
		return err
	}
	for _, r := range recs {
		pass, err := atoi(r[0], path)
		if err != nil {
			return err
		}
		epoch, err := atoi64(r[2], path)
		if err != nil {
			return err
		}
		bytes, err := atoi64(r[7], path)
		if err != nil {
			return err
		}
		turns, err := atoi(r[8], path)
		if err != nil {
			return err
		}
		userTurns, err := atoi(r[9], path)
		if err != nil {
			return err
		}
		aborted, lostRace, completionMiss, reconcileLoop, err := parseBools(r[10], r[11], r[12], r[13], path)
		if err != nil {
			return err
		}
		silentLoss, err := parseBool(r[15], path)
		if err != nil {
			return err
		}
		l.Metrics = append(l.Metrics, MetricRow{
			Pass: pass,
			Session: Session{
				ID: r[1], Epoch: epoch, Submodule: r[3], Kind: r[4], Branch: r[5], TaskID: r[6],
				Bytes: bytes, Turns: turns, UserTurns: userTurns,
				Heuristics: Heuristics{
					Aborted: aborted, LostRace: lostRace,
					CompletionMiss: completionMiss, ReconcileLoop: reconcileLoop,
					AbortReason: r[14], SilentLoss: silentLoss,
				},
			},
		})
	}
	return nil
}

// readMetricsTSV reads metrics.tsv into rows normalized to the CURRENT metricsHdr
// width. It is the additive-schema reader: the on-disk header may be the current
// schema OR any legacy prefix of it no narrower than minMetricsCols (the original
// 15-wide schema), and every data row must match that on-disk width; each row is
// then right-padded with metricsDefaults for the append-only columns it predates,
// so callers can index the full current layout unconditionally. A missing file
// yields (nil, nil): an empty ledger, not an error.
func readMetricsTSV(path string) ([][]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var rows [][]string
	width := 0
	first := true
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if first {
			w, err := validateMetricsHeader(fields, path)
			if err != nil {
				return nil, err
			}
			width = w
			first = false
			continue
		}
		if len(fields) != width {
			return nil, fmt.Errorf("audit: %s: row has %d cols, want %d", path, len(fields), width)
		}
		rows = append(rows, padMetricsRow(fields))
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return rows, nil
}

// validateMetricsHeader accepts the on-disk metrics header iff it is a PREFIX of
// the current metricsHdr no narrower than minMetricsCols — i.e. exactly the
// current schema, or a legacy schema missing only the append-only trailing
// columns — and returns its column count. A header of the wrong width, or whose
// names diverge from metricsHdr at any shared position (a reorder/rename, not a
// clean append), is rejected: the additive contract permits appended columns,
// never a changed prefix.
func validateMetricsHeader(fields []string, path string) (int, error) {
	n := len(fields)
	if n < minMetricsCols() || n > len(metricsHdr) {
		return 0, fmt.Errorf("audit: %s: header has %d cols, want %d..%d", path, n, minMetricsCols(), len(metricsHdr))
	}
	for i := range fields {
		if fields[i] != metricsHdr[i] {
			return 0, fmt.Errorf("audit: %s: header col %d = %q, want %q", path, i, fields[i], metricsHdr[i])
		}
	}
	return n, nil
}

// padMetricsRow right-pads a legacy row to the full current width, filling each
// appended column from metricsDefaults. A row already at full width is returned
// unchanged. The input width is header-validated (readMetricsTSV), so this only
// ever appends the schema's own declared defaults.
func padMetricsRow(fields []string) []string {
	if len(fields) == len(metricsHdr) {
		return fields
	}
	row := make([]string, len(metricsHdr))
	copy(row, fields)
	for i := len(fields); i < len(metricsHdr); i++ {
		row[i] = metricsDefaults[metricsHdr[i]]
	}
	return row
}

func loadTrend(path string, l *Ledger) error {
	recs, err := readTSV(path, trendHdr)
	if err != nil || recs == nil {
		return err
	}
	for _, r := range recs {
		pass, err := atoi(r[0], path)
		if err != nil {
			return err
		}
		dt, err := atoi(r[1], path)
		if err != nil {
			return err
		}
		turns, err := atoi(r[2], path)
		if err != nil {
			return err
		}
		bytes, err := atoi64(r[3], path)
		if err != nil {
			return err
		}
		retries, err := atoi(r[4], path)
		if err != nil {
			return err
		}
		l.Trends = append(l.Trends, Trend{
			Pass: pass, DeliveredTasks: dt, Turns: turns, Bytes: bytes, Retries: retries,
		})
	}
	return nil
}

// readTSV returns the data rows (header validated, excluded). A missing file
// yields (nil, nil): an empty ledger, not an error.
func readTSV(path string, header []string) ([][]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var rows [][]string
	first := true
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if first {
			if len(fields) != len(header) {
				return nil, fmt.Errorf("audit: %s: header has %d cols, want %d", path, len(fields), len(header))
			}
			first = false
			continue
		}
		if len(fields) != len(header) {
			return nil, fmt.Errorf("audit: %s: row has %d cols, want %d", path, len(fields), len(header))
		}
		rows = append(rows, fields)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return rows, nil
}

func writeTSV(path string, header []string, rows [][]string) error {
	var b strings.Builder
	b.WriteString(strings.Join(header, "\t"))
	b.WriteByte('\n')
	for _, r := range rows {
		b.WriteString(strings.Join(r, "\t"))
		b.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// tsvSafe strips tab/newline so a free-text field cannot break the TSV grid.
func tsvSafe(s string) string {
	return strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(s)
}

func atoi(s, path string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("audit: %s: bad int %q: %w", path, s, err)
	}
	return n, nil
}

func atoi64(s, path string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("audit: %s: bad int %q: %w", path, s, err)
	}
	return n, nil
}

func parseBool(s, path string) (bool, error) {
	b, err := strconv.ParseBool(s)
	if err != nil {
		return false, fmt.Errorf("audit: %s: bad bool %q: %w", path, s, err)
	}
	return b, nil
}

func parseBools(a, b, c, d, path string) (bool, bool, bool, bool, error) {
	pa, err := parseBool(a, path)
	if err != nil {
		return false, false, false, false, err
	}
	pb, err := parseBool(b, path)
	if err != nil {
		return false, false, false, false, err
	}
	pc, err := parseBool(c, path)
	if err != nil {
		return false, false, false, false, err
	}
	pd, err := parseBool(d, path)
	if err != nil {
		return false, false, false, false, err
	}
	return pa, pb, pc, pd, nil
}
