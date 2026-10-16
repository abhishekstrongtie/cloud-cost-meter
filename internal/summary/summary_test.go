package summary

import (
	"context"
	"math"
	"testing"
	"time"

	"costmeter/internal/provider"
	"costmeter/internal/provider/fake"
)

var now = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

const refresh = 4 * time.Hour

func near(a, b float64) bool { return math.Abs(a-b) < 0.005 }

func fakeSnapshots(t *testing.T) []provider.Snapshot {
	t.Helper()
	p := fake.New()
	ctx := context.Background()
	accts, _, err := p.Accounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var out []provider.Snapshot
	for _, a := range accts {
		s, err := p.Snapshot(ctx, a, now)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func TestMockupTotals(t *testing.T) {
	v := Build(fakeSnapshots(t), now, refresh)

	if !v.HasTotals || v.OtherCurrency != 0 {
		t.Fatalf("HasTotals = %v, OtherCurrency = %d", v.HasTotals, v.OtherCurrency)
	}
	c := v.Totals
	if c.Currency != "USD" || c.Accounts != 2 {
		t.Errorf("currency %q with %d accounts", c.Currency, c.Accounts)
	}
	if !near(c.MonthToDate, 1284.20) {
		t.Errorf("MonthToDate = %.2f, want 1284.20", c.MonthToDate)
	}
	if !c.HasChange || math.Round(c.ChangePercent) != 12 {
		t.Errorf("change = %.3f%% (has=%v), want +12%%", c.ChangePercent, c.HasChange)
	}
	if !c.HasBudget || !near(c.BudgetTotal, 2200) || math.Round(c.BudgetUsagePercent) != 58 {
		t.Errorf("budget total %.2f, usage %.2f%%; want 2200 and 58%%", c.BudgetTotal, c.BudgetUsagePercent)
	}
	if c.BudgetPartial || c.ForecastPartial {
		t.Errorf("nothing is partial here: budget=%v forecast=%v", c.BudgetPartial, c.ForecastPartial)
	}
	if !c.HasForecast || !near(c.Forecast, 2450) {
		t.Errorf("Forecast = %.2f (has=%v), want 2450", c.Forecast, c.HasForecast)
	}
	if !near(c.OverBudget, 250) {
		t.Errorf("OverBudget = %.2f, want 250", c.OverBudget)
	}
	if v.Stale {
		t.Error("fresh fake data must not be stale")
	}
}

func TestAccountRowsKeepInputOrder(t *testing.T) {
	v := Build(fakeSnapshots(t), now, refresh)
	if len(v.Accounts) != 2 || v.Accounts[0].Account.Name != "Office-Prod" || v.Accounts[1].Account.Name != "Office-Dev" {
		t.Errorf("rows = %+v", v.Accounts)
	}
	if !near(v.Accounts[0].MonthToDate, 1102.40) {
		t.Errorf("prod MTD = %.2f", v.Accounts[0].MonthToDate)
	}
}

func TestServiceBucketing(t *testing.T) {
	prod := Build(fakeSnapshots(t), now, refresh).Accounts[0]

	// default maxServices (5): five services plus Other (5 services) = 29.25
	if len(prod.Services) != 6 {
		t.Fatalf("rows = %d, want 6: %+v", len(prod.Services), prod.Services)
	}
	if prod.Services[0].Name != "Virtual Machines" || prod.Services[4].Name != "Key Vault" {
		t.Errorf("top services = %+v", prod.Services[:5])
	}
	other := prod.Services[5]
	if !other.Other || other.Count != 5 || other.Name != "Other (5 services)" || !near(other.Amount, 29.25) {
		t.Errorf("other = %+v, want Other (5 services) = 29.25", other)
	}

	// the buckets always add up to the account total
	var sum float64
	for _, r := range prod.Services {
		sum += r.Amount
	}
	if !near(sum, prod.MonthToDate) {
		t.Errorf("bucketed rows sum to %.2f, want %.2f", sum, prod.MonthToDate)
	}

	three := Build(fakeSnapshots(t), now, refresh, WithMaxServices(3)).Accounts[0]
	if len(three.Services) != 4 || three.Services[3].Count != 7 {
		t.Errorf("maxServices 3: %+v", three.Services)
	}
}

func TestServiceBucketingEdgeCases(t *testing.T) {
	svc := func(names ...string) []provider.ServiceCost {
		var out []provider.ServiceCost
		for i, n := range names {
			out = append(out, provider.ServiceCost{Service: n, Amount: float64(100 - i)})
		}
		return out
	}
	tests := []struct {
		name     string
		services []provider.ServiceCost
		limit    int
		wantRows int
		wantLast string
	}{
		{"fewer than limit: no Other row", svc("a", "b"), 5, 2, "b"},
		{"exactly the limit: no Other row", svc("a", "b", "c"), 3, 3, "c"},
		{"one left over uses the singular", svc("a", "b", "c"), 2, 3, "Other (1 service)"},
		{"none at all", nil, 5, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := bucketServices(tt.services, tt.limit)
			if len(rows) != tt.wantRows {
				t.Fatalf("rows = %+v, want %d", rows, tt.wantRows)
			}
			if tt.wantRows > 0 && rows[len(rows)-1].Name != tt.wantLast {
				t.Errorf("last row = %q, want %q", rows[len(rows)-1].Name, tt.wantLast)
			}
		})
	}
}

func TestBucketingSortsUnsortedInputAndKeepsCredits(t *testing.T) {
	rows := bucketServices([]provider.ServiceCost{
		{Service: "small", Amount: 1},
		{Service: "credit", Amount: -5},
		{Service: "big", Amount: 50},
	}, 2)
	if rows[0].Name != "big" || rows[1].Name != "small" || !rows[2].Other || rows[2].Amount != -5 {
		t.Errorf("rows = %+v", rows)
	}
}

func TestAccountWithoutBudgetIsExcludedFromBudgetFigures(t *testing.T) {
	snaps := fakeSnapshots(t)
	snaps[1].Budget = nil // Office-Dev has no monthly budget

	c := Build(snaps, now, refresh).Totals
	if !c.HasBudget || !c.BudgetPartial || c.BudgetAccounts != 1 || c.Accounts != 2 {
		t.Errorf("budget accounts %d of %d, partial=%v", c.BudgetAccounts, c.Accounts, c.BudgetPartial)
	}
	if !near(c.BudgetTotal, 2000) || !near(c.BudgetSpent, 1102.40) {
		t.Errorf("budget total %.2f spent %.2f; want 2000 and 1102.40", c.BudgetTotal, c.BudgetSpent)
	}
	if !near(c.BudgetUsagePercent, 55.12) {
		t.Errorf("usage = %.2f%%, want 55.12%% (Dev must not count)", c.BudgetUsagePercent)
	}
	// forecast still shows everything, but over-budget compares Prod only: 2100 - 2000
	if !near(c.Forecast, 2450) || !near(c.OverBudget, 100) {
		t.Errorf("forecast %.2f over %.2f; want 2450 and 100", c.Forecast, c.OverBudget)
	}
}

func TestNoBudgetAtAll(t *testing.T) {
	snaps := fakeSnapshots(t)
	snaps[0].Budget, snaps[1].Budget = nil, nil
	c := Build(snaps, now, refresh).Totals
	if c.HasBudget || c.BudgetPartial || c.OverBudget != 0 || c.BudgetUsagePercent != 0 {
		t.Errorf("no budgets: %+v", c)
	}
}

func TestForecastPartialAndWithinBudget(t *testing.T) {
	snaps := fakeSnapshots(t)
	snaps[1].ForecastMonth = nil
	c := Build(snaps, now, refresh).Totals
	if !c.HasForecast || !c.ForecastPartial || !near(c.Forecast, 2100) {
		t.Errorf("forecast %.2f partial=%v", c.Forecast, c.ForecastPartial)
	}
	if !near(c.OverBudget, 100) { // only Prod has forecast and budget: 2100 - 2000
		t.Errorf("OverBudget = %.2f, want 100", c.OverBudget)
	}

	for i := range snaps {
		f := 500.0
		snaps[i].ForecastMonth = &f
	}
	if got := Build(snaps, now, refresh).Totals.OverBudget; got != 0 {
		t.Errorf("forecast within budget: OverBudget = %.2f, want 0", got)
	}

	snaps[0].ForecastMonth, snaps[1].ForecastMonth = nil, nil
	c = Build(snaps, now, refresh).Totals
	if c.HasForecast || c.ForecastPartial || c.OverBudget != 0 {
		t.Errorf("no forecast at all: %+v", c)
	}
}

func TestAScopeInAnotherCurrencyHasARowButIsNotAddedUp(t *testing.T) {
	snaps := fakeSnapshots(t)
	snaps[1].Currency = "EUR" // Office-Dev bills in euro, Office-Prod in dollars

	v := Build(snaps, now, refresh)
	if v.Totals.Currency != "USD" || v.OtherCurrency != 1 || v.Totals.Accounts != 1 {
		t.Fatalf("totals %+v, other currency %d", v.Totals, v.OtherCurrency)
	}
	if !near(v.Totals.MonthToDate, 1102.40) || !near(v.Totals.BudgetTotal, 2000) || !near(v.Totals.Forecast, 2100) {
		t.Errorf("the euro scope leaked into the dollar totals: %+v", v.Totals)
	}
	if len(v.Accounts) != 2 || v.Accounts[1].Currency != "EUR" || !near(v.Accounts[1].MonthToDate, 181.80) {
		t.Errorf("rows = %+v; the euro scope must still show, in its own currency", v.Accounts)
	}
}

func TestTheTotalsCurrencyIsTheFirstFetchedScopes(t *testing.T) {
	snaps := fakeSnapshots(t)
	snaps[0].Currency = "EUR" // the first scope decides, even though the second is larger in numbers
	snaps[0].MonthToDate = 5
	v := Build(snaps, now, refresh)
	if v.Totals.Currency != "EUR" || v.OtherCurrency != 1 || !near(v.Totals.MonthToDate, 5) {
		t.Errorf("totals %+v, other currency %d", v.Totals, v.OtherCurrency)
	}
}

func TestAnUnfetchedScopeDoesNotDecideTheCurrency(t *testing.T) {
	snaps := append([]provider.Snapshot{{Account: provider.Account{ID: "x", Name: "X"}, Currency: "SEK", Stale: true, Err: "boom"}}, fakeSnapshots(t)...)
	v := Build(snaps, now, refresh)
	if v.Totals.Currency != "USD" || v.OtherCurrency != 0 {
		t.Errorf("totals currency %q, other %d; a placeholder must not set the currency", v.Totals.Currency, v.OtherCurrency)
	}
}

func TestStale(t *testing.T) {
	tests := []struct {
		name  string
		edit  func(*provider.Snapshot)
		stale bool
	}{
		{"fresh", func(*provider.Snapshot) {}, false},
		{"exactly 2 x refresh old", func(s *provider.Snapshot) { s.FetchedAt = now.Add(-2 * refresh) }, false},
		{"older than 2 x refresh", func(s *provider.Snapshot) { s.FetchedAt = now.Add(-2*refresh - time.Second) }, true},
		{"flagged by the scheduler", func(s *provider.Snapshot) { s.Stale = true }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snaps := fakeSnapshots(t)
			tt.edit(&snaps[1])
			v := Build(snaps, now, refresh)
			if v.Stale != tt.stale {
				t.Errorf("View.Stale = %v, want %v", v.Stale, tt.stale)
			}
			if v.Accounts[0].Stale || v.Accounts[1].Stale != tt.stale {
				t.Errorf("row staleness = %v/%v, want false/%v", v.Accounts[0].Stale, v.Accounts[1].Stale, tt.stale)
			}
		})
	}
}

