package azure

import (
	"context"
	"os"
	"testing"
	"time"

	"costmeter/internal/config"
)

// TestLive talks to the real Azure using your `az login` session. It only runs when
// COSTMETER_LIVE=1 and asserts nothing about amounts: compare the logged month-to-date with
// the Azure Portal's Cost analysis for the same UTC period.
//
//	COSTMETER_LIVE=1 go test ./internal/azure -run Live -v
func TestLive(t *testing.T) {
	if os.Getenv("COSTMETER_LIVE") != "1" {
		t.Skip("set COSTMETER_LIVE=1 to query the real Azure")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	p, err := NewCLI(config.Default().Azure)
	if err != nil {
		t.Fatal(err)
	}
	accts, truncated, err := p.Accounts(ctx)
	if err != nil {
		t.Fatalf("Accounts: %v", err)
	}
	if len(accts) == 0 {
		t.Fatal("no enabled subscriptions are visible to this login")
	}
	t.Logf("%d subscriptions (%d more not shown); using %q", len(accts), truncated, accts[0].Name)

	now := time.Now()
	s, err := p.Snapshot(ctx, accts[0], now)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	t.Logf("UTC month to date through %s: %.2f %s", now.UTC().Format("2006-01-02"), s.MonthToDate, s.Currency)
	t.Logf("last month, same days: %.2f", s.LastMonthToDate)
	t.Logf("services: %d", len(s.ByService))
	if s.ForecastMonth != nil {
		t.Logf("forecast for the month: %.2f", *s.ForecastMonth)
	} else {
		t.Log("forecast: none returned")
	}
	if s.Budget != nil {
		t.Logf("budget %q: %.2f of %.2f spent", s.Budget.Name, s.Budget.CurrentSpend, s.Budget.Amount)
	} else {
		t.Log("budget: no monthly budget found")
	}
}
