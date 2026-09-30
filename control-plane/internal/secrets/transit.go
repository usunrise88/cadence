package secrets

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// transitPattern names a value in transit: the id of the task that carries it (agent credential writes, act_…).
var transitPattern = regexp.MustCompile(`^act_[0-9a-f-]{36}$`)

func (s *Store) transitDir() string { return filepath.Join(s.dir, "transit") }

func (s *Store) transitPath(name string) (string, error) {
	if !transitPattern.MatchString(name) {
		return "", fmt.Errorf("transit name %q is malformed", name)
	}
	return filepath.Join(s.transitDir(), name), nil
}

// SealTransit keeps value encrypted under name until DropTransit: a value on its way to another component (an agent
// credential that the agent host writes into its own volume, docs/spec/08-resolutions.md R3). Transit values have no
// row in Postgres and are not secrets.list entries; the task that carries one names it.
func (s *Store) SealTransit(name string, value []byte) error {
	p, err := s.transitPath(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.transitDir(), 0o700); err != nil {
		return fmt.Errorf("create transit store: %w", err)
	}
	return s.seal(s.transitDir(), p, name, value)
}

// OpenTransit returns the value sealed under name; fs.ErrNotExist (wrapped) when there is none.
func (s *Store) OpenTransit(name string) ([]byte, error) {
	p, err := s.transitPath(name)
	if err != nil {
		return nil, err
	}
	return s.open(p, name)
}

// DropTransit deletes the value sealed under name; a missing one is not an error.
func (s *Store) DropTransit(name string) error {
	p, err := s.transitPath(name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("drop transit %s: %w", name, err)
	}
	return nil
}

// TransitNames lists the values in transit with their modification times (the sweeper drops orphans).
func (s *Store) TransitNames() (map[string]time.Time, error) {
	entries, err := os.ReadDir(s.transitDir())
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]time.Time{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list transit store: %w", err)
	}
	out := make(map[string]time.Time, len(entries))
	for _, e := range entries {
		if !transitPattern.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out[e.Name()] = info.ModTime()
	}
	return out, nil
}
