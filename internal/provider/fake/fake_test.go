package fake

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"costmeter/internal/config"
	"costmeter/internal/provider"
)

var now = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func near(a, b float64) bool { return math.Abs(a-b) < 0.005 }

func snapshots(t *testing.T, p *Provider) []provider.Snapshot {
	t.Helper()
	ctx := context.Background()
	accts, truncated, err := p.Accounts(ctx)
	if err != nil || truncated != 0 || len(accts) != 2 {
		t.Fatalf("Accounts = %v, %d, %v", accts, truncated, err)
	}
	var out []provider.Snapshot
	for _, a := range accts {
		s, err := p.Snapshot(ctx, a, now)
		if err != nil {
			t.Fatalf("Snapshot(%s): %v", a.Name, err)
		}
		out = append(out, s)
	}
	return out
}

func TestMockupNumbers(t *testing.T) {
	snaps := snapshots(t, New())

	var mtd, last, forecast, budget float64
	for _, s := range snaps {
		mtd += s.MonthToDate
		last += s.LastMonthToDate
		forecast += *s.ForecastMonth
		budget += s.Budget.Amount
		if s.Currency != "USD" || !s.FetchedAt.Equal(now) {
			t.Errorf("%s: currency %q, fetched %v", s.Account.Name, s.Currency, s.FetchedAt)
		}
	}
	for name, c := range map[string]struct{ got, want float64 }{
		"mtd": {mtd, 1284.20}, "last month": {last, 1146.61}, "forecast": {forecast, 2450}, "budget": {budget, 2200},
	} {
		if !near(c.got, c.want) {
			t.Errorf("%s = %.2f, want %.2f", name, c.got, c.want)
		}
	}
}

func TestSnapshotsAreInternallyConsistent(t *testing.T) {
	for _, s := range snapshots(t, New()) {
		var services float64
		for _, sc := range s.ByService {
			services += sc.Amount
		}
		if !near(services, s.MonthToDate) {
			t.Errorf("%s: services sum %.2f != MTD %.2f", s.Account.Name, services, s.MonthToDate)
		}
		if !near(s.Budget.CurrentSpend, s.MonthToDate) {
			t.Errorf("%s: budget spend %.2f != MTD %.2f", s.Account.Name, s.Budget.CurrentSpend, s.MonthToDate)
		}
		for i := 1; i < len(s.ByService); i++ {
			if s.ByService[i].Amount > s.ByService[i-1].Amount {
				t.Errorf("%s: services not sorted desc at %d", s.Account.Name, i)
			}
		}
	}
}

func TestProdServiceList(t *testing.T) {
	prod := snapshots(t, New())[0]
	if len(prod.ByService) != 10 || prod.ByService[0].Service != "Virtual Machines" || prod.ByService[4].Service != "Key Vault" {
		t.Errorf("unexpected prod services: %+v", prod.ByService)
	}
}

func TestSnapshotsDoNotAliasProviderData(t *testing.T) {
	p := New()
	a := snapshots(t, p)[0]
	a.ByService[0].Amount = 0
	*a.ForecastMonth = 0
	a.Budget.Amount = 0
	b := snapshots(t, p)[0]
	if b.ByService[0].Amount == 0 || *b.ForecastMonth == 0 || b.Budget.Amount == 0 {
		t.Error("mutating one snapshot changed later snapshots")
	}
}

func TestToggles(t *testing.T) {
	ctx := context.Background()
	accts, _, _ := New().Accounts(ctx)

	_, err := (&Provider{FailAccount: "Office-Dev"}).Snapshot(ctx, accts[1], now)
	if err == nil {
		t.Error("FailAccount: expected an error")
	}
	if _, err := (&Provider{FailAccount: "Office-Dev"}).Snapshot(ctx, accts[0], now); err != nil {
		t.Errorf("FailAccount must not affect other accounts: %v", err)
	}

	s, err := (&Provider{Currency: "EUR"}).Snapshot(ctx, accts[0], now)
	if err != nil || s.Currency != "EUR" {
		t.Errorf("Currency override: %q, %v", s.Currency, err)
	}
}

func TestUnknownAccountAndCancelledContext(t *testing.T) {
	if _, err := New().Snapshot(context.Background(), provider.Account{ID: "nope", Name: "x"}, now); err == nil {
		t.Error("unknown account: expected an error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := New().Accounts(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Accounts with cancelled ctx: %v", err)
	}
}

func TestResourceGroupsAddUpToTheirSubscription(t *testing.T) {
	for _, s := range snapshots(t, New()) {
		var sum float64
		for _, g := range s.ResourceGroups {
			sum += g.Amount
		}
		if !near(sum, s.MonthToDate) {
			t.Errorf("%s: resource groups sum to %.2f, want %.2f", s.Account.Name, sum, s.MonthToDate)
		}
		for i := 1; i < len(s.ResourceGroups); i++ {
			if s.ResourceGroups[i].Amount > s.ResourceGroups[i-1].Amount {
				t.Errorf("%s: resource groups not sorted by spend", s.Account.Name)
			}
		}
	}
}

