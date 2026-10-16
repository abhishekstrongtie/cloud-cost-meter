package main

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"costmeter/internal/provider"
	"costmeter/internal/settings"
	"costmeter/internal/ui"
)

var (
	prod = provider.Account{Provider: "azure", ID: "aaa", Name: "Office-Prod"}
	dev  = provider.Account{Provider: "azure", ID: "bbb", Name: "Office-Dev"}
)

type rig struct {
	t       *testing.T
	store   *settings.Store
	tracked []provider.Account
	applied int // fetches asked for
	reloads int // redraws asked for
	c       *chooser
}

func newRig(t *testing.T, tracked ...provider.Account) *rig {
	t.Helper()
	r := &rig{t: t, store: settings.New(filepath.Join(t.TempDir(), "settings.json")), tracked: tracked}
	r.c = newChooser(r.store, func() []provider.Account { return r.tracked }, func() { r.applied++ }, func() { r.reloads++ })
	return r
}

func (r *rig) scopes() []settings.Choice {
	r.t.Helper()
	s := r.store.Load().Scopes
	if s == nil {
		r.t.Fatal("no scopes are saved")
	}
	return *s
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

func TestFirstPickStartsFromWhatIsTracked(t *testing.T) {
	r := newRig(t, prod, dev)
	if r.c.Saved() {
		t.Fatal("nothing is saved yet")
	}

	r.c.Pick(ui.Change{Subscription: "aaa", Name: "Office-Prod", ResourceGroup: "rg-web"})

	want := []settings.Choice{
		{Subscription: "aaa", Name: "Office-Prod", ResourceGroup: "rg-web"},
		{Subscription: "bbb", Name: "Office-Dev"},
	}
	if got := r.scopes(); !reflect.DeepEqual(got, want) {
		t.Errorf("saved %+v, want %+v", got, want)
	}
	if !r.c.Saved() || r.applied != 1 || r.reloads != 0 {
		t.Errorf("saved=%v applied=%d reloads=%d; a new scope needs a fetch", r.c.Saved(), r.applied, r.reloads)
	}
}

func TestTwoQuickPicksBothSurvive(t *testing.T) {
	// The scheduler has not caught up yet, so `tracked` still shows the old scopes.
	r := newRig(t, prod, dev)
	r.c.Pick(ui.Change{Subscription: "aaa", Name: "Office-Prod", ResourceGroup: "rg-web"})
	r.c.Pick(ui.Change{Subscription: "bbb", Name: "Office-Dev", ResourceGroup: "rg-dev-apps"})

	want := []settings.Choice{
		{Subscription: "aaa", Name: "Office-Prod", ResourceGroup: "rg-web"},
		{Subscription: "bbb", Name: "Office-Dev", ResourceGroup: "rg-dev-apps"},
	}
	if got := r.scopes(); !reflect.DeepEqual(got, want) {
		t.Errorf("saved %+v, want %+v; the second pick must not undo the first", got, want)
	}
	if r.applied != 2 {
		t.Errorf("applied = %d", r.applied)
	}
}

func TestPickingTheWholeSubscriptionAgain(t *testing.T) {
	r := newRig(t, prod, dev)
	r.c.Pick(ui.Change{Subscription: "aaa", Name: "Office-Prod", ResourceGroup: "rg-web"})
	r.c.Pick(ui.Change{Subscription: "aaa", Name: "Office-Prod"})
	if got := r.scopes()[0]; got.ResourceGroup != "" {
		t.Errorf("Prod = %+v, want the whole subscription again", got)
	}
}

func TestNotTrackedRemovesTheSubscription(t *testing.T) {
	r := newRig(t, prod, dev)
	r.c.Pick(ui.Change{Subscription: "bbb", Name: "Office-Dev", Remove: true})
	if got := r.scopes(); len(got) != 1 || got[0].Subscription != "aaa" {
		t.Errorf("saved %+v", got)
	}

	// Stopping to track everything is a real choice, not "nothing chosen".
	r.c.Pick(ui.Change{Subscription: "aaa", Remove: true})
	if s := r.store.Load().Scopes; s == nil || len(*s) != 0 {
		t.Errorf("Scopes = %v; want an empty, saved list", s)
	}
}

func TestTrackingAnEleventhSubscriptionIsRefused(t *testing.T) {
	var ten []provider.Account
	for i := 0; i < settings.MaxTracked; i++ {
		ten = append(ten, provider.Account{ID: fmt.Sprintf("sub-%02d", i), Name: "S"})
	}
	r := newRig(t, ten...)
	logs := captureLog(t)

	r.c.Pick(ui.Change{Subscription: "one-too-many", Name: "Extra"})

	if r.store.Load().Scopes != nil || r.c.Saved() || r.applied != 0 {
		t.Errorf("a refused pick must change nothing: saved=%v applied=%d", r.c.Saved(), r.applied)
	}
	if !strings.Contains(logs.String(), "at most 10") {
		t.Errorf("log = %q", logs.String())
	}
}

func TestAFailedSaveChangesNothing(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var applied, reloaded int
	c := newChooser(settings.New(filepath.Join(blocker, "settings.json")),
		func() []provider.Account { return []provider.Account{prod} }, func() { applied++ }, func() { reloaded++ })
	logs := captureLog(t)

	c.Pick(ui.Change{Subscription: "aaa", Name: "Office-Prod", ResourceGroup: "rg-web"})
	c.SetRefresh(2 * time.Hour)
	c.SetServices(3)
	if c.Saved() || applied != 0 || reloaded != 0 {
		t.Errorf("saved=%v applied=%d reloaded=%d; a failed save must change nothing", c.Saved(), applied, reloaded)
	}
	for _, want := range []string{"cannot change the scopes", "cannot save the refresh interval", "cannot save the services setting"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %q", want)
		}
	}
}

