package azure

import (
	"context"
	"log"

	"costmeter/internal/provider"
)

func forecastRequest(w windows) queryRequest {
	no := false
	return queryRequest{
		Type:       "ActualCost",
		Timeframe:  "Custom",
		TimePeriod: timePeriod{From: startOfDay(w.forecastFrom), To: endOfDay(w.monthEnd)},
		Dataset: dataset{
			Granularity: "Daily",
			Aggregation: costAggregation,
		},
		IncludeActualCost:       &no,
		IncludeFreshPartialCost: &no,
	}
}

// forecastMonth returns the projected month total: month to date plus Azure's forecast for
// the remaining days. It returns nil when no forecast is available. The forecast is a
// nice-to-have, so no failure here fails the snapshot.
func (p *Provider) forecastMonth(ctx context.Context, a provider.Account, w windows, monthToDate float64) *float64 {
	if w.lastDay {
		total := monthToDate // nothing left to forecast
		return &total
	}
	pages, err := p.fetchRows(ctx, a, "forecast", forecastRequest(w))
	if err != nil {
		log.Printf("azure: forecast for %s unavailable: %v", a.Name, err)
		return nil
	}
	rows, err := parseRows(pages, layout{status: true})
	if err != nil {
		log.Printf("azure: forecast for %s unreadable: %v", a.Name, err)
		return nil
	}

	var remaining float64
	var counted int
	for _, r := range rows {
		if r.Status == "Forecast" {
			remaining += r.Cost
			counted++
		}
	}
	if counted == 0 {
		return nil
	}
	total := monthToDate + remaining
	return &total
}