func TestNoLastMonthFigure(t *testing.T) {
	snaps := fakeSnapshots(t)
	snaps[0].LastMonthToDate, snaps[1].LastMonthToDate = 0, 0
	c := Build(snaps, now, refresh).Totals
	if c.HasChange || c.ChangePercent != 0 {
		t.Errorf("change without a baseline: %+v", c)
	}
}

func TestEmpty(t *testing.T) {
	v := Build(nil, now, refresh)
	if v.HasTotals || len(v.Accounts) != 0 || v.Stale {
		t.Errorf("empty input: %+v", v)
	}
}

func TestAScopeThatHasNeverBeenFetchedHasARowButNoTotals(t *testing.T) {
	snaps := fakeSnapshots(t)
	placeholder := provider.Snapshot{
		Account: provider.Account{Provider: "azure", ID: "ccc", Name: "Office-QA", ResourceGroup: "rg-private"},
		Stale:   true,
		Err:     "no cost access on Office-QA / rg-private",
	}
	v := Build(append(snaps, placeholder), now, refresh)

	if !v.HasTotals || v.Totals.Currency != "USD" || v.Totals.Accounts != 2 || v.OtherCurrency != 0 {
		t.Fatalf("totals = %+v; the placeholder must not count as a scope or set the currency", v.Totals)
	}
	if !near(v.Totals.MonthToDate, 1284.20) || v.Totals.BudgetPartial || v.Totals.ForecastPartial {
		t.Errorf("the placeholder changed the totals: %+v", v.Totals)
	}
	if len(v.Accounts) != 3 {
		t.Fatalf("rows = %d, want 3", len(v.Accounts))
	}
	row := v.Accounts[2]
	if !row.NoData || !row.Stale || row.Err == "" || row.Account.ResourceGroup != "rg-private" {
		t.Errorf("placeholder row = %+v", row)
	}
	if !v.Stale {
		t.Error("a placeholder means the view is not fully up to date")
	}
	if v.Accounts[0].NoData || v.Accounts[1].NoData {
		t.Error("fetched accounts must not be flagged")
	}
}

func TestAViewOfOnlyPlaceholdersHasRowsButNoTotals(t *testing.T) {
	v := Build([]provider.Snapshot{{Account: provider.Account{ID: "a", Name: "A"}, Stale: true, Err: "boom"}}, now, refresh)
	if v.HasTotals || len(v.Accounts) != 1 || !v.Accounts[0].NoData || !v.Stale {
		t.Errorf("view = %+v", v)
	}
}
