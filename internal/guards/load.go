package guards

import (
	"errors"
	"fmt"
	"os"
)

// Load reads and parses the GUARDS.md at path. A non-existent file returns
// ErrNoGuardsFile (distinguishable via errors.Is) — the benign "no guards
// declared" condition, not a failure. Any other read/parse failure is wrapped. On
// success the returned *Guards is never nil. Mirrors checks.Load.
func Load(path string) (*Guards, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNoGuardsFile
		}
		return nil, fmt.Errorf("guards: reading %s: %w", path, err)
	}
	g, err := Parse(string(b))
	if err != nil {
		return nil, fmt.Errorf("guards: parsing %s: %w", path, err)
	}
	return g, nil
}
