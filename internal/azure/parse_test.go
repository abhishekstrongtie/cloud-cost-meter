package azure

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func near(a, b float64) bool { return a-b < 0.005 && b-a < 0.005 }

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func loadFixture(t *testing.T, path ...string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(append([]string{"testdata"}, path...)...))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func loadPage(t *testing.T, name string) queryResponse {
	t.Helper()
	var page queryResponse
	if err := json.Unmarshal(loadFixture(t, name), &page); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return page
}

var servicesLayout = layout{group: "ServiceName"}

func TestParseFindsColumnsByName(t *testing.T) {
	t.Run("Cost column", func(t *testing.T) {
		rows, err := parseRows([]queryResponse{loadPage(t, "query_services.json")}, servicesLayout)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 3 {
			t.Fatalf("rows = %d, want 3", len(rows))
		}
		if first := rows[0]; first.Cost != 52 || first.Group != "Storage" || first.Currency != "USD" {
			t.Errorf("first row = %+v", first)
		}
	})

	t.Run("PreTaxCost column in a different order", func(t *testing.T) {
		rows, err := parseRows([]queryResponse{loadPage(t, "query_services_pretax.json")}, servicesLayout)
		if err != nil {
			t.Fatal(err)
		}
		want := costRow{Group: "Storage", Cost: 12.5, Currency: "USD"}
		if len(rows) != 2 || rows[0] != want {
			t.Errorf("rows = %+v, want first %+v", rows, want)
		}
	})
}

func TestParseMergesPages(t *testing.T) {
	pages := []queryResponse{loadPage(t, "query_paged_1.json"), loadPage(t, "query_paged_2.json")}
	rows, err := parseRows(pages, servicesLayout)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[2].Cost != 3 || rows[2].Group != "Key Vault" {
		t.Errorf("merged rows = %+v", rows)
	}
	if pages[0].Properties.NextLink == "" || pages[1].Properties.NextLink != "" {
		t.Errorf("nextLinks = %q / %q", pages[0].Properties.NextLink, pages[1].Properties.NextLink)
	}
}