func TestSubscriptionsAreAllListedWhateverIsTracked(t *testing.T) {
	p := New()
	p.Configure(config.Azure{Subscriptions: config.Subscriptions{List: []config.Subscription{{ID: accounts[0].ID}}}})

	tracked, _, _ := p.Accounts(context.Background())
	all, err := p.Subscriptions(context.Background())
	if err != nil || len(tracked) != 1 || len(all) != 2 {
		t.Errorf("tracked %d, all %d, err %v", len(tracked), len(all), err)
	}
	for _, a := range all {
		if a.ResourceGroup != "" {
			t.Errorf("a subscription listing must be whole subscriptions: %+v", a)
		}
	}
}

func TestConfigureSelectsScopes(t *testing.T) {
	ctx := context.Background()
	p := New()
	p.Configure(config.Azure{Subscriptions: config.Subscriptions{List: []config.Subscription{
		{ID: strings.ToUpper(accounts[0].ID), ResourceGroup: "rg-web"},
		{ID: accounts[1].ID},
		{ID: "not-a-fake-subscription"},
	}}})

	got, truncated, err := p.Accounts(ctx)
	if err != nil || truncated != 0 || len(got) != 2 {
		t.Fatalf("Accounts = %+v, %d, %v", got, truncated, err)
	}
	if got[0].Name != "Office-Prod" || got[0].ResourceGroup != "rg-web" || got[1].Name != "Office-Dev" || got[1].ResourceGroup != "" {
		t.Errorf("Accounts = %+v", got)
	}

	p.Configure(config.Azure{Subscriptions: config.Subscriptions{Auto: true}})
	if got, _, _ := p.Accounts(ctx); len(got) != 2 || got[0].ResourceGroup != "" {
		t.Errorf("auto should track both subscriptions whole: %+v", got)
	}
	p.Configure(config.Azure{Subscriptions: config.Subscriptions{List: []config.Subscription{}}})
	if got, _, _ := p.Accounts(ctx); len(got) != 0 {
		t.Errorf("an empty list tracks nothing: %+v", got)
	}
}

func TestResourceGroupSnapshotIsTheGroupsShare(t *testing.T) {
	ctx := context.Background()
	p := New()
	prod := snapshots(t, p)[0]

	web, err := p.Snapshot(ctx, provider.Account{ID: prod.Account.ID, Name: "Office-Prod", ResourceGroup: "RG-WEB"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if web.Account.ResourceGroup != "rg-web" || web.Account.Label() != "Office-Prod / rg-web" {
		t.Errorf("account = %+v, want the group's own spelling", web.Account)
	}
	if !near(web.MonthToDate, 600) {
		t.Errorf("month to date = %.2f, want 600", web.MonthToDate)
	}
	share := 600 / prod.MonthToDate
	if !near(web.LastMonthToDate, prod.LastMonthToDate*share) || !near(*web.ForecastMonth, *prod.ForecastMonth*share) {
		t.Errorf("last month %.2f / forecast %.2f are not scaled by %.4f", web.LastMonthToDate, *web.ForecastMonth, share)
	}
	if web.Budget != nil {
		t.Errorf("a resource group has no budget of its own: %+v", web.Budget)
	}
	var services float64
	for _, s := range web.ByService {
		services += s.Amount
	}
	if !near(services, 600) {
		t.Errorf("services sum %.2f, want the group's 600", services)
	}
	if len(web.ResourceGroups) != 3 {
		t.Errorf("the picker still needs the subscription's groups: %+v", web.ResourceGroups)
	}
	if prod.ByService[0].Amount != 540.10 {
		t.Error("scaling a group's snapshot must not touch the shared sample data")
	}
}

func TestUnknownResourceGroup(t *testing.T) {
	prod := snapshots(t, New())[0]
	_, err := New().Snapshot(context.Background(), provider.Account{ID: prod.Account.ID, ResourceGroup: "rg-nope"}, now)
	if err == nil {
		t.Error("expected an error")
	}
}

func TestTogglesTargetOneScope(t *testing.T) {
	ctx := context.Background()
	prod := snapshots(t, New())[0].Account
	web := prod
	web.ResourceGroup = "rg-web"

	p := &Provider{FailAccount: "Office-Prod / rg-web"}
	if _, err := p.Snapshot(ctx, web, now); err == nil {
		t.Error("the resource-group scope should fail")
	}
	if _, err := p.Snapshot(ctx, prod, now); err != nil {
		t.Errorf("the whole subscription must be unaffected: %v", err)
	}
}
