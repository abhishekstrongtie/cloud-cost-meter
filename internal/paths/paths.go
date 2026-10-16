// Package paths decides where CostMeter keeps its settings, cache, alert state, lock and log.
// Fake mode gets its own settings, cache, alert state and log so demos and tests never touch real data.
package paths

import (
	"os"
	"path/filepath"
)

type Paths struct {
	Cache      string
	AlertState string
	Settings   string // what was chosen in the menu: refresh interval, services listed, tracked scopes
	Lock       string
	Log        string
}

// New returns the paths for the current user and creates their parent directories.
func New(fake bool) (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	p := ForHome(home, fake)
	if err := p.mkdirs(); err != nil {
		return Paths{}, err
	}
	return p, nil
}

// ForHome computes the paths for a given home directory without touching the filesystem.
func ForHome(home string, fake bool) Paths {
	suffix, logName := "", "costmeter.log"
	if fake {
		suffix, logName = "-fake", "costmeter-fake.log"
	}
	appSupport := filepath.Join(home, "Library", "Application Support")

	return Paths{
		Cache:      filepath.Join(home, "Library", "Caches", "costmeter"+suffix, "snapshots.json"),
		AlertState: filepath.Join(appSupport, "costmeter"+suffix, "alerts.json"),
		Settings:   filepath.Join(appSupport, "costmeter"+suffix, "settings.json"),
		Lock:       filepath.Join(appSupport, "costmeter", "costmeter.lock"), // shared: one instance in total
		Log:        filepath.Join(home, "Library", "Logs", logName),
	}
}

func (p Paths) mkdirs() error {
	for _, file := range []string{p.Cache, p.AlertState, p.Settings, p.Lock, p.Log} {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return err
		}
	}
	return nil
}
