package azure

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"costmeter/internal/provider"
)

const maxPages = 100

type queryRequest struct {
	Type       string     `json:"type"`
	Timeframe  string     `json:"timeframe"`
	TimePeriod timePeriod `json:"timePeriod"`
	Dataset    dataset    `json:"dataset"`

	// Forecast only; pointers so an explicit false is still sent.
	IncludeActualCost       *bool `json:"includeActualCost,omitempty"`
	IncludeFreshPartialCost *bool `json:"includeFreshPartialCost,omitempty"`
}

type timePeriod struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type dataset struct {
	Granularity string                 `json:"granularity"`
	Aggregation map[string]aggregation `json:"aggregation"`
	Grouping    []grouping             `json:"grouping,omitempty"`
}

type aggregation struct {
	Name     string `json:"name"`
	Function string `json:"function"`
}

type grouping struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

// windows holds every date range one snapshot needs. All dates are UTC, because that is
// what Azure's cost data uses; only the UI shows local times.
type windows struct {
	today         time.Time // 00:00 UTC of now's date
	monthStart    time.Time
	lastMonthFrom time.Time
	lastMonthTo   time.Time // same day-of-month last month, clamped to that month's length
	forecastFrom  time.Time // tomorrow
	monthEnd      time.Time // last day of this month
	lastDay       bool      // today is the last day of the month: nothing left to forecast
}

func windowsFor(now time.Time) windows {
	n := now.UTC()
	today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
	monthStart := time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, time.UTC)

	lastMonthFrom := monthStart.AddDate(0, -1, 0)
	lastMonthDays := monthStart.AddDate(0, 0, -1).Day()
	monthEnd := monthStart.AddDate(0, 1, -1)

	return windows{
		today:         today,
		monthStart:    monthStart,
		lastMonthFrom: lastMonthFrom,
		lastMonthTo:   lastMonthFrom.AddDate(0, 0, min(today.Day(), lastMonthDays)-1),
		forecastFrom:  today.AddDate(0, 0, 1),
		monthEnd:      monthEnd,
		lastDay:       today.Equal(monthEnd),
	}
}

func startOfDay(t time.Time) string { return t.Format("2006-01-02") + "T00:00:00Z" }
func endOfDay(t time.Time) string   { return t.Format("2006-01-02") + "T23:59:59Z" }

var costAggregation = map[string]aggregation{"totalCost": {Name: "Cost", Function: "Sum"}}

// serviceColumn is what this month's cost is grouped by.
const serviceColumn = "ServiceName"

// servicesRequest sums this month's cost per service: one row each.
func servicesRequest(w windows) queryRequest {
	return queryRequest{
		Type:       "ActualCost",
		Timeframe:  "Custom",
		TimePeriod: timePeriod{From: startOfDay(w.monthStart), To: endOfDay(w.today)},
		Dataset: dataset{
			Granularity: "None",
			Aggregation: costAggregation,
			Grouping:    []grouping{{Type: "Dimension", Name: serviceColumn}},
		},
	}
}

func lastMonthRequest(w windows) queryRequest {
	return queryRequest{
		Type:       "ActualCost",
		Timeframe:  "Custom",
		TimePeriod: timePeriod{From: startOfDay(w.lastMonthFrom), To: endOfDay(w.lastMonthTo)},
		Dataset:    dataset{Granularity: "None", Aggregation: costAggregation},
	}
}

// scopePath is the URL path of an account's scope: the subscription, or one resource group in
// it. The names are path-escaped, so a hostile or mistyped name cannot change the route.
func scopePath(a provider.Account) string {
	path := "/subscriptions/" + url.PathEscape(a.ID)
	if a.ResourceGroup != "" {
		path += "/resourceGroups/" + url.PathEscape(a.ResourceGroup)
	}
	return path
}

// fetchRows POSTs body to a Cost Management operation ("query" or "forecast") at the
// account's scope and follows nextLink, re-sending the same body for each further page.
// A 204 yields no pages.
func (p *Provider) fetchRows(ctx context.Context, a provider.Account, operation string, body queryRequest) ([]queryResponse, error) {
	next := p.c.endpoint(scopePath(a)+"/providers/Microsoft.CostManagement/"+operation, apiCostMgmt)
	var pages []queryResponse
	for i := 0; next != ""; i++ {
		if i == maxPages {
			return nil, errTooManyPages
		}
		var page queryResponse
		status, err := p.c.doJSON(ctx, http.MethodPost, next, body, &page)
		if err != nil {
			return nil, err
		}
		if status == http.StatusNoContent {
			break
		}
		pages = append(pages, page)
		next = page.Properties.NextLink
	}
	return pages, nil
}

// fallbackCurrency is used only when Azure returned no cost rows at all, e.g. a brand-new
// subscription, so there is no currency to read.
const fallbackCurrency = "USD"

func (p *Provider) Snapshot(ctx context.Context, a provider.Account, now time.Time) (provider.Snapshot, error) {
	ctx = withAccount(ctx, a.Label())
	w := windowsFor(now)

	pages, err := p.fetchRows(ctx, a, "query", servicesRequest(w))
	if err != nil {
		return provider.Snapshot{}, err
	}
	rows, err := parseRows(pages, layout{group: serviceColumn})
	if err != nil {
		return provider.Snapshot{}, wrapParse(a, "cost query", err)
	}

	pages, err = p.fetchRows(ctx, a, "query", lastMonthRequest(w))
	if err != nil {
		return provider.Snapshot{}, err
	}
	lastRows, err := parseRows(pages, layout{})
	if err != nil {
		return provider.Snapshot{}, wrapParse(a, "last-month query", err)
	}

	currency, err := singleCurrency(rows, lastRows)
	if err != nil {
		return provider.Snapshot{}, wrapParse(a, "cost query", err)
	}
	if currency == "" {
		currency = fallbackCurrency
	}

	m := summarize(rows)
	snap := provider.Snapshot{
		Account:         a,
		Currency:        currency,
		MonthToDate:     m.monthToDate,
		LastMonthToDate: sumCost(lastRows),
		ByService:       m.byService,
		ForecastMonth:   p.forecastMonth(ctx, a, w, m.monthToDate),
		Budget:          p.monthlyBudget(ctx, a),
		ResourceGroups:  p.resourceGroups(ctx, a, w),
		FetchedAt:       now,
	}
	if err := ctx.Err(); err != nil {
		return provider.Snapshot{}, err // forecast and budget swallow errors, including cancellation
	}
	return snap, nil
}

func wrapParse(a provider.Account, what string, err error) error {
	return fmt.Errorf("azure: %s for %s: %w", what, a.Name, err)
}
