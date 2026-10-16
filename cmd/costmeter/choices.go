package main

import (
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"costmeter/internal/config"
	"costmeter/internal/provider"
	"costmeter/internal/settings"
	"costmeter/internal/ui"
)

// configLoader returns the function the scheduler calls before every refresh: the defaults with
// whatever was chosen in the menu laid over them. There is no config file.
func configLoader(store *settings.Store) func() (config.Config, error) {
	return func() (config.Config, error) {
		cfg := settings.Apply(config.Default(), store.Load())
		if err := cfg.Validate(); err != nil { // a hand-edited settings file skips the menu's checks
			return config.Config{}, fmt.Errorf("saved settings: %w", err)
		}
		return cfg, nil
	}
}

// chooser turns clicks in the menu into saved settings.
type chooser struct {
	store   *settings.Store
	tracked func() []provider.Account // the scopes being tracked right now
	apply   func()                    // fetch at once, so the menu shows the newly chosen scope
	reload  func()                    // redraw with the new setting; nothing needs fetching
	saved   atomic.Bool               // scopes have been chosen, so the menu offers to forget them
}

func newChooser(store *settings.Store, tracked func() []provider.Account, apply, reload func()) *chooser {
	c := &chooser{store: store, tracked: tracked, apply: apply, reload: reload}
	c.saved.Store(store.Load().Scopes != nil)
	return c
}

// Saved reports whether scopes have been chosen.
func (c *chooser) Saved() bool { return c.saved.Load() }

// Pick applies one choice from the Scope submenu. It builds on the saved scopes when there are
// some, so two quick picks cannot lose the first; otherwise on what is tracked now.
func (c *chooser) Pick(ch ui.Change) {
	err := c.store.Update(func(cur *settings.Settings) error {
		var choices []settings.Choice
		if cur.Scopes != nil {
			choices = *cur.Scopes
		} else {
			choices = settings.FromAccounts(c.tracked())
		}
		if ch.Remove {
			choices = settings.Remove(choices, ch.Subscription)
		} else {
			var err error
			choices, err = settings.Set(choices, settings.Choice{Subscription: ch.Subscription, Name: ch.Name, ResourceGroup: ch.ResourceGroup})
			if err != nil {
				return err
			}
		}
		cur.Scopes = &choices
		return nil
	})
	if err != nil {
		log.Printf("settings: cannot change the scopes: %v", err)
		return
	}
	c.saved.Store(true)
	c.apply()
}

// Reset forgets the chosen scopes, so every subscription is tracked again.
func (c *chooser) Reset() {
	if err := c.store.Update(func(cur *settings.Settings) error { cur.Scopes = nil; return nil }); err != nil {
		log.Printf("settings: cannot reset the scopes: %v", err)
		return
	}
	c.saved.Store(false)
	c.apply()
}

// SetRefresh chooses how often the data is refreshed.
func (c *chooser) SetRefresh(d time.Duration) {
	if err := c.store.Update(func(cur *settings.Settings) error { cur.RefreshMinutes = int(d / time.Minute); return nil }); err != nil {
		log.Printf("settings: cannot save the refresh interval: %v", err)
		return
	}
	c.reload()
}

// SetServices chooses how many services each row lists.
func (c *chooser) SetServices(n int) {
	if err := c.store.Update(func(cur *settings.Settings) error { cur.MaxServices = n; return nil }); err != nil {
		log.Printf("settings: cannot save the services setting: %v", err)
		return
	}
	c.reload()
}
