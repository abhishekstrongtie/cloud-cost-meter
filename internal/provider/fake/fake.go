// Package fake is a deterministic CostProvider that reproduces the Option A mockup:
// Office-Prod $1,102.40 + Office-Dev $181.80 = $1,284.20 month to date.
package fake

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"costmeter/internal/config"
	"costmeter/internal/provider"
)

// Provider serves the sample data. Create it with New (or as a literal); the exported
// fields are test toggles. Do not copy it after first use.
type Provider struct {
	FailAccount string // Account.Label() whose Snapshot returns a generic error
	Currency    string // overrides "USD" for every account when set

	mu         sync.Mutex
	cfg        config.Azure
	configured bool
}

var (
	_ provider.CostProvider       = (*Provider)(nil)
	_ provider.SubscriptionLister = (*Provider)(nil)
)

type account struct {
	provider.Account
	monthToDate     float64
	lastMonthToDate float64
	forecast        float64
	budget          provider.Budget
	services        []provider.ServiceCost // already sorted desc, unbucketed
	groups          []provider.ResourceGroupCost
}

var accounts = []account{
	{
		Account:         provider.Account{Provider: "azure", ID: "11111111-1111-1111-1111-111111111111", Name: "Office-Prod"},
		monthToDate:     1102.40,
		lastMonthToDate: 985.00,
		forecast:        2100.00,
		budget:          provider.Budget{Name: "prod-monthly", Amount: 2000, CurrentSpend: 1102.40},
		services: []provider.ServiceCost{
			{Service: "Virtual Machines", Amount: 540.10},
			{Service: "Kubernetes Service", Amount: 310.25},
			{Service: "Storage", Amount: 118.40},
			{Service: "SQL Database", Amount: 92.00},
			{Service: "Key Vault", Amount: 12.40},
			{Service: "Azure Monitor", Amount: 9.80},
			{Service: "Load Balancer", Amount: 7.25},
			{Service: "Bandwidth", Amount: 5.60},
			{Service: "Container Registry", Amount: 4.10},
			{Service: "Azure DNS", Amount: 2.50},
		},
		groups: []provider.ResourceGroupCost{ // sums to the subscription's 1,102.40
			{Name: "rg-web", Amount: 600.00},
			{Name: "rg-data", Amount: 350.40},
			{Name: "rg-shared", Amount: 152.00},
		},
	},
	{
		Account:         provider.Account{Provider: "azure", ID: "22222222-2222-2222-2222-222222222222", Name: "Office-Dev"},
		monthToDate:     181.80,
		lastMonthToDate: 161.61,
		forecast:        350.00,
		budget:          provider.Budget{Name: "dev-monthly", Amount: 200, CurrentSpend: 181.80},
		services: []provider.ServiceCost{
			{Service: "Virtual Machines", Amount: 120.30},
			{Service: "Storage", Amount: 41.50},
			{Service: "Azure Monitor", Amount: 20.00},
		},
		groups: []provider.ResourceGroupCost{ // sums to the subscription's 181.80
			{Name: "rg-dev-apps", Amount: 130.00},
			{Name: "rg-dev-data", Amount: 51.80},
		},
	},
}

func New() *Provider { return &Provider{} }

func (p *Provider) Name() string { return "Azure" }

// Configure takes an explicit list of scopes into account, like the real provider does.
// Until it is called (or while the config says "auto") both subscriptions are tracked whole.
func (p *Provider) Configure(cfg config.Azure) {
	p.mu.Lock()
	p.cfg, p.configured = cfg, true
	p.mu.Unlock()
}

func find(id string) (account, bool) {
	for _, a := range accounts {
		if strings.EqualFold(a.ID, id) {
			return a, true
		}
	}
	return account{}, false
}

func (p *Provider) Accounts(ctx context.Context) ([]provider.Account, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	p.mu.Lock()
	cfg, configured := p.cfg, p.configured
	p.mu.Unlock()

	if !configured || cfg.Subscriptions.Auto {
		return p.wholeSubscriptions(), 0, nil
	}
	out := []provider.Account{}
	for _, entry := range cfg.Subscriptions.List {
		base, ok := find(entry.ID)
		if !ok {
			continue // the fake only knows its own subscriptions
		}
		acct := base.Account
		if entry.Name != "" {
			acct.Name = entry.Name
		}
		acct.ResourceGroup = entry.ResourceGroup
		out = append(out, acct)
	}
	return out, 0, nil
}

// Subscriptions lists every subscription, whatever is tracked.
func (p *Provider) Subscriptions(ctx context.Context) ([]provider.Account, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return p.wholeSubscriptions(), nil
}

func (p *Provider) wholeSubscriptions() []provider.Account {
	out := make([]provider.Account, len(accounts))
	for i, a := range accounts {
		out[i] = a.Account
	}
	return out
}

func (p *Provider) Snapshot(ctx context.Context, want provider.Account, now time.Time) (provider.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return provider.Snapshot{}, err
	}
	if label := want.Label(); label != "" {
		if label == p.FailAccount {
			return provider.Snapshot{}, errors.New("fake: simulated failure for " + label)
		}
	}

	a, ok := find(want.ID)
	if !ok {
		return provider.Snapshot{}, errors.New("fake: unknown account " + want.ID)
	}
	currency := "USD"
	if p.Currency != "" {
		currency = p.Currency
	}
	forecast := a.forecast
	budget := a.budget
	snap := provider.Snapshot{
		Account:         a.Account,
		Currency:        currency,
		MonthToDate:     a.monthToDate,
		LastMonthToDate: a.lastMonthToDate,
		ByService:       append([]provider.ServiceCost(nil), a.services...),
		ResourceGroups:  append([]provider.ResourceGroupCost(nil), a.groups...),
		ForecastMonth:   &forecast,
		Budget:          &budget,
		FetchedAt:       now,
	}
	if want.ResourceGroup == "" {
		return snap, nil
	}

	// A resource group is its share of the subscription; it has no budget of its own.
	var group provider.ResourceGroupCost
	for _, g := range a.groups {
		if strings.EqualFold(g.Name, want.ResourceGroup) {
			group = g
		}
	}
	if group.Name == "" {
		return provider.Snapshot{}, errors.New("fake: unknown resource group " + want.ResourceGroup)
	}
	share := group.Amount / a.monthToDate
	snap.Account.ResourceGroup = group.Name
	snap.MonthToDate = group.Amount
	snap.LastMonthToDate *= share
	scaled := forecast * share
	snap.ForecastMonth = &scaled
	snap.Budget = nil
	for i := range snap.ByService {
		snap.ByService[i].Amount *= share
	}
	return snap, nil
}
