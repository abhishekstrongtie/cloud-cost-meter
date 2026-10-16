package azure

import (
	"context"
	"log"
	"net/http"
	"strings"

	"costmeter/internal/provider"
)

type budgetsResponse struct {
	Value    []budgetItem `json:"value"`
	NextLink string       `json:"nextLink"`
}

type budgetItem struct {
	Name       string `json:"name"`
	Properties struct {
		TimeGrain    string  `json:"timeGrain"`
		Amount       float64 `json:"amount"`
		CurrentSpend struct {
			Amount float64 `json:"amount"`
		} `json:"currentSpend"`
	} `json:"properties"`
}

// pickBudget chooses the budget to report: of the Monthly budgets, the largest. Azure's own
// currentSpend is used, so any filters on the budget apply.
func pickBudget(items []budgetItem) *provider.Budget {
	var best *budgetItem
	for i := range items {
		it := &items[i]
		if !strings.EqualFold(it.Properties.TimeGrain, "Monthly") {
			continue
		}
		if best == nil || it.Properties.Amount > best.Properties.Amount {
			best = it
		}
	}
	if best == nil {
		return nil
	}
	return &provider.Budget{
		Name:         best.Name,
		Amount:       best.Properties.Amount,
		CurrentSpend: best.Properties.CurrentSpend.Amount,
	}
}

// monthlyBudget returns the subscription's monthly budget, or nil if there is none or it
// cannot be read. Like the forecast, a budget problem never fails the snapshot.
func (p *Provider) monthlyBudget(ctx context.Context, a provider.Account) *provider.Budget {
	var items []budgetItem
	next := p.c.endpoint(scopePath(a)+"/providers/Microsoft.Consumption/budgets", apiBudgets)
	for i := 0; next != ""; i++ {
		if i == maxPages {
			log.Printf("azure: budgets for %s: %v", a.Name, errTooManyPages)
			return nil
		}
		var page budgetsResponse
		if _, err := p.c.doJSON(ctx, http.MethodGet, next, nil, &page); err != nil {
			log.Printf("azure: budgets for %s unavailable: %v", a.Name, err)
			return nil
		}
		items = append(items, page.Value...)
		next = page.NextLink
	}
	return pickBudget(items)
}
