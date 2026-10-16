// Package provider defines the cloud-neutral types the rest of CostMeter works with.
// Azure implements CostProvider today; AWS can be added later without touching callers.
package provider

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Account is one scope that is tracked: a subscription, optionally narrowed to one resource group.
type Account struct {
	Provider      string // "azure" (later "aws")
	ID            string // subscription ID / AWS account ID
	Name          string // display name, e.g. "Office-Prod"
	ResourceGroup string // "" means the whole subscription
}

// Key identifies the scope. Azure treats IDs and resource group names case-insensitively.
func (a Account) Key() string {
	id := strings.ToLower(a.ID)
	if a.ResourceGroup == "" {
		return id
	}
	return id + "/resourceGroups/" + strings.ToLower(a.ResourceGroup)
}

// Label is the name to show for the scope: "Office-Prod" or "Office-Prod / rg-web".
func (a Account) Label() string {
	if a.ResourceGroup == "" {
		return a.Name
	}
	return a.Name + " / " + a.ResourceGroup
}

// ResourceGroupCost is what one resource group has spent.
type ResourceGroupCost struct {
	Name   string
	Amount float64
}

type ServiceCost struct {
	Service string
	Amount  float64
}

type Budget struct {
	Name         string
	Amount       float64 // monthly limit
	CurrentSpend float64 // provider-reported spend against this budget
}

type Snapshot struct {
	Account         Account
	Currency        string
	MonthToDate     float64       // UTC month, through today
	LastMonthToDate float64       // previous month, day 1 .. same day-of-month (clamped)
	ByService       []ServiceCost // sorted desc, unbucketed; summary applies top N + "Other"
	// ResourceGroups lists the subscription's resource groups with spend since the start of last
	// month, most expensive first. It feeds the scope picker and is best effort: empty when
	// unreadable, and filled for a resource-group scope too.
	ResourceGroups []ResourceGroupCost
	ForecastMonth  *float64 // nil when unavailable
	Budget         *Budget  // the selected monthly budget, nil if none
	FetchedAt      time.Time
	Stale          bool   // set by scheduler when a refresh for this account failed
	Err            string // last error message for this account, "" if none
}

type CostProvider interface {
	Name() string // "Azure"
	Accounts(ctx context.Context) (accounts []Account, truncated int, err error)
	Snapshot(ctx context.Context, a Account, now time.Time) (Snapshot, error)
}

// SubscriptionLister is implemented by providers that can list every enabled subscription,
// including ones that are not tracked, so the user can choose which to track.
type SubscriptionLister interface {
	Subscriptions(ctx context.Context) ([]Account, error)
}

var (
	ErrNotAuthorized   = errors.New("not signed in") // token acquisition failed
	ErrAzureCLIMissing = errors.New("az not found on PATH")
)

// ForbiddenError is HTTP 403 for one account. It is returned by value; match it with
// errors.As(err, &provider.ForbiddenError{}).
type ForbiddenError struct{ AccountName string }

func (e ForbiddenError) Error() string { return "no cost access on " + e.AccountName }
