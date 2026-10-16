// Package scheduler refreshes cost data in the background and tells subscribers about it.
//
// One goroutine (Run) owns all state, so there are no races between a scheduled refresh, a
// Refresh Now click and a scope change. Time and the one-minute tick are injectable.
package scheduler

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"costmeter/internal/config"
	"costmeter/internal/provider"
)

const (
	tickEvery      = time.Minute
	manualDebounce = 5 * time.Minute // Refresh Now is ignored if a refresh finished this recently
)

// Update is everything a subscriber needs to draw the current state. The slices and the
// values they point to are shared with the scheduler and must be treated as read-only.
type Update struct {
	Snapshots   []provider.Snapshot
	Config      config.Config      // the config actually in use
	ConfigErr   string             // "" if the last load was valid
	CycleErr    error              // ErrNotAuthorized / ErrAzureCLIMissing that aborted a cycle, else nil
	Truncated   int                // subscriptions not shown (auto mode, >10)
	Tracked     []provider.Account // the scopes being fetched, in menu order
	Available   []provider.Account // every enabled subscription, for the scope picker
	Refreshing  bool
	LastRefresh time.Time
	NextRefresh time.Time
}

// Cache persists snapshots between runs; *cache.Store implements it.
type Cache interface {
	Load() []provider.Snapshot
	Save([]provider.Snapshot) error
}

type Options struct {
	Provider   provider.CostProvider
	Cache      Cache
	LoadConfig func() (config.Config, error) // called before every refresh; nil means defaults
	Now        func() time.Time              // default: the wall clock
	Tick       <-chan time.Time              // default: a one-minute ticker
}

// configurable is implemented by providers that take part of the config (azure.Provider does).
type configurable interface{ Configure(config.Azure) }

type Scheduler struct {
	prov       provider.CostProvider
	cache      Cache
	loadConfig func() (config.Config, error)
	now        func() time.Time
	tick       <-chan time.Time

	subsMu sync.Mutex
	subs   []func(Update)

	refreshReq chan struct{}
	applyReq   chan struct{}
	reloadReq  chan struct{}

	// Everything below belongs to the Run goroutine.
	cfg         config.Config
	haveConfig  bool
	cfgErr      string
	snaps       []provider.Snapshot
	accounts    []provider.Account // the tracked scopes, from the latest Accounts call
	available   []provider.Account // every enabled subscription, from the latest listing
	truncated   int
	cycleErr    error
	lastRefresh time.Time // last cycle that got through to Azure
	lastAttempt time.Time // last cycle of any kind; drives scheduling and the debounce
}

func New(o Options) *Scheduler {
	s := &Scheduler{
		prov:       o.Provider,
		cache:      o.Cache,
		loadConfig: o.LoadConfig,
		now:        o.Now,
		tick:       o.Tick,
		refreshReq: make(chan struct{}, 1),
		applyReq:   make(chan struct{}, 1),
		reloadReq:  make(chan struct{}, 1),
	}
	if s.now == nil {
		// Round(0) drops the monotonic reading, which does not advance while the Mac sleeps;
		// without it a night's sleep would look like a few minutes.
		s.now = func() time.Time { return time.Now().Round(0) }
	}
	if s.loadConfig == nil {
		s.loadConfig = func() (config.Config, error) { return config.Default(), nil }
	}
	return s
}

// Subscribe registers fn to receive every update. Call it before Run. fn runs on the
// scheduler's goroutine, so it must return quickly; hand slow work to another goroutine.
func (s *Scheduler) Subscribe(fn func(Update)) {
	s.subsMu.Lock()
	s.subs = append(s.subs, fn)
	s.subsMu.Unlock()
}

// RefreshNow asks for a refresh. It never blocks, and is ignored when one finished less than
// five minutes ago (unless that one failed to sign in, so recovering after `az login` works).
func (s *Scheduler) RefreshNow() {
	select {
	case s.refreshReq <- struct{}{}:
	default: // one is already queued
	}
}

// Apply refreshes straight away, whatever the debounce says. Use it after the tracked scopes
// were changed, so the menu does not show stale numbers for a scope the user just picked.
func (s *Scheduler) Apply() {
	select {
	case s.applyReq <- struct{}{}:
	default: // one is already queued
	}
}

// Reload re-reads the settings and redraws, without fetching anything. Use it after a setting
// that needs no new data changed, such as the refresh interval or how many services are listed.
func (s *Scheduler) Reload() {
	select {
	case s.reloadReq <- struct{}{}:
	default: // one is already queued
	}
}

// Run publishes the cached data, refreshes if it is due, and then keeps going until ctx ends.
func (s *Scheduler) Run(ctx context.Context) {
	tick := s.tick
	if tick == nil {
		t := time.NewTicker(tickEvery)
		defer t.Stop()
		tick = t.C
	}
	s.start(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			s.onTick(ctx)
		case <-s.refreshReq:
			s.onRefreshNow(ctx)
		case <-s.applyReq:
			s.onApply(ctx)
		case <-s.reloadReq:
			s.onReload()
		}
	}
}

func (s *Scheduler) start(ctx context.Context) {
	s.reloadConfig()
	s.snaps = s.cache.Load()
	for _, snap := range s.snaps {
		s.accounts = append(s.accounts, snap.Account)
		if snap.FetchedAt.After(s.lastRefresh) {
			s.lastRefresh = snap.FetchedAt
		}
	}
	s.lastAttempt = s.lastRefresh

	due := s.due(s.now())
	s.publish(due)
	if due {
		s.cycle(ctx, false)
	}
}