func TestParseUnknownLayoutListsFoundColumns(t *testing.T) {
	var page queryResponse
	page.Properties.Columns = []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}{{Name: "Spend"}, {Name: "When"}}

	_, err := parseRows([]queryResponse{page}, servicesLayout)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"Cost/PreTaxCost/totalCost", "Currency", "ServiceName", "found [Spend, When]"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestParseRejectsMalformedRows(t *testing.T) {
	cols := `"columns":[{"name":"Cost"},{"name":"ServiceName"},{"name":"Currency"}]`
	tests := []struct{ name, rows, want string }{
		{"short row", `[[1.0, "VM"]]`, "2 values for 3 columns"},
		{"cost is text", `[["x", "VM", "USD"]]`, "cost"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var page queryResponse
			if err := json.Unmarshal([]byte(`{"properties":{`+cols+`,"rows":`+tt.rows+`}}`), &page); err != nil {
				t.Fatal(err)
			}
			_, err := parseRows([]queryResponse{page}, servicesLayout)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestSummarizeAddsUpServices(t *testing.T) {
	rows, err := parseRows([]queryResponse{loadPage(t, "query_services.json")}, servicesLayout)
	if err != nil {
		t.Fatal(err)
	}
	s := summarize(rows)

	if !near(s.monthToDate, 330.5) {
		t.Errorf("monthToDate = %.2f, want 330.50", s.monthToDate)
	}
	wantServices := []struct {
		name   string
		amount float64
	}{{"Virtual Machines", 275.5}, {"Storage", 52}, {"Key Vault", 3}} // sorted, though the fixture is not
	if len(s.byService) != len(wantServices) {
		t.Fatalf("byService = %+v", s.byService)
	}
	for i, want := range wantServices {
		if got := s.byService[i]; got.Service != want.name || !near(got.Amount, want.amount) {
			t.Errorf("byService[%d] = %+v, want %v %.2f", i, got, want.name, want.amount)
		}
	}
}

func TestSummarizeMergesRowsOfTheSameService(t *testing.T) {
	s := summarize([]costRow{
		{Group: "VM", Cost: 10, Currency: "USD"},
		{Group: "VM", Cost: 5, Currency: "USD"},
		{Group: "Storage", Cost: -2, Currency: "USD"}, // a credit
	})
	if s.monthToDate != 13 || len(s.byService) != 2 || s.byService[0].Service != "VM" || s.byService[0].Amount != 15 {
		t.Errorf("summary = %+v", s)
	}
	if got := sumCost([]costRow{{Cost: 40}, {Cost: 2.5}}); got != 42.5 {
		t.Errorf("sumCost = %v", got)
	}
}

func TestSingleCurrency(t *testing.T) {
	usd := []costRow{{Currency: "USD"}, {Currency: "USD"}}
	eur := []costRow{{Currency: "EUR"}}

	if got, err := singleCurrency(nil); got != "" || err != nil {
		t.Errorf("no rows: %q, %v", got, err)
	}
	if got, err := singleCurrency(usd); got != "USD" || err != nil {
		t.Errorf("one currency: %q, %v", got, err)
	}
	if got, err := singleCurrency(nil, usd, nil, usd); got != "USD" || err != nil {
		t.Errorf("one currency across row sets: %q, %v", got, err)
	}
	_, err := singleCurrency(usd, eur)
	if err == nil || !strings.Contains(err.Error(), "more than one currency (EUR, USD)") {
		t.Errorf("mixed currencies must be an error that names them: %v", err)
	}
}

func TestWindows(t *testing.T) {
	tests := []struct {
		name                               string
		now                                time.Time
		lastFrom, lastTo, fcFrom, monthEnd time.Time
		lastDay                            bool
	}{
		{
			name:     "mid month: daily window starts on the 1st",
			now:      time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC),
			lastFrom: day(2026, 9, 1), lastTo: day(2026, 9, 15),
			fcFrom: day(2026, 10, 16), monthEnd: day(2026, 10, 31),
		},
		{
			name:     "the 3rd: daily window crosses into the previous month",
			now:      time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC),
			lastFrom: day(2026, 9, 1), lastTo: day(2026, 9, 3),
			fcFrom: day(2026, 10, 4), monthEnd: day(2026, 10, 31),
		},
		{
			name:     "31 Oct: last-month window ends 30 Sep, last day of month",
			now:      time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC),
			lastFrom: day(2026, 9, 1), lastTo: day(2026, 9, 30),
			fcFrom: day(2026, 11, 1), monthEnd: day(2026, 10, 31), lastDay: true,
		},
		{
			name:     "30 Mar in a leap year clamps to 29 Feb",
			now:      time.Date(2028, 3, 30, 8, 0, 0, 0, time.UTC),
			lastFrom: day(2028, 2, 1), lastTo: day(2028, 2, 29),
			fcFrom: day(2028, 3, 31), monthEnd: day(2028, 3, 31),
		},
		{
			name:     "31 Mar clamps to 28 Feb",
			now:      time.Date(2026, 3, 31, 8, 0, 0, 0, time.UTC),
			lastFrom: day(2026, 2, 1), lastTo: day(2026, 2, 28),
			fcFrom: day(2026, 4, 1), monthEnd: day(2026, 3, 31), lastDay: true,
		},
		{
			name:     "January looks back into December of the previous year",
			now:      time.Date(2027, 1, 2, 8, 0, 0, 0, time.UTC),
			lastFrom: day(2026, 12, 1), lastTo: day(2026, 12, 2),
			fcFrom: day(2027, 1, 3), monthEnd: day(2027, 1, 31),
		},
		{
			name:     "local evening is already tomorrow in UTC",
			now:      time.Date(2026, 10, 15, 22, 30, 0, 0, time.FixedZone("CDT", -5*3600)), // 03:30 UTC on the 16th
			lastFrom: day(2026, 9, 1), lastTo: day(2026, 9, 16),
			fcFrom: day(2026, 10, 17), monthEnd: day(2026, 10, 31),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := windowsFor(tt.now)
			got := []time.Time{w.lastMonthFrom, w.lastMonthTo, w.forecastFrom, w.monthEnd}
			want := []time.Time{tt.lastFrom, tt.lastTo, tt.fcFrom, tt.monthEnd}
			labels := []string{"lastMonthFrom", "lastMonthTo", "forecastFrom", "monthEnd"}
			for i := range got {
				if !got[i].Equal(want[i]) {
					t.Errorf("%s = %s, want %s", labels[i], got[i].Format("2006-01-02"), want[i].Format("2006-01-02"))
				}
			}
			if w.lastDay != tt.lastDay {
				t.Errorf("lastDay = %v, want %v", w.lastDay, tt.lastDay)
			}
		})
	}
}

