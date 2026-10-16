package settings

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"costmeter/internal/config"
	"costmeter/internal/provider"
)

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

func newStore(t *testing.T) *Store { return New(filepath.Join(t.TempDir(), "nested", "settings.json")) }

var sample = []Choice{
	{Subscription: "aaa", Name: "Office-Prod", ResourceGroup: "rg-web"},
	{Subscription: "bbb", Name: "Office-Dev"},
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	s := newStore(t)
	want := Settings{RefreshMinutes: 120, MaxServices: 3, Scopes: &sample}
	if err := s.Update(func(cur *Settings) error { *cur = want; return nil }); err != nil {
		t.Fatal(err)
	}
	if got := s.Load(); !reflect.DeepEqual(got, want) {
		t.Errorf("Load = %+v, want %+v", got, want)
	}
}

func TestNothingSavedMeansTheDefaults(t *testing.T) {
	logs := captureLog(t)
	got := newStore(t).Load()
	if !reflect.DeepEqual(got, Settings{}) || got.Scopes != nil {
		t.Errorf("Load = %+v", got)
	}
	if logs.Len() != 0 {
		t.Errorf("a missing file is normal but it logged: %s", logs)
	}
}

func TestSavingNoScopesIsDifferentFromNeverChoosing(t *testing.T) {
	s := newStore(t)
	if err := s.Update(func(cur *Settings) error { cur.Scopes = &[]Choice{}; return nil }); err != nil {
		t.Fatal(err)
	}
	got := s.Load()
	if got.Scopes == nil || len(*got.Scopes) != 0 {
		t.Errorf("Scopes = %v; \"track nothing\" must survive a round trip", got.Scopes)
	}
}

func TestUnreadableFilesGiveTheDefaultsWithALogLine(t *testing.T) {
	tests := []struct{ name, content, wantLog string }{
		{"corrupt", `{"v":1,"scopes":[{"sub`, "corrupt"},
		{"other version", `{"v":7,"refreshMinutes":60}`, "version 7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureLog(t)
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := New(path).Load(); !reflect.DeepEqual(got, Settings{}) {
				t.Errorf("Load = %+v, want the defaults", got)
			}
			if !strings.Contains(logs.String(), tt.wantLog) {
				t.Errorf("log = %q, want it to mention %q", logs, tt.wantLog)
			}
		})
	}
}

