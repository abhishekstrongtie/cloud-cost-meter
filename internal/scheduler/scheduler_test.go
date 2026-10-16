package scheduler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"costmeter/internal/config"
	"costmeter/internal/provider"
	"costmeter/internal/provider/fake"
)

func TestMain(m *testing.M) {
	log.SetOutput(io.Discard) // failures are logged by design; tests assert on the updates instead
	os.Exit(m.Run())
}

var base = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// testProv wraps the fake provider with the failure modes the scheduler must cope with.
type testProv struct {
	*fake.Provider

	mu          sync.Mutex
	accountsErr error
	snapshotErr map[string]error // by scope label, e.g. "Office-Prod / rg-web"
	subsErr     error            // returned by Subscriptions
	hide        map[string]bool
	truncated   int
	calls       map[string]int
	configured  []config.Azure
}

func newProv() *testProv {
	return &testProv{
		Provider:    fake.New(),
		snapshotErr: map[string]error{},
		hide:        map[string]bool{},
		calls:       map[string]int{},
	}
}

func (p *testProv) Accounts(ctx context.Context) ([]provider.Account, int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.accountsErr != nil {
		return nil, 0, p.accountsErr
	}
	all, _, err := p.Provider.Accounts(ctx)
	var out []provider.Account
	for _, a := range all {
		if !p.hide[a.Name] {
			out = append(out, a)
		}
	}
	return out, p.truncated, err
}

func (p *testProv) Snapshot(ctx context.Context, a provider.Account, now time.Time) (provider.Snapshot, error) {
	p.mu.Lock()
	p.calls[a.Label()]++
	err := p.snapshotErr[a.Label()]
	p.mu.Unlock()
	if err != nil {
		return provider.Snapshot{}, err
	}
	return p.Provider.Snapshot(ctx, a, now)
}

func (p *testProv) Subscriptions(ctx context.Context) ([]provider.Account, error) {
	p.mu.Lock()
	err := p.subsErr
	p.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return p.Provider.Subscriptions(ctx)
}

func (p *testProv) Configure(cfg config.Azure) {
	p.mu.Lock()
	p.configured = append(p.configured, cfg)
	p.mu.Unlock()
	p.Provider.Configure(cfg) // the fake honours an explicit list of scopes
}

func (p *testProv) callCount(name string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[name]
}

func (p *testProv) set(f func(*testProv)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f(p)
}

type memCache struct {
	mu    sync.Mutex
	snaps []provider.Snapshot
	saves [][]provider.Snapshot
}

func (c *memCache) Load() []provider.Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]provider.Snapshot(nil), c.snaps...)
}

func (c *memCache) Save(s []provider.Snapshot) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.saves = append(c.saves, append([]provider.Snapshot(nil), s...))
	return nil
}

func (c *memCache) saveCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.saves)
}

