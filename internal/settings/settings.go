// Package settings keeps what the user chose in the menu: how often to refresh, how many
// services to list, and which subscriptions (and resource groups) to track. It is saved in one
// small file of its own; there is no config file to edit.
package settings

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"costmeter/internal/config"
	"costmeter/internal/fsutil"
	"costmeter/internal/provider"
)

const version = 1

// MaxTracked is how many subscriptions can be tracked at once (the menu has room for 10).
const MaxTracked = config.MaxSubscriptions

var ErrTooMany = errors.New("at most 10 subscriptions can be tracked")

// The choices the Settings menu offers.
var (
	RefreshOptions  = []time.Duration{1 * time.Hour, 2 * time.Hour, 4 * time.Hour, 8 * time.Hour, 12 * time.Hour, 24 * time.Hour}
	ServicesOptions = []int{3, 4, 5}
)

// Choice is one tracked scope. An empty ResourceGroup means the whole subscription.
type Choice struct {
	Subscription  string `json:"subscription"`
	Name          string `json:"name"`
	ResourceGroup string `json:"resourceGroup"`
}

// Settings holds the choices made in the menu. A zero value means "the default".
type Settings struct {
	RefreshMinutes int `json:"refreshMinutes,omitempty"`
	MaxServices    int `json:"maxServices,omitempty"`
	// Scopes is nil until the user picks a scope, which means every enabled subscription is
	// tracked. A non-nil empty list means nothing is tracked.
	Scopes *[]Choice `json:"scopes,omitempty"`
}

type file struct {
	V int `json:"v"`
	Settings
}

// Store saves Settings in a file. It is safe for concurrent use: the menu's click handlers
// each change one setting, and none may overwrite another's change.
type Store struct {
	path string
	mu   sync.Mutex
}

func New(path string) *Store { return &Store{path: path} }

// Load returns the saved settings. A missing, unreadable, corrupt or other-version file gives
// the defaults (and a log line, except when the file simply does not exist yet).
func (s *Store) Load() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

func (s *Store) load() Settings {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return Settings{}
	}
	if err != nil {
		log.Printf("settings: cannot read %s, using the defaults: %v", s.path, err)
		return Settings{}
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		log.Printf("settings: %s is corrupt, using the defaults: %v", s.path, err)
		return Settings{}
	}
	if f.V != version {
		log.Printf("settings: %s has version %d, want %d; ignoring it", s.path, f.V, version)
		return Settings{}
	}
	return f.Settings
}

// Update changes the saved settings: it loads them, applies fn and saves the result.
func (s *Store) Update(fn func(*Settings) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.load()
	if err := fn(&cur); err != nil {
		return err
	}
	data, err := json.Marshal(file{V: version, Settings: cur})
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(s.path, data, 0o600)
}

// Apply returns cfg with the saved choices laid over it.
func Apply(cfg config.Config, s Settings) config.Config {
	if s.RefreshMinutes > 0 {
		cfg.Refresh = time.Duration(s.RefreshMinutes) * time.Minute
	}
	if s.MaxServices > 0 {
		cfg.Azure.MaxServices = s.MaxServices
	}
	if s.Scopes != nil {
		list := make([]config.Subscription, 0, len(*s.Scopes))
		for _, c := range *s.Scopes {
			list = append(list, config.Subscription{ID: c.Subscription, Name: c.Name, ResourceGroup: c.ResourceGroup})
		}
		cfg.Azure.Subscriptions = config.Subscriptions{List: list}
	}
	return cfg
}

// Set returns choices with c replacing the entry for the same subscription, or added at the end.
// It never changes its argument.
func Set(choices []Choice, c Choice) ([]Choice, error) {
	out := append([]Choice(nil), choices...)
	for i := range out {
		if strings.EqualFold(out[i].Subscription, c.Subscription) {
			out[i] = c
			return out, nil
		}
	}
	if len(out) >= MaxTracked {
		return nil, ErrTooMany
	}
	return append(out, c), nil
}

// Remove returns choices without the given subscription.
func Remove(choices []Choice, subscription string) []Choice {
	out := make([]Choice, 0, len(choices))
	for _, c := range choices {
		if !strings.EqualFold(c.Subscription, subscription) {
			out = append(out, c)
		}
	}
	return out
}

// FromAccounts turns the scopes currently being fetched into choices, the starting point for a change.
func FromAccounts(tracked []provider.Account) []Choice {
	out := make([]Choice, 0, len(tracked))
	for _, a := range tracked {
		out = append(out, Choice{Subscription: a.ID, Name: a.Name, ResourceGroup: a.ResourceGroup})
	}
	return out
}
