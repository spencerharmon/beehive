package web

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// sessEntry is one enumerated session transcript file: its id (the file stem,
// no ".md") plus the two metadata a hot page scan needs — the file size (to tell
// a tiny live stub from a durable multi-KB transcript without opening it) and
// the file mtime (to order the sessions list newest-first). Neither requires
// reading the file body.
type sessEntry struct {
	ID   string
	Size int64
	Mod  time.Time
}

// scanWorkers bounds the fan-out of the parallel session-dir stat scan. The scan
// is pure I/O (a stat syscall per file), so a pool several times the CPU count
// hides per-file latency — the syscalls block on the filesystem, not the CPU —
// while staying well short of exhausting file-descriptor / thread limits on a
// dir with thousands of entries.
func scanWorkers() int {
	n := runtime.NumCPU() * 4
	if n < 8 {
		n = 8
	}
	if n > 64 {
		n = 64
	}
	return n
}

// scanSessionDir enumerates the ".md" session transcripts in dir and stats each
// in PARALLEL, returning one sessEntry per file (order unspecified — callers
// sort as they need). Enumerating and stat-ing the whole sessions/ directory is
// the single hot primitive behind the dashboard's active-honeybee count, /stats'
// aggregation, and the sessions list; on a mature hive (thousands of accumulated
// transcripts totalling hundreds of MB) the per-file stat latency — not any
// parse — dominates page load, and the stats are independent, so fanning them
// across a bounded worker pool hides that latency (concurrent syscalls) instead
// of paying it one blocking stat at a time. A file that vanishes or fails to
// stat mid-scan is simply dropped (best-effort, matching every other
// file-derived view). A missing dir yields no entries and no error.
func scanSessionDir(dir string) []sessEntry {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		names = append(names, e.Name())
	}
	if len(names) == 0 {
		return nil
	}
	out := make([]sessEntry, len(names))
	ok := make([]bool, len(names))
	var idx int64
	var mu sync.Mutex
	next := func() (int, bool) {
		mu.Lock()
		defer mu.Unlock()
		if int(idx) >= len(names) {
			return 0, false
		}
		i := int(idx)
		idx++
		return i, true
	}
	workers := scanWorkers()
	if workers > len(names) {
		workers = len(names)
	}
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i, more := next()
				if !more {
					return
				}
				fi, err := os.Stat(filepath.Join(dir, names[i]))
				if err != nil {
					continue
				}
				out[i] = sessEntry{
					ID:   strings.TrimSuffix(names[i], ".md"),
					Size: fi.Size(),
					Mod:  fi.ModTime(),
				}
				ok[i] = true
			}
		}()
	}
	wg.Wait()
	res := make([]sessEntry, 0, len(names))
	for i := range out {
		if ok[i] {
			res = append(res, out[i])
		}
	}
	return res
}

// parallelMap applies f to each element of items across a bounded worker pool
// (scanWorkers), returning the results index-aligned with items. It is the
// generic fan-out behind the hot per-session read loops (header-tag derivation,
// stub classification): the work is per-file I/O with no cross-item dependency,
// so spreading it over the pool hides the accumulated read latency that
// otherwise dominates page load on a thousands-of-transcripts hive. f must be
// safe for concurrent use (the callers pass pure, read-only per-file work).
func parallelMap[T any](items []string, f func(string) T) []T {
	out := make([]T, len(items))
	if len(items) == 0 {
		return out
	}
	var idx int64
	var mu sync.Mutex
	next := func() (int, bool) {
		mu.Lock()
		defer mu.Unlock()
		if int(idx) >= len(items) {
			return 0, false
		}
		i := int(idx)
		idx++
		return i, true
	}
	workers := scanWorkers()
	if workers > len(items) {
		workers = len(items)
	}
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i, more := next()
				if !more {
					return
				}
				out[i] = f(items[i])
			}
		}()
	}
	wg.Wait()
	return out
}

// sortSessEntriesByModDesc orders entries newest-first by file mtime, in place,
// so the sessions list's page 1 holds the most-recent transcripts. Ties break on
// ID for a deterministic order.
func sortSessEntriesByModDesc(es []sessEntry) {
	sort.Slice(es, func(i, j int) bool {
		if !es[i].Mod.Equal(es[j].Mod) {
			return es[i].Mod.After(es[j].Mod)
		}
		return es[i].ID > es[j].ID
	})
}
