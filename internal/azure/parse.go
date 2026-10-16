package azure

import (
	"fmt"
	"sort"
	"strings"

	"costmeter/internal/provider"
)

// queryResponse is one page of a Cost Management query or forecast result.
type queryResponse struct {
	Properties struct {
		NextLink string `json:"nextLink"`
		Columns  []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"columns"`
		Rows [][]any `json:"rows"`
	} `json:"properties"`
}

// costRow is one row of a query result; fields are zero when the layout does not include them.
type costRow struct {
	Group    string // e.g. the service name
	Cost     float64
	Currency string
	Status   string // "Actual" or "Forecast", forecast results only
}

// layout says which columns a query is expected to return besides cost and currency.
type layout struct {
	group  string // name of the grouping column, "" for none
	status bool   // CostStatus
}

var costColumnNames = []string{"Cost", "PreTaxCost", "totalCost"}

// columnAliases are other names Azure has been seen to use for a grouping column.
var columnAliases = map[string][]string{"ResourceGroupName": {"ResourceGroup"}}

// parseRows concatenates the rows of all pages. Columns are found by name, never by position.
func parseRows(pages []queryResponse, l layout) ([]costRow, error) {
	var out []costRow
	for _, page := range pages {
		names := make([]string, len(page.Properties.Columns))
		for i, c := range page.Properties.Columns {
			names[i] = c.Name
		}
		idx := func(candidates ...string) int {
			for _, want := range candidates {
				for i, name := range names {
					if strings.EqualFold(name, want) {
						return i
					}
				}
			}
			return -1
		}

		cost, cur := idx(costColumnNames...), idx("Currency")
		group, status := -1, -1
		var missing []string
		if cost < 0 {
			missing = append(missing, strings.Join(costColumnNames, "/"))
		}
		if cur < 0 {
			missing = append(missing, "Currency")
		}
		if l.group != "" {
			if group = idx(append([]string{l.group}, columnAliases[l.group]...)...); group < 0 {
				missing = append(missing, l.group)
			}
		}
		if l.status {
			if status = idx("CostStatus"); status < 0 {
				missing = append(missing, "CostStatus")
			}
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("unexpected column layout: missing %s; found [%s]",
				strings.Join(missing, ", "), strings.Join(names, ", "))
		}

		for n, raw := range page.Properties.Rows {
			if len(raw) != len(names) {
				return nil, fmt.Errorf("row %d has %d values for %d columns", n, len(raw), len(names))
			}
			row := costRow{Currency: fmt.Sprint(raw[cur])}
			var err error
			if row.Cost, err = number(raw[cost]); err != nil {
				return nil, fmt.Errorf("row %d cost: %w", n, err)
			}
			if group >= 0 {
				row.Group = fmt.Sprint(raw[group])
			}
			if status >= 0 {
				row.Status = fmt.Sprint(raw[status])
			}
			out = append(out, row)
		}
	}
	return out, nil
}

func number(v any) (float64, error) {
	f, ok := v.(float64)
	if !ok {
		return 0, fmt.Errorf("want a number, got %T (%v)", v, v)
	}
	return f, nil
}

// singleCurrency returns the one currency all the rows are in, or "" if there are no rows.
// Mixed currencies are an error, not a guess: CostMeter adds the amounts up as they are.
func singleCurrency(rowSets ...[]costRow) (string, error) {
	seen := map[string]bool{}
	for _, rows := range rowSets {
		for _, r := range rows {
			seen[r.Currency] = true
		}
	}
	var codes []string
	for code := range seen {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	switch len(codes) {
	case 0:
		return "", nil
	case 1:
		return codes[0], nil
	}
	return "", fmt.Errorf("costs are reported in more than one currency (%s), which is not supported", strings.Join(codes, ", "))
}

type monthSummary struct {
	monthToDate float64
	byService   []provider.ServiceCost // sorted desc, unbucketed
}

// summarize adds up the rows: the month-to-date total and the cost per service.
func summarize(rows []costRow) monthSummary {
	var s monthSummary
	services := map[string]float64{}
	for _, r := range rows {
		s.monthToDate += r.Cost
		services[r.Group] += r.Cost
	}
	for name, amount := range services {
		s.byService = append(s.byService, provider.ServiceCost{Service: name, Amount: amount})
	}
	sort.Slice(s.byService, func(i, j int) bool {
		a, b := s.byService[i], s.byService[j]
		if a.Amount != b.Amount {
			return a.Amount > b.Amount
		}
		return a.Service < b.Service
	})
	return s
}

func sumCost(rows []costRow) float64 {
	var total float64
	for _, r := range rows {
		total += r.Cost
	}
	return total
}
