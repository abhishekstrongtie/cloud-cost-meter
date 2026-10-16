// Package config describes how CostMeter is set up right now: the defaults, with whatever the
// user chose in the menu laid over them (see package settings). There is no config file.
package config

import (
	"fmt"
	"strings"
	"time"
)

const (
	MinRefresh       = time.Hour
	MaxSubscriptions = 10
	MaxServices      = 5 // how many services the menu can list per scope, before "Other"
)

type Config struct {
	Refresh time.Duration
	Azure   Azure
}

type Azure struct {
	Subscriptions Subscriptions
	MaxServices   int
}

// Subscriptions is either "auto" (every enabled subscription) or an explicit list of scopes.
type Subscriptions struct {
	Auto bool
	List []Subscription
}

// Subscription is one tracked scope: a subscription, optionally narrowed to one resource group.
type Subscription struct {
	ID            string
	Name          string
	ResourceGroup string
}

// Default is what CostMeter does until the user chooses otherwise.
func Default() Config {
	return Config{
		Refresh: 4 * time.Hour,
		Azure: Azure{
			Subscriptions: Subscriptions{Auto: true},
			MaxServices:   MaxServices,
		},
	}
}

// Validate checks a configuration. The menu only offers valid choices, so this guards against a
// hand-edited or damaged settings file.
func (c Config) Validate() error {
	if c.Refresh < MinRefresh {
		return fmt.Errorf("refresh must be at least %s, got %s", MinRefresh, c.Refresh)
	}
	if c.Azure.MaxServices < 1 || c.Azure.MaxServices > MaxServices {
		return fmt.Errorf("services listed must be 1..%d, got %d", MaxServices, c.Azure.MaxServices)
	}
	if n := len(c.Azure.Subscriptions.List); n > MaxSubscriptions {
		return fmt.Errorf("%d subscriptions are tracked, at most %d can be", n, MaxSubscriptions)
	}
	seen := map[string]bool{}
	for i, s := range c.Azure.Subscriptions.List {
		if s.ID == "" {
			return fmt.Errorf("tracked subscription %d has no id", i)
		}
		id := strings.ToLower(s.ID)
		if seen[id] {
			return fmt.Errorf("subscription %s is tracked twice; each subscription can have only one scope", s.ID)
		}
		seen[id] = true
	}
	return nil
}