func TestRequestBodies(t *testing.T) {
	w := windowsFor(time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC))
	tests := []struct {
		name string
		got  queryRequest
		want string
	}{
		{"services this month", servicesRequest(w), `{
			"type":"ActualCost","timeframe":"Custom",
			"timePeriod":{"from":"2026-10-01T00:00:00Z","to":"2026-10-05T23:59:59Z"},
			"dataset":{"granularity":"None",
				"aggregation":{"totalCost":{"name":"Cost","function":"Sum"}},
				"grouping":[{"type":"Dimension","name":"ServiceName"}]}}`},
		{"last month", lastMonthRequest(w), `{
			"type":"ActualCost","timeframe":"Custom",
			"timePeriod":{"from":"2026-09-01T00:00:00Z","to":"2026-09-05T23:59:59Z"},
			"dataset":{"granularity":"None",
				"aggregation":{"totalCost":{"name":"Cost","function":"Sum"}}}}`},
		{"forecast", forecastRequest(w), `{
			"type":"ActualCost","timeframe":"Custom",
			"timePeriod":{"from":"2026-10-06T00:00:00Z","to":"2026-10-31T23:59:59Z"},
			"dataset":{"granularity":"Daily",
				"aggregation":{"totalCost":{"name":"Cost","function":"Sum"}}},
			"includeActualCost":false,"includeFreshPartialCost":false}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(tt.got)
			if err != nil {
				t.Fatal(err)
			}
			var got, want any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tt.want), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("body = %s\nwant   %s", raw, tt.want)
			}
		})
	}
}

func TestPickBudget(t *testing.T) {
	var resp budgetsResponse
	if err := json.Unmarshal(loadFixture(t, "budgets.json"), &resp); err != nil {
		t.Fatal(err)
	}

	// Annual, quarterly and monthly budgets are all there; only the largest Monthly one counts.
	got := pickBudget(resp.Value)
	if got == nil || got.Name != "monthly-big" || got.Amount != 2000 || got.CurrentSpend != 412.75 {
		t.Errorf("largest monthly budget = %+v", got)
	}
	if got := pickBudget(nil); got != nil {
		t.Errorf("no budgets: %+v", got)
	}
	onlyAnnual := []budgetItem{resp.Value[0]}
	if got := pickBudget(onlyAnnual); got != nil {
		t.Errorf("a non-monthly budget must be ignored: %+v", got)
	}
}

// Responses captured on a real subscription (IDs stripped) are dropped into testdata/real/.
// They only get structural checks, so they can never break on values.
func TestRealFixturesStructural(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "real", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Log("no captured responses in testdata/real yet")
		return
	}
	for _, file := range files {
		name := filepath.Base(file)
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case strings.HasPrefix(name, "query_"), strings.HasPrefix(name, "forecast_"):
				var page queryResponse
				if err := json.Unmarshal(data, &page); err != nil {
					t.Fatal(err)
				}
				l := layout{status: strings.HasPrefix(name, "forecast_")}
				for _, c := range page.Properties.Columns {
					switch strings.ToLower(c.Name) {
					case "servicename":
						l.group = "ServiceName"
					case "resourcegroupname", "resourcegroup":
						l.group = "ResourceGroupName"
					}
				}
				rows, err := parseRows([]queryResponse{page}, l)
				if err != nil {
					t.Fatalf("%v (columns: %+v)", err, page.Properties.Columns)
				}
				if total := sumAll(rows); total < 0 {
					t.Errorf("total cost %.2f is negative", total)
				}
			case strings.HasPrefix(name, "budgets_"):
				var resp budgetsResponse
				if err := json.Unmarshal(data, &resp); err != nil {
					t.Fatal(err)
				}
				pickBudget(resp.Value)
			case strings.HasPrefix(name, "subscriptions_"):
				var resp subscriptionsResponse
				if err := json.Unmarshal(data, &resp); err != nil {
					t.Fatal(err)
				}
			default:
				t.Errorf("%s: name must start with query_, forecast_, budgets_ or subscriptions_", name)
			}
		})
	}
}

func sumAll(rows []costRow) float64 {
	var total float64
	for _, r := range rows {
		total += r.Cost
	}
	return total
}
