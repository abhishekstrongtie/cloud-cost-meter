// Package cache keeps the last fetched snapshots on disk, so the menu has numbers the
// moment the app starts and Azure is only asked again when they are due.
package cache

import (
	"encoding/json"
	"errors"
	"log"
	"os"

	"costmeter/internal/fsutil"
	"costmeter/internal/provider"
)

const version = 1

type file struct {
	V         int                 `json:"v"`
	Snapshots []provider.Snapshot `json:"snapshots"`
}

type Store struct{ path string }

func New(path string) *Store { return &Store{path: path} }

// Load returns the cached snapshots. A missing, unreadable, corrupt or other-version file
// yields none (and a log line, except when the file simply does not exist yet).
func (s *Store) Load() []provider.Snapshot {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		log.Printf("cache: cannot read %s, starting empty: %v", s.path, err)
		return nil
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		log.Printf("cache: %s is corrupt, starting empty: %v", s.path, err)
		return nil
	}
	if f.V != version {
		log.Printf("cache: %s has version %d, want %d; discarding it", s.path, f.V, version)
		return nil
	}
	return f.Snapshots
}

// Save replaces the cache atomically, so a crash never leaves a half-written file.
func (s *Store) Save(snaps []provider.Snapshot) error {
	data, err := json.Marshal(file{V: version, Snapshots: snaps})
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(s.path, data, 0o600) // cost data is private
}
