package azure

import (
	"context"
	"log"
	"sort"

	"costmeter/internal/provider"
)

const resourceGroupColumn = "ResourceGroupName"

// resourceGroupsRequest sums cost per resource group, from the start of last month to today,
// so a group that was busy recently but is quiet today can still be picked.
func resourceGroupsRequest(w windows) queryRequest {
	return queryRequest{
		Type:       "ActualCost",
		Timeframe:  "Custom",
		TimePeriod: timePeriod{From: startOfDay(w.lastMonthFrom), To: endOfDay(w.today)},
		Dataset: dataset{
			Granularity: "None",
			Aggregation: costAggregation,
			Grouping:    []grouping{{Type: "Dimension", Name: resourceGroupColumn}},
		},
	}
}

// resourceGroups lists the subscription's resource groups that have spend, most expensive
// first. It always asks at subscription scope, even for a resource-group account, so the
// picker can offer the other groups too. It is best effort (a login that can only see one
// resource group cannot ask at this scope): any problem yields nil and never fails the snapshot.
func (p *Provider) resourceGroups(ctx context.Context, a provider.Account, w windows) []provider.ResourceGroupCost {
	subscription := provider.Account{Provider: a.Provider, ID: a.ID, Name: a.Name}
	pages, err := p.fetchRows(ctx, subscription, "query", resourceGroupsRequest(w))
	if err != nil {
		log.Printf("azure: resource groups of %s unavailable: %v", a.Name, err)
		return nil
	}
	rows, err := parseRows(pages, layout{group: resourceGroupColumn})
	if err != nil {
		log.Printf("azure: resource groups of %s unreadable: %v", a.Name, err)
		return nil
	}

	totals := map[string]float64{}
	for _, r := range rows {
		if r.Group == "" { // cost that belongs to no resource group
			continue
		}
		totals[r.Group] += r.Cost
	}
	var out []provider.ResourceGroupCost
	for name, amount := range totals {
		out = append(out, provider.ResourceGroupCost{Name: name, Amount: amount})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Amount != out[j].Amount {
			return out[i].Amount > out[j].Amount
		}
		return out[i].Name < out[j].Name
	})
	return out
}