func fakeAt(t *testing.T, at time.Time) []provider.Snapshot {
	t.Helper()
	p := fake.New()
	accts, _, _ := p.Accounts(context.Background())
	var out []provider.Snapshot
	for _, a := range accts {
		s, err := p.Snapshot(context.Background(), a, at)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

type rig struct {
	t       *testing.T
	s       *Scheduler
	clk     *clock
	prov    *testProv
	cache   *memCache
	cfg     config.Config
	cfgErr  error
	updates []Update
	ctx     context.Context
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, clk: &clock{t: base}, prov: newProv(), cache: &memCache{}, cfg: config.Default(), ctx: context.Background()}
	r.s = New(Options{
		Provider:   r.prov,
		Cache:      r.cache,
		LoadConfig: func() (config.Config, error) { return r.cfg, r.cfgErr },
		Now:        r.clk.Now,
	})
	r.s.Subscribe(func(u Update) { r.updates = append(r.updates, u) })
	return r
}

func (r *rig) start()      { r.s.start(r.ctx) }
func (r *rig) tick()       { r.s.onTick(r.ctx) }
func (r *rig) refreshNow() { r.s.onRefreshNow(r.ctx) }
func (r *rig) apply()      { r.s.onApply(r.ctx) }

func (r *rig) last() Update {
	r.t.Helper()
	if len(r.updates) == 0 {
		r.t.Fatal("no updates were published")
	}
	return r.updates[len(r.updates)-1]
}

func (r *rig) snap(u Update, name string) provider.Snapshot {
	r.t.Helper()
	for _, s := range u.Snapshots {
		if s.Account.Name == name {
			return s
		}
	}
	r.t.Fatalf("no snapshot for %s in %+v", name, u.Snapshots)
	return provider.Snapshot{}
}

func names(u Update) string {
	var out []string
	for _, s := range u.Snapshots {
		out = append(out, s.Account.Name)
	}
	return strings.Join(out, ",")
}

func TestFreshCacheIsPublishedWithoutFetching(t *testing.T) {
	r := newRig(t)
	r.cache.snaps = fakeAt(t, base.Add(-time.Hour))
	r.start()

	if len(r.updates) != 1 {
		t.Fatalf("updates = %d, want just the cached one", len(r.updates))
	}
	u := r.last()
	if names(u) != "Office-Prod,Office-Dev" || u.Refreshing {
		t.Errorf("cached update: %s, refreshing=%v", names(u), u.Refreshing)
	}
	if !u.LastRefresh.Equal(base.Add(-time.Hour)) || !u.NextRefresh.Equal(base.Add(3*time.Hour)) {
		t.Errorf("last %v next %v", u.LastRefresh, u.NextRefresh)
	}
	if r.prov.callCount("Office-Prod") != 0 || r.cache.saveCount() != 0 {
		t.Error("a fresh cache must not trigger a fetch")
	}
}

func TestStaleCacheIsPublishedBeforeTheFetchReplacesIt(t *testing.T) {
	r := newRig(t)
	r.cache.snaps = fakeAt(t, base.Add(-5*time.Hour))
	r.start()

	if len(r.updates) != 2 {
		t.Fatalf("updates = %d, want cached then fresh", len(r.updates))
	}
	cached, fresh := r.updates[0], r.updates[1]
	if !cached.Refreshing || !r.snap(cached, "Office-Prod").FetchedAt.Equal(base.Add(-5*time.Hour)) {
		t.Errorf("first update should be the cached data with Refreshing set: %+v", cached)
	}
	if fresh.Refreshing || !r.snap(fresh, "Office-Prod").FetchedAt.Equal(base) {
		t.Errorf("second update should be fresh data: refreshing=%v", fresh.Refreshing)
	}
	if r.cache.saveCount() != 1 || !reflect.DeepEqual(r.cache.saves[0], fresh.Snapshots) {
		t.Errorf("the fresh data should have been cached once, saves = %d", r.cache.saveCount())
	}
}

func TestEmptyCacheWhileSignedOut(t *testing.T) {
	r := newRig(t)
	r.prov.accountsErr = fmt.Errorf("%w: run az login", provider.ErrNotAuthorized)
	r.start()

	first, final := r.updates[0], r.last()
	if !first.Refreshing || len(first.Snapshots) != 0 {
		t.Errorf("first update = %+v", first)
	}
	if len(final.Snapshots) != 0 || !errors.Is(final.CycleErr, provider.ErrNotAuthorized) || final.Refreshing {
		t.Errorf("final update = %+v, want no snapshots and CycleErr = ErrNotAuthorized", final)
	}
	if !final.LastRefresh.IsZero() || r.cache.saveCount() != 0 {
		t.Errorf("nothing was refreshed: last=%v saves=%d", final.LastRefresh, r.cache.saveCount())
	}
}

func TestMissingAzureCLIIsReportedToo(t *testing.T) {
	r := newRig(t)
	r.prov.accountsErr = fmt.Errorf("%w: no", provider.ErrAzureCLIMissing)
	r.start()
	if !errors.Is(r.last().CycleErr, provider.ErrAzureCLIMissing) {
		t.Errorf("CycleErr = %v", r.last().CycleErr)
	}
}

func TestEverySubscriberGetsEveryUpdate(t *testing.T) {
	r := newRig(t)
	var second []Update
	r.s.Subscribe(func(u Update) { second = append(second, u) })
	r.cache.snaps = fakeAt(t, base.Add(-5*time.Hour))
	r.start()
	r.clk.advance(4 * time.Hour)
	r.tick()

	if len(second) == 0 || len(second) != len(r.updates) {
		t.Fatalf("first subscriber saw %d updates, second %d", len(r.updates), len(second))
	}
	for i := range second {
		if !reflect.DeepEqual(second[i], r.updates[i]) {
			t.Errorf("update %d differs between subscribers", i)
		}
	}
}

// Updates are read-only for subscribers, but a careless one must not corrupt the scheduler's
// own copy: a failing account re-publishes the snapshot the scheduler kept from before.
func TestSubscribersCannotCorruptTheSchedulersData(t *testing.T) {
	clk := &clock{t: base}
	cache := &memCache{}
	prov := newProv()
	s := New(Options{Provider: prov, Cache: cache, Now: clk.Now})
	s.Subscribe(func(u Update) {
		for i := range u.Snapshots {
			u.Snapshots[i].MonthToDate = -1
		}
	})
	ctx := context.Background()
	s.start(ctx)

	clk.advance(4 * time.Hour)
	prov.set(func(p *testProv) { p.Provider.FailAccount = "Office-Prod" })
	s.onTick(ctx)

	if len(cache.saves) != 2 {
		t.Fatalf("saves = %d, want 2", len(cache.saves))
	}
	kept := cache.saves[1][0]
	if kept.Account.Name != "Office-Prod" || !kept.Stale || kept.MonthToDate != 1102.40 {
		t.Errorf("the kept snapshot was changed through a subscriber: %+v", kept)
	}
}

func TestInvalidConfigAtStartupUsesDefaults(t *testing.T) {
	r := newRig(t)
	r.cfg, r.cfgErr = config.Config{}, errors.New("config: refresh must be at least 1h0m0s, got 10m0s")
	r.start()

	u := r.last()
	if !reflect.DeepEqual(u.Config, config.Default()) || !strings.Contains(u.ConfigErr, "refresh must be at least") {
		t.Errorf("config %+v, err %q", u.Config, u.ConfigErr)
	}
	if len(u.Snapshots) != 2 {
		t.Errorf("the app must still work on defaults, got %d snapshots", len(u.Snapshots))
	}
	if n := len(r.prov.configured); n == 0 || !reflect.DeepEqual(r.prov.configured[0], config.Default().Azure) {
		t.Errorf("provider was configured with %+v", r.prov.configured)
	}
}

func TestConfigIsReloadedEveryRefreshAndAnInvalidOneKeepsThePreviousConfig(t *testing.T) {
	r := newRig(t)
	r.start()

	good := config.Default()
	good.Refresh = 2 * time.Hour
	r.cfg = good
	r.clk.advance(4 * time.Hour)
	r.tick()
	if u := r.last(); u.Config.Refresh != 2*time.Hour || u.ConfigErr != "" {
		t.Fatalf("new config not picked up: %+v / %q", u.Config.Refresh, u.ConfigErr)
	}
	if got := r.prov.configured[len(r.prov.configured)-1]; !reflect.DeepEqual(got, good.Azure) {
		t.Errorf("provider not reconfigured: %+v", got)
	}

	r.cfg, r.cfgErr = config.Config{}, errors.New("config: oops")
	r.clk.advance(2 * time.Hour)
	r.tick()
	u := r.last()
	if u.Config.Refresh != 2*time.Hour || u.ConfigErr != "config: oops" {
		t.Errorf("an invalid file must keep the config in use: %v / %q", u.Config.Refresh, u.ConfigErr)
	}
	if len(u.Snapshots) != 2 || r.snap(u, "Office-Prod").Stale {
		t.Error("an invalid config must not stop refreshing")
	}

	r.cfgErr = nil
	r.clk.advance(2 * time.Hour)
	r.tick()
	if r.last().ConfigErr != "" {
		t.Error("ConfigErr should clear once the file is valid again")
	}
}

func TestFailingAccountKeepsItsOldDataMarkedStale(t *testing.T) {
	r := newRig(t)
	r.start()
	r.clk.advance(4 * time.Hour)
	r.prov.set(func(p *testProv) { p.Provider.FailAccount = "Office-Dev" })
	r.tick()

	u := r.last()
	dev, prod := r.snap(u, "Office-Dev"), r.snap(u, "Office-Prod")
	if !dev.Stale || !strings.Contains(dev.Err, "simulated failure") || !dev.FetchedAt.Equal(base) || dev.MonthToDate != 181.80 {
		t.Errorf("Dev should keep its old data, stale: %+v", dev)
	}
	if prod.Stale || !prod.FetchedAt.Equal(base.Add(4*time.Hour)) {
		t.Errorf("Prod should be fresh: %+v", prod)
	}
	if names(u) != "Office-Prod,Office-Dev" || u.CycleErr != nil {
		t.Errorf("order %s, CycleErr %v", names(u), u.CycleErr)
	}
	if !u.LastRefresh.Equal(base.Add(4 * time.Hour)) {
		t.Errorf("LastRefresh = %v", u.LastRefresh)
	}

	r.prov.set(func(p *testProv) { p.Provider.FailAccount = "" })
	r.clk.advance(4 * time.Hour)
	r.tick()
	if dev := r.snap(r.last(), "Office-Dev"); dev.Stale || dev.Err != "" {
		t.Errorf("Dev should recover: %+v", dev)
	}
}

// An account that has never succeeded has nothing to keep, but it must not just vanish (a
// resource group you cannot read would simply disappear from the menu): it shows up as a
// placeholder carrying its error, and is replaced by real data once a refresh succeeds.
func TestAccountThatHasNeverSucceededShowsAsAPlaceholder(t *testing.T) {
	r := newRig(t)
	r.prov.set(func(p *testProv) { p.Provider.FailAccount = "Office-Dev" })
	r.start()

	u := r.last()
	if names(u) != "Office-Prod,Office-Dev" {
		t.Fatalf("snapshots = %s", names(u))
	}
	dev := r.snap(u, "Office-Dev")
	if !dev.Stale || !strings.Contains(dev.Err, "simulated failure") || !dev.FetchedAt.IsZero() || dev.MonthToDate != 0 || dev.Currency != "" {
		t.Errorf("the placeholder should be stale, carry the error and no data: %+v", dev)
	}
	if r.snap(u, "Office-Prod").Stale {
		t.Error("the other account must be unaffected")
	}
	if !u.LastRefresh.Equal(base) {
		t.Errorf("a placeholder must not count as fetched data: LastRefresh = %v", u.LastRefresh)
	}

	r.prov.set(func(p *testProv) { p.Provider.FailAccount = "" })
	r.clk.advance(4 * time.Hour)
	r.tick()
	if dev := r.snap(r.last(), "Office-Dev"); dev.Stale || dev.Err != "" || dev.MonthToDate != 181.80 {
		t.Errorf("the next refresh should replace the placeholder: %+v", dev)
	}
}

func TestRefreshNowIsDebounced(t *testing.T) {
	r := newRig(t)
	r.start()
	updates, calls := len(r.updates), r.prov.callCount("Office-Prod")

	r.clk.advance(4*time.Minute + 59*time.Second)
	r.refreshNow()
	if len(r.updates) != updates || r.prov.callCount("Office-Prod") != calls {
		t.Error("Refresh Now within five minutes of the last refresh must be ignored")
	}

	r.clk.advance(2 * time.Second)
	r.refreshNow()
	if r.prov.callCount("Office-Prod") != calls+1 {
		t.Error("Refresh Now after five minutes should refresh")
	}
	if got := len(r.updates) - updates; got != 2 { // "refreshing", then the result
		t.Errorf("published %d updates, want 2", got)
	}
	if !r.updates[updates].Refreshing || r.last().Refreshing {
		t.Error("expected a Refreshing update followed by a final one")
	}
}

func TestRefreshNowRightAfterAFailedSignInIsNotDebounced(t *testing.T) {
	r := newRig(t)
	r.prov.accountsErr = fmt.Errorf("%w: signed out", provider.ErrNotAuthorized)
	r.start()
	if r.last().CycleErr == nil {
		t.Fatal("setup: expected a sign-in failure")
	}

	r.prov.set(func(p *testProv) { p.accountsErr = nil }) // the user ran az login
	r.clk.advance(10 * time.Second)
	r.refreshNow()
	if u := r.last(); u.CycleErr != nil || len(u.Snapshots) != 2 {
		t.Errorf("Refresh Now should recover straight away: %+v", u)
	}
}

func TestScheduledRefreshFollowsTheWallClock(t *testing.T) {
	r := newRig(t)
	r.start()
	calls := r.prov.callCount("Office-Prod")

	r.clk.advance(3 * time.Hour)
	r.tick()
	if r.prov.callCount("Office-Prod") != calls {
		t.Fatal("refreshed before the interval elapsed")
	}

	r.clk.advance(6 * time.Hour) // the Mac slept through the night
	r.tick()
	if r.prov.callCount("Office-Prod") != calls+1 {
		t.Fatal("a 6 hour clock jump should trigger a refresh on the next tick")
	}
	u := r.last()
	if !u.LastRefresh.Equal(base.Add(9*time.Hour)) || !u.NextRefresh.Equal(base.Add(13*time.Hour)) {
		t.Errorf("last %v next %v", u.LastRefresh, u.NextRefresh)
	}
}

// A Go time with a monotonic reading ("m=+..." in String) does not count the time the Mac
// spends asleep, so comparing two such times after a night's sleep would say a few minutes.
func TestDefaultClockHasNoMonotonicReading(t *testing.T) {
	now := New(Options{}).now()
	if strings.Contains(now.String(), "m=") {
		t.Errorf("default clock returns %q; it must be wall-clock only", now)
	}
}

func TestSignInFailureMidCycleAbortsAndMarksEverythingStale(t *testing.T) {
	r := newRig(t)
	r.start()
	saves := r.cache.saveCount()
	r.clk.advance(4 * time.Hour)
	r.prov.set(func(p *testProv) { p.snapshotErr["Office-Dev"] = fmt.Errorf("%w: expired", provider.ErrNotAuthorized) })
	r.tick()

	u := r.last()
	if !errors.Is(u.CycleErr, provider.ErrNotAuthorized) {
		t.Fatalf("CycleErr = %v", u.CycleErr)
	}
	for _, s := range u.Snapshots {
		if !s.Stale || !strings.Contains(s.Err, "expired") || !s.FetchedAt.Equal(base) {
			t.Errorf("%s should keep old data, stale: %+v", s.Account.Name, s)
		}
	}
	if !u.LastRefresh.Equal(base) {
		t.Errorf("LastRefresh moved to %v although nothing was refreshed", u.LastRefresh)
	}
	if r.cache.saveCount() != saves {
		t.Error("an aborted cycle must not rewrite the cache")
	}

	r.prov.set(func(p *testProv) { delete(p.snapshotErr, "Office-Dev") })
	r.clk.advance(time.Minute)
	r.refreshNow()
	if u := r.last(); u.CycleErr != nil || r.snap(u, "Office-Dev").Stale {
		t.Errorf("signing in again should clear the error: %+v", u.CycleErr)
	}
}

func TestOtherAccountListFailuresMarkStaleWithoutACycleError(t *testing.T) {
	r := newRig(t)
	r.start()
	saves := r.cache.saveCount()
	r.clk.advance(4 * time.Hour)
	r.prov.set(func(p *testProv) { p.accountsErr = errors.New("network is down") })
	r.tick()

	u := r.last()
	if u.CycleErr != nil {
		t.Errorf("CycleErr = %v; only sign-in problems belong there", u.CycleErr)
	}
	for _, s := range u.Snapshots {
		if !s.Stale || s.Err != "network is down" {
			t.Errorf("%+v", s)
		}
	}
	if r.cache.saveCount() != saves {
		t.Error("nothing new to cache")
	}
}

func TestAccountsThatDisappearAreDroppedAndTruncationIsReported(t *testing.T) {
	r := newRig(t)
	r.start()
	r.clk.advance(4 * time.Hour)
	r.prov.set(func(p *testProv) { p.hide["Office-Dev"] = true; p.truncated = 2 })
	r.tick()

	u := r.last()
	if names(u) != "Office-Prod" || u.Truncated != 2 {
		t.Errorf("snapshots %s, truncated %d", names(u), u.Truncated)
	}
}

func TestCancelledContextLeavesTheStateAlone(t *testing.T) {
	r := newRig(t)
	r.start()
	saves, calls := r.cache.saveCount(), r.prov.callCount("Office-Prod")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.ctx = ctx
	r.clk.advance(4 * time.Hour)
	r.tick()

	if r.cache.saveCount() != saves || r.prov.callCount("Office-Prod") != calls {
		t.Error("a cancelled refresh must not fetch or save")
	}
	if u := r.last(); u.CycleErr != nil {
		t.Errorf("cancellation is not a cycle error: %v", u.CycleErr)
	}
}

func waitFor(t *testing.T, ch <-chan Update, what string, pred func(Update) bool) Update {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case u := <-ch:
			if pred(u) {
				return u
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func TestRunLoop(t *testing.T) {
	clk := &clock{t: base}
	cache := &memCache{}
	ticks := make(chan time.Time)
	s := New(Options{Provider: newProv(), Cache: cache, Now: clk.Now, Tick: ticks})
	updates := make(chan Update, 100)
	s.Subscribe(func(u Update) { updates <- u })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	waitFor(t, updates, "the first fetch", func(u Update) bool { return !u.Refreshing && len(u.Snapshots) == 2 })

	clk.advance(4 * time.Hour)
	ticks <- clk.Now()
	u := waitFor(t, updates, "a scheduled refresh", func(u Update) bool {
		return !u.Refreshing && len(u.Snapshots) == 2 && u.Snapshots[0].FetchedAt.Equal(base.Add(4*time.Hour))
	})
	if !u.LastRefresh.Equal(base.Add(4 * time.Hour)) {
		t.Errorf("LastRefresh = %v", u.LastRefresh)
	}

	clk.advance(10 * time.Minute)
	s.RefreshNow()
	s.RefreshNow() // a second request while one is queued is dropped, not blocked
	waitFor(t, updates, "Refresh Now", func(u Update) bool {
		return !u.Refreshing && len(u.Snapshots) == 2 && u.Snapshots[0].FetchedAt.Equal(base.Add(4*time.Hour+10*time.Minute))
	})

	clk.advance(time.Minute)
	s.Apply() // inside the debounce window, so Refresh Now would have been ignored
	waitFor(t, updates, "Apply", func(u Update) bool {
		return !u.Refreshing && len(u.Snapshots) == 2 && u.Snapshots[0].FetchedAt.Equal(base.Add(4*time.Hour+11*time.Minute))
	})

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
}

func labels(accounts []provider.Account) string {
	var out []string
	for _, a := range accounts {
		out = append(out, a.Label())
	}
	return strings.Join(out, ",")
}

func snapLabels(u Update) string {
	var out []string
	for _, s := range u.Snapshots {
		out = append(out, s.Account.Label())
	}
	return strings.Join(out, ",")
}

func (r *rig) subID(name string) string {
	r.t.Helper()
	subs, _ := r.prov.Provider.Subscriptions(r.ctx)
	for _, s := range subs {
		if s.Name == name {
			return s.ID
		}
	}
	r.t.Fatalf("no subscription called %s", name)
	return ""
}

func (r *rig) track(scopes ...config.Subscription) {
	r.cfg.Azure.Subscriptions = config.Subscriptions{List: scopes}
}

func TestTrackedAndAvailableArePublished(t *testing.T) {
	r := newRig(t)
	r.start()
	u := r.last()
	if labels(u.Tracked) != "Office-Prod,Office-Dev" || labels(u.Available) != "Office-Prod,Office-Dev" {
		t.Errorf("tracked %q, available %q", labels(u.Tracked), labels(u.Available))
	}
}

func TestTrackedScopesAreKnownFromTheCacheBeforeTheFirstRefresh(t *testing.T) {
	r := newRig(t)
	r.cache.snaps = fakeAt(t, base.Add(-time.Hour))
	r.start()
	if len(r.updates) != 1 {
		t.Fatalf("updates = %d", len(r.updates))
	}
	u := r.last()
	if labels(u.Tracked) != "Office-Prod,Office-Dev" || len(u.Available) != 0 {
		t.Errorf("tracked %q, available %q; the menu needs the tracked scopes at once", labels(u.Tracked), labels(u.Available))
	}
}

func TestAvailableSurvivesAListingFailure(t *testing.T) {
	r := newRig(t)
	r.start()
	r.clk.advance(4 * time.Hour)
	r.prov.set(func(p *testProv) { p.subsErr = errors.New("listing failed") })
	r.tick()

	u := r.last()
	if labels(u.Available) != "Office-Prod,Office-Dev" {
		t.Errorf("available = %q, want the previous list", labels(u.Available))
	}
	if !r.snap(u, "Office-Prod").FetchedAt.Equal(base.Add(4 * time.Hour)) {
		t.Error("a failed listing must not stop the refresh")
	}
}

func TestScopesOfOneSubscriptionDoNotCollide(t *testing.T) {
	r := newRig(t)
	prod := r.subID("Office-Prod")
	r.track(config.Subscription{ID: prod}, config.Subscription{ID: prod, ResourceGroup: "rg-web"})
	r.start()

	u := r.last()
	if snapLabels(u) != "Office-Prod,Office-Prod / rg-web" || u.Snapshots[0].MonthToDate != 1102.40 || u.Snapshots[1].MonthToDate != 600 {
		t.Fatalf("snapshots = %s", snapLabels(u))
	}

	// One scope fails: only that one goes stale, and it keeps its own old numbers.
	r.clk.advance(4 * time.Hour)
	r.prov.set(func(p *testProv) { p.snapshotErr["Office-Prod / rg-web"] = errors.New("boom") })
	r.tick()
	u = r.last()
	whole, group := u.Snapshots[0], u.Snapshots[1]
	if whole.Stale || !whole.FetchedAt.Equal(base.Add(4*time.Hour)) {
		t.Errorf("the whole subscription must be unaffected: %+v", whole)
	}
	if !group.Stale || group.MonthToDate != 600 || !group.FetchedAt.Equal(base) {
		t.Errorf("the resource group should keep its own old data, stale: %+v", group)
	}
}

func TestSwitchingAScopeReplacesItsSnapshot(t *testing.T) {
	r := newRig(t)
	r.start()
	prod, dev := r.subID("Office-Prod"), r.subID("Office-Dev")

	r.track(config.Subscription{ID: prod, ResourceGroup: "rg-web"}, config.Subscription{ID: dev})
	r.apply()

	u := r.last()
	if snapLabels(u) != "Office-Prod / rg-web,Office-Dev" || labels(u.Tracked) != "Office-Prod / rg-web,Office-Dev" {
		t.Errorf("snapshots %q, tracked %q", snapLabels(u), labels(u.Tracked))
	}
	if u.Snapshots[0].MonthToDate != 600 {
		t.Errorf("month to date = %.2f, want the resource group's 600", u.Snapshots[0].MonthToDate)
	}
	if labels(u.Available) != "Office-Prod,Office-Dev" {
		t.Errorf("the picker must still see whole subscriptions: %q", labels(u.Available))
	}
}

func TestStoppingTrackingASubscriptionDropsIt(t *testing.T) {
	r := newRig(t)
	r.start()

	r.track(config.Subscription{ID: r.subID("Office-Prod")})
	r.apply()
	devCalls := r.prov.callCount("Office-Dev")

	r.clk.advance(5 * time.Hour)
	r.tick()
	if snapLabels(r.last()) != "Office-Prod" || r.prov.callCount("Office-Dev") != devCalls {
		t.Errorf("snapshots %q; Dev (calls %d->%d) must be gone and not fetched any more", snapLabels(r.last()), devCalls, r.prov.callCount("Office-Dev"))
	}
}

func TestApplyIgnoresTheDebounceThatRefreshNowObeys(t *testing.T) {
	r := newRig(t)
	r.start()
	calls := r.prov.callCount("Office-Prod")

	r.clk.advance(time.Minute)
	r.refreshNow()
	if r.prov.callCount("Office-Prod") != calls {
		t.Fatal("setup: Refresh Now should have been debounced")
	}
	r.apply()
	if r.prov.callCount("Office-Prod") != calls+1 {
		t.Error("Apply must refresh straight away")
	}
}

func TestReloadRedrawsWithTheNewSettingsAndFetchesNothing(t *testing.T) {
	r := newRig(t)
	r.start()
	calls, updates := r.prov.callCount("Office-Prod"), len(r.updates)

	r.cfg.Refresh = 2 * time.Hour
	r.cfg.Azure.MaxServices = 3
	r.s.onReload()

	if len(r.updates) != updates+1 {
		t.Fatalf("a reload should publish exactly once, published %d", len(r.updates)-updates)
	}
	u := r.last()
	if u.Config.Refresh != 2*time.Hour || u.Config.Azure.MaxServices != 3 || u.Refreshing {
		t.Errorf("config %+v, refreshing %v", u.Config, u.Refreshing)
	}
	if !u.NextRefresh.Equal(base.Add(2 * time.Hour)) {
		t.Errorf("NextRefresh = %v; it must follow the new interval", u.NextRefresh)
	}
	if r.prov.callCount("Office-Prod") != calls {
		t.Error("changing a setting must not fetch from Azure")
	}
}

func TestShorteningTheIntervalMakesTheNextTickRefresh(t *testing.T) {
	r := newRig(t)
	r.start()
	calls := r.prov.callCount("Office-Prod")

	r.clk.advance(90 * time.Minute)
	r.tick()
	if r.prov.callCount("Office-Prod") != calls {
		t.Fatal("setup: nothing is due yet")
	}
	r.cfg.Refresh = time.Hour
	r.s.onReload()
	r.tick()
	if r.prov.callCount("Office-Prod") != calls+1 {
		t.Error("with a 1 hour interval, 90 minutes old data is due")
	}
}

func TestReloadReportsAnInvalidSettingsFile(t *testing.T) {
	r := newRig(t)
	r.start()
	r.cfg, r.cfgErr = config.Config{}, errors.New("refresh must be at least 1h0m0s, got 5m0s")
	r.s.onReload()
	if u := r.last(); u.ConfigErr == "" || u.Config.Refresh != 4*time.Hour {
		t.Errorf("ConfigErr %q, config kept = %v", u.ConfigErr, u.Config.Refresh)
	}
}

func TestRunLoopReload(t *testing.T) {
	clk := &clock{t: base}
	var minutes atomic.Int64
	minutes.Store(240)
	s := New(Options{
		Provider: newProv(), Cache: &memCache{}, Now: clk.Now, Tick: make(chan time.Time),
		LoadConfig: func() (config.Config, error) {
			cfg := config.Default()
			cfg.Refresh = time.Duration(minutes.Load()) * time.Minute
			return cfg, nil
		},
	})
	updates := make(chan Update, 100)
	s.Subscribe(func(u Update) { updates <- u })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	waitFor(t, updates, "the first fetch", func(u Update) bool { return !u.Refreshing && len(u.Snapshots) == 2 })

	minutes.Store(120)
	s.Reload()
	waitFor(t, updates, "the reload", func(u Update) bool { return u.Config.Refresh == 2*time.Hour && !u.Refreshing })
}