func TestUpdateChangesOneSettingAndKeepsTheRest(t *testing.T) {
	s := newStore(t)
	if err := s.Update(func(cur *Settings) error { cur.RefreshMinutes = 480; cur.Scopes = &sample; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(cur *Settings) error { cur.MaxServices = 4; return nil }); err != nil {
		t.Fatal(err)
	}
	got := s.Load()
	if got.RefreshMinutes != 480 || got.MaxServices != 4 || got.Scopes == nil || len(*got.Scopes) != 2 {
		t.Errorf("Load = %+v; every setting must survive a change to another", got)
	}
}

func TestAFailingUpdateSavesNothing(t *testing.T) {
	s := newStore(t)
	if err := s.Update(func(cur *Settings) error { cur.RefreshMinutes = 120; return nil }); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	if err := s.Update(func(cur *Settings) error { cur.RefreshMinutes = 999; return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if got := s.Load().RefreshMinutes; got != 120 {
		t.Errorf("RefreshMinutes = %d, want the earlier 120", got)
	}
}

func TestConcurrentUpdatesAllSurvive(t *testing.T) {
	s := newStore(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.Update(func(cur *Settings) error {
				var choices []Choice
				if cur.Scopes != nil {
					choices = *cur.Scopes
				}
				choices, err := Set(choices, Choice{Subscription: fmt.Sprintf("sub-%d", i)})
				cur.Scopes = &choices
				return err
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := s.Load(); got.Scopes == nil || len(*got.Scopes) != 8 {
		t.Errorf("Scopes = %v; two clicks at once must not overwrite each other", got.Scopes)
	}
}

func TestApplyLaysTheChoicesOverTheConfig(t *testing.T) {
	cfg := config.Default()
	got := Apply(cfg, Settings{RefreshMinutes: 120, MaxServices: 3, Scopes: &sample})

	if got.Refresh != 2*time.Hour || got.Azure.MaxServices != 3 {
		t.Errorf("refresh %v, services %d", got.Refresh, got.Azure.MaxServices)
	}
	want := []config.Subscription{
		{ID: "aaa", Name: "Office-Prod", ResourceGroup: "rg-web"},
		{ID: "bbb", Name: "Office-Dev"},
	}
	if subs := got.Azure.Subscriptions; subs.Auto || !reflect.DeepEqual(subs.List, want) {
		t.Errorf("subscriptions = %+v, want the explicit list %+v", subs, want)
	}
	if !cfg.Azure.Subscriptions.Auto || cfg.Refresh != 4*time.Hour {
		t.Error("Apply modified its argument")
	}
	if err := got.Validate(); err != nil {
		t.Errorf("the result must be valid: %v", err)
	}
}

func TestApplyWithNothingSavedChangesNothing(t *testing.T) {
	cfg := config.Default()
	if got := Apply(cfg, Settings{}); !reflect.DeepEqual(got, cfg) {
		t.Errorf("got %+v, want the defaults %+v", got, cfg)
	}
}

func TestApplyWithNoScopesTracksNothing(t *testing.T) {
	got := Apply(config.Default(), Settings{Scopes: &[]Choice{}})
	if got.Azure.Subscriptions.Auto || len(got.Azure.Subscriptions.List) != 0 {
		t.Errorf("subscriptions = %+v", got.Azure.Subscriptions)
	}
}

func TestAHandEditedFileCannotSlipPastValidation(t *testing.T) {
	for name, s := range map[string]Settings{
		"refresh too short":  {RefreshMinutes: 5},
		"too many services":  {MaxServices: 9},
		"duplicate scopes":   {Scopes: &[]Choice{{Subscription: "aaa"}, {Subscription: "AAA", ResourceGroup: "rg"}}},
		"a scope with no id": {Scopes: &[]Choice{{Name: "x"}}},
		"eleven subscriptions": {Scopes: func() *[]Choice {
			c := make([]Choice, 11)
			for i := range c {
				c[i].Subscription = fmt.Sprint(i)
			}
			return &c
		}()},
	} {
		if err := Apply(config.Default(), s).Validate(); err == nil {
			t.Errorf("%s: expected Validate to reject the result", name)
		}
	}
}

func TestTheMenuOnlyOffersValidChoices(t *testing.T) {
	for i, d := range RefreshOptions {
		cfg := config.Default()
		cfg.Refresh = d
		if err := cfg.Validate(); err != nil {
			t.Errorf("refresh option %v is not valid: %v", d, err)
		}
		if i > 0 && d <= RefreshOptions[i-1] {
			t.Errorf("refresh options must be ascending: %v after %v", d, RefreshOptions[i-1])
		}
	}
	for _, n := range ServicesOptions {
		cfg := config.Default()
		cfg.Azure.MaxServices = n
		if err := cfg.Validate(); err != nil {
			t.Errorf("services option %d is not valid: %v", n, err)
		}
	}
	if !containsDuration(RefreshOptions, config.Default().Refresh) || !containsInt(ServicesOptions, config.Default().Azure.MaxServices) {
		t.Error("the defaults must be among the choices, so one is always checked")
	}
}

func containsDuration(list []time.Duration, d time.Duration) bool {
	for _, x := range list {
		if x == d {
			return true
		}
	}
	return false
}

func containsInt(list []int, n int) bool {
	for _, x := range list {
		if x == n {
			return true
		}
	}
	return false
}

func TestSetReplacesBySubscriptionIgnoringCase(t *testing.T) {
	orig := append([]Choice(nil), sample...)
	got, err := Set(sample, Choice{Subscription: "AAA", Name: "Office-Prod", ResourceGroup: ""})
	if err != nil {
		t.Fatal(err)
	}
	want := []Choice{{Subscription: "AAA", Name: "Office-Prod"}, sample[1]}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if !reflect.DeepEqual(sample, orig) {
		t.Error("Set modified its argument")
	}
}

func TestSetAddsNewSubscriptionsAtTheEnd(t *testing.T) {
	got, err := Set(sample, Choice{Subscription: "ccc", Name: "Office-QA", ResourceGroup: "rg-qa"})
	if err != nil || len(got) != 3 || got[2].Subscription != "ccc" {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestSetRefusesAnEleventhSubscription(t *testing.T) {
	var ten []Choice
	for i := 0; i < MaxTracked; i++ {
		ten = append(ten, Choice{Subscription: fmt.Sprintf("sub-%02d", i)})
	}
	if _, err := Set(ten, Choice{Subscription: "one-too-many"}); !errors.Is(err, ErrTooMany) {
		t.Errorf("err = %v, want ErrTooMany", err)
	}
	if got, err := Set(ten, Choice{Subscription: "sub-03", ResourceGroup: "rg"}); err != nil || got[3].ResourceGroup != "rg" {
		t.Errorf("changing an existing one at the limit must work: %v", err)
	}
}

func TestRemove(t *testing.T) {
	got := Remove(sample, "AAA")
	if len(got) != 1 || got[0].Subscription != "bbb" {
		t.Errorf("got %+v", got)
	}
	if len(Remove(nil, "x")) != 0 {
		t.Error("removing from nothing")
	}
}

func TestFromAccounts(t *testing.T) {
	got := FromAccounts([]provider.Account{
		{Provider: "azure", ID: "aaa", Name: "Office-Prod", ResourceGroup: "rg-web"},
		{Provider: "azure", ID: "bbb", Name: "Office-Dev"},
	})
	if !reflect.DeepEqual(got, sample) {
		t.Errorf("got %+v, want %+v", got, sample)
	}
}