// onTick runs every minute. Comparing wall-clock time, rather than counting ticks, is what
// makes a refresh happen promptly after the Mac wakes from sleep.
func (s *Scheduler) onTick(ctx context.Context) {
	if s.due(s.now()) {
		s.cycle(ctx, true)
	}
}

func (s *Scheduler) onRefreshNow(ctx context.Context) {
	if s.cycleErr == nil && !s.lastAttempt.IsZero() && s.now().Sub(s.lastAttempt) < manualDebounce {
		return
	}
	s.cycle(ctx, true)
}

func (s *Scheduler) onReload() {
	s.reloadConfig()
	s.publish(false)
}

// onApply refreshes without looking at the debounce.
func (s *Scheduler) onApply(ctx context.Context) { s.cycle(ctx, true) }

func (s *Scheduler) due(now time.Time) bool {
	return s.lastAttempt.IsZero() || now.Sub(s.lastAttempt) >= s.cfg.Refresh
}

// reloadConfig re-reads the config. An invalid file keeps the config already in use; at
// startup, when there is none yet, the built-in defaults are used instead.
func (s *Scheduler) reloadConfig() {
	cfg, err := s.loadConfig()
	switch {
	case err == nil:
		s.cfg, s.haveConfig, s.cfgErr = cfg, true, ""
	case !s.haveConfig:
		s.cfg, s.haveConfig, s.cfgErr = config.Default(), true, err.Error()
	default:
		s.cfgErr = err.Error()
		return
	}
	if c, ok := s.prov.(configurable); ok {
		c.Configure(s.cfg.Azure)
	}
}

func isAuthError(err error) bool {
	return errors.Is(err, provider.ErrNotAuthorized) || errors.Is(err, provider.ErrAzureCLIMissing)
}

// cycle fetches every account in turn. announce publishes a "refreshing" update first.
func (s *Scheduler) cycle(ctx context.Context, announce bool) {
	if announce {
		s.publish(true)
	}
	s.reloadConfig()
	start := s.now()

	accounts, truncated, err := s.prov.Accounts(ctx)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		s.failCycle(err)
		return
	}

	// Every subscription the login can see, for the scope picker. Best effort: a failure keeps
	// the previous list. Only a provider that can list them is asked.
	if lister, ok := s.prov.(provider.SubscriptionLister); ok {
		switch subs, err := lister.Subscriptions(ctx); {
		case ctx.Err() != nil:
			return
		case err != nil:
			log.Printf("scheduler: cannot list subscriptions: %v", err)
		default:
			s.available = subs
		}
	}

	prev := make(map[string]provider.Snapshot, len(s.snaps))
	for _, snap := range s.snaps {
		prev[snap.Account.Key()] = snap
	}
	var next []provider.Snapshot
	for _, a := range accounts {
		snap, err := s.prov.Snapshot(ctx, a, start)
		if ctx.Err() != nil {
			return
		}
		switch {
		case err == nil:
			next = append(next, snap)
		case isAuthError(err):
			s.failCycle(err)
			return
		default:
			log.Printf("scheduler: %s: %v", a.Label(), err)
			// Keep what we had, marked stale; it is tried again at the next refresh, or Refresh Now.
			p, ok := prev[a.Key()]
			if !ok {
				// Never fetched, so there is nothing to keep. A placeholder (no amounts, zero
				// FetchedAt) makes the failure visible instead of the scope silently vanishing.
				p = provider.Snapshot{Account: a}
			}
			p.Stale, p.Err = true, err.Error()
			next = append(next, p)
		}
	}

	s.snaps, s.accounts, s.truncated, s.cycleErr = next, accounts, truncated, nil
	s.lastRefresh = s.now()
	s.lastAttempt = s.lastRefresh
	s.save()
	s.publish(false)
}

// failCycle ends a cycle that could not fetch anything. The old data stays, marked stale.
// Only a sign-in problem is reported as CycleErr; anything else is shown per snapshot.
func (s *Scheduler) failCycle(err error) {
	log.Printf("scheduler: refresh failed: %v", err)
	s.markAllStale(err)
	if isAuthError(err) {
		s.cycleErr = err
	} else {
		s.cycleErr = nil
	}
	s.lastAttempt = s.now() // lastRefresh stays: the data was not refreshed
	s.publish(false)
}

func (s *Scheduler) markAllStale(err error) {
	for i := range s.snaps {
		s.snaps[i].Stale, s.snaps[i].Err = true, err.Error()
	}
}

func (s *Scheduler) save() {
	if err := s.cache.Save(s.snaps); err != nil {
		log.Printf("scheduler: cannot save the cache: %v", err)
	}
}

func (s *Scheduler) publish(refreshing bool) {
	next := s.now()
	if !s.lastAttempt.IsZero() {
		next = s.lastAttempt.Add(s.cfg.Refresh)
	}
	u := Update{
		Snapshots:   append([]provider.Snapshot(nil), s.snaps...),
		Config:      s.cfg,
		ConfigErr:   s.cfgErr,
		CycleErr:    s.cycleErr,
		Truncated:   s.truncated,
		Tracked:     append([]provider.Account(nil), s.accounts...),
		Available:   append([]provider.Account(nil), s.available...),
		Refreshing:  refreshing,
		LastRefresh: s.lastRefresh,
		NextRefresh: next,
	}

	s.subsMu.Lock()
	subs := make([]func(Update), len(s.subs))
	copy(subs, s.subs)
	s.subsMu.Unlock()
	for _, fn := range subs {
		fn(u)
	}
}
