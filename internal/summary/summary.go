// Package summary turns provider snapshots into the numbers the menu shows.
// CostMeter assumes every scope is billed in one currency and never converts between them:
// scopes in a different currency from the first fetched one get a row but stay out of the totals.
package summary

import (
	"fmt"
	"sort"
	"time"

	"costmeter/internal/provider"
)

const defaultMaxServices = 5

type View struct {
	Totals        Totals
	HasTotals     bool         // false until some scope has been fetched
	OtherCurrency int          // fetched scopes in another currency than Totals.Currency
	Accounts      []AccountRow // in input order
	Stale         bool         // any snapshot is stale or older than 2 × refresh
}

type Totals struct {
	Currency string // that of the first fetched scope
	Accounts int    // scopes counted in the totals

	MonthToDate     float64
	LastMonthToDate float64
	ChangePercent   float64
	HasChange       bool // false when there is no last-month figure to compare against

	Forecast        float64
	HasForecast     bool
	ForecastPartial bool // some scopes reported no forecast

	BudgetTotal        float64
	BudgetSpent        float64
	BudgetUsagePercent float64
	HasBudget          bool
	BudgetAccounts     int  // scopes that have a budget
	BudgetPartial      bool // some scopes have no budget

	// OverBudget is how far the forecast exceeds the budget, counting only scopes that have
	// both. Zero when the forecast is within budget.
	OverBudget float64
}

type AccountRow struct {
	Account     provider.Account
	Currency    string
	MonthToDate float64
	Services    []ServiceRow // top maxServices, then one "Other" row if any services remain
	Stale       bool
	Err         string
	NoData      bool // the first fetch of this scope has not succeeded yet: only Account, Stale and Err mean anything
}

type ServiceRow struct {
	Name   string
	Amount float64
	Other  bool // the "Other (n services)" bucket
	Count  int  // services folded into the bucket; 0 for ordinary rows
}

type Option func(*options)

type options struct{ maxServices int }

// WithMaxServices sets how many services each account row lists before the "Other" bucket.
func WithMaxServices(n int) Option {
	return func(o *options) { o.maxServices = n }
}

func Build(snaps []provider.Snapshot, now time.Time, refresh time.Duration, opts ...Option) View {
	o := options{maxServices: defaultMaxServices}
	for _, opt := range opts {
		opt(&o)
	}

	var v View
	t := &v.Totals
	var forecastOfBoth, budgetOfBoth float64 // over scopes that have both, for OverBudget
	noForecast := 0

	for _, s := range snaps {
		stale := s.Stale || now.Sub(s.FetchedAt) > 2*refresh
		v.Stale = v.Stale || stale
		row := AccountRow{
			Account:     s.Account,
			Currency:    s.Currency,
			MonthToDate: s.MonthToDate,
			Services:    bucketServices(s.ByService, o.maxServices),
			Stale:       stale,
			Err:         s.Err,
			NoData:      s.FetchedAt.IsZero(),
		}
		v.Accounts = append(v.Accounts, row)
		if row.NoData { // never fetched: a row with its error, but nothing to add up
			continue
		}

		if !v.HasTotals {
			v.HasTotals, t.Currency = true, s.Currency
		}
		if s.Currency != t.Currency {
			v.OtherCurrency++
			continue
		}
		t.Accounts++
		t.MonthToDate += s.MonthToDate
		t.LastMonthToDate += s.LastMonthToDate

		if s.ForecastMonth != nil {
			t.Forecast += *s.ForecastMonth
			t.HasForecast = true
		} else {
			noForecast++
		}
		if s.Budget != nil {
			t.BudgetTotal += s.Budget.Amount
			t.BudgetSpent += s.Budget.CurrentSpend
			t.BudgetAccounts++
			t.HasBudget = true
			if s.ForecastMonth != nil {
				forecastOfBoth += *s.ForecastMonth
				budgetOfBoth += s.Budget.Amount
			}
		}
	}

	if t.LastMonthToDate > 0 {
		t.HasChange = true
		t.ChangePercent = (t.MonthToDate - t.LastMonthToDate) / t.LastMonthToDate * 100
	}
	t.ForecastPartial = t.HasForecast && noForecast > 0
	if t.BudgetTotal > 0 {
		t.BudgetUsagePercent = t.BudgetSpent / t.BudgetTotal * 100
	}
	t.BudgetPartial = t.HasBudget && t.BudgetAccounts < t.Accounts
	if over := forecastOfBoth - budgetOfBoth; over > 0 {
		t.OverBudget = over
	}
	return v
}

func bucketServices(services []provider.ServiceCost, limit int) []ServiceRow {
	sorted := append([]provider.ServiceCost(nil), services...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Amount != sorted[j].Amount {
			return sorted[i].Amount > sorted[j].Amount
		}
		return sorted[i].Service < sorted[j].Service
	})

	rows := make([]ServiceRow, 0, limit+1)
	var other ServiceRow
	for i, s := range sorted {
		if i < limit {
			rows = append(rows, ServiceRow{Name: s.Service, Amount: s.Amount})
			continue
		}
		other.Amount += s.Amount
		other.Count++
	}
	if other.Count > 0 {
		other.Other = true
		other.Name = fmt.Sprintf("Other (%d services)", other.Count)
		if other.Count == 1 {
			other.Name = "Other (1 service)"
		}
		rows = append(rows, other)
	}
	return rows
}
