package checks

import (
	"errors"
	"fmt"
	"os"
)

// Load reads and parses the CHECKS.md at path. A non-existent file returns
// ErrNoChecksFile (distinguishable via errors.Is); any other read/parse failure
// returns a wrapped error. On success the returned *Checks is never nil.
func Load(path string) (*Checks, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNoChecksFile
		}
		return nil, fmt.Errorf("checks: reading %s: %w", path, err)
	}
	c, err := Parse(string(b))
	if err != nil {
		return nil, fmt.Errorf("checks: parsing %s: %w", path, err)
	}
	return c, nil
}