func TestResetTracksEverySubscriptionAgain(t *testing.T) {
	r := newRig(t, prod, dev)
	r.c.Pick(ui.Change{Subscription: "aaa", Name: "Office-Prod", ResourceGroup: "rg-web"})
	r.applied = 0

	r.c.Reset()
	if r.store.Load().Scopes != nil || r.c.Saved() || r.applied != 1 {
		t.Errorf("after Reset: scopes=%v saved=%v applied=%d", r.store.Load().Scopes, r.c.Saved(), r.applied)
	}
}

func TestScopesSavedByAnEarlierRunAreKnownAtStart(t *testing.T) {
	store := settings.New(filepath.Join(t.TempDir(), "settings.json"))
	if err := store.Update(func(cur *settings.Settings) error { cur.Scopes = &[]settings.Choice{{Subscription: "aaa"}}; return nil }); err != nil {
		t.Fatal(err)
	}
	if c := newChooser(store, func() []provider.Account { return nil }, func() {}, func() {}); !c.Saved() {
		t.Error("Saved should be true so the menu offers to reset it")
	}
}

func TestRefreshAndServicesNeedOnlyARedraw(t *testing.T) {
	r := newRig(t, prod, dev)
	r.c.SetRefresh(8 * time.Hour)
	r.c.SetServices(3)

	got := r.store.Load()
	if got.RefreshMinutes != 480 || got.MaxServices != 3 {
		t.Errorf("saved %+v", got)
	}
	if r.reloads != 2 || r.applied != 0 {
		t.Errorf("reloads=%d applied=%d; these settings need no new data from Azure", r.reloads, r.applied)
	}
}

func TestSettingsDoNotDisturbEachOther(t *testing.T) {
	r := newRig(t, prod, dev)
	r.c.Pick(ui.Change{Subscription: "aaa", Name: "Office-Prod", ResourceGroup: "rg-web"})
	r.c.SetRefresh(2 * time.Hour)
	r.c.SetServices(4)
	r.c.Pick(ui.Change{Subscription: "bbb", Name: "Office-Dev", Remove: true})

	got := r.store.Load()
	if got.RefreshMinutes != 120 || got.MaxServices != 4 || got.Scopes == nil || len(*got.Scopes) != 1 || (*got.Scopes)[0].ResourceGroup != "rg-web" {
		t.Errorf("saved %+v; each change must keep the others", got)
	}
}

func TestConfigLoaderUsesTheDefaultsUntilSomethingIsChosen(t *testing.T) {
	store := settings.New(filepath.Join(t.TempDir(), "settings.json"))
	cfg, err := configLoader(store)()
	if err != nil || cfg.Refresh != 4*time.Hour || cfg.Azure.MaxServices != 5 || !cfg.Azure.Subscriptions.Auto {
		t.Fatalf("defaults: %+v, %v", cfg, err)
	}

	load := configLoader(store) // re-read on every call, like the scheduler does
	if err := store.Update(func(cur *settings.Settings) error {
		cur.RefreshMinutes, cur.MaxServices = 120, 3
		cur.Scopes = &[]settings.Choice{{Subscription: "aaa", Name: "Office-Prod", ResourceGroup: "rg-web"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err = load()
	if err != nil {
		t.Fatal(err)
	}
	subs := cfg.Azure.Subscriptions
	if cfg.Refresh != 2*time.Hour || cfg.Azure.MaxServices != 3 || subs.Auto || len(subs.List) != 1 || subs.List[0].ResourceGroup != "rg-web" {
		t.Errorf("the saved choices should be applied: %+v", cfg)
	}
}

func TestConfigLoaderRejectsInvalidSavedSettings(t *testing.T) {
	for name, edit := range map[string]func(*settings.Settings){
		"refresh too short": func(s *settings.Settings) { s.RefreshMinutes = 5 },
		"duplicate scopes": func(s *settings.Settings) {
			s.Scopes = &[]settings.Choice{{Subscription: "aaa"}, {Subscription: "AAA", ResourceGroup: "rg"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := settings.New(filepath.Join(t.TempDir(), "settings.json"))
			if err := store.Update(func(cur *settings.Settings) error { edit(cur); return nil }); err != nil { // as if edited by hand
				t.Fatal(err)
			}
			if _, err := configLoader(store)(); err == nil || !strings.Contains(err.Error(), "saved settings") {
				t.Errorf("err = %v", err)
			}
		})
	}
}
