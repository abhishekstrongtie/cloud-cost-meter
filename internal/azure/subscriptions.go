package azure

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"costmeter/internal/provider"
)

const maxAccounts = 10

type subscriptionsResponse struct {
	Value []struct {
		SubscriptionID string `json:"subscriptionId"`
		DisplayName    string `json:"displayName"`
		State          string `json:"state"`
	} `json:"value"`
	NextLink string `json:"nextLink"`
}

var _ provider.SubscriptionLister = (*Provider)(nil)

// Accounts returns the configured scopes, or in "auto" mode every enabled subscription (whole),
// sorted by name. At most 10 are returned; truncated counts the rest.
func (p *Provider) Accounts(ctx context.Context) ([]provider.Account, int, error) {
	cfg := p.config()
	if !cfg.Subscriptions.Auto {
		out := make([]provider.Account, 0, len(cfg.Subscriptions.List))
		for _, s := range cfg.Subscriptions.List {
			name := s.Name
			if name == "" {
				name = s.ID
			}
			out = append(out, provider.Account{Provider: "azure", ID: s.ID, Name: name, ResourceGroup: s.ResourceGroup})
		}
		return out, 0, nil
	}

	all, err := p.Subscriptions(ctx)
	if err != nil {
		return nil, 0, err
	}
	if len(all) > maxAccounts {
		return all[:maxAccounts], len(all) - maxAccounts, nil
	}
	return all, 0, nil
}

// Subscriptions lists every enabled subscription the login can see, sorted by name and not
// truncated: the scope picker offers all of them.
func (p *Provider) Subscriptions(ctx context.Context) ([]provider.Account, error) {
	var all []provider.Account
	next := p.c.endpoint("/subscriptions", apiSubscriptions)
	for i := 0; next != ""; i++ {
		if i == maxPages {
			return nil, errTooManyPages
		}
		var page subscriptionsResponse
		if _, err := p.c.doJSON(ctx, http.MethodGet, next, nil, &page); err != nil {
			return nil, err
		}
		for _, s := range page.Value {
			if strings.EqualFold(s.State, "Enabled") {
				all = append(all, provider.Account{Provider: "azure", ID: s.SubscriptionID, Name: s.DisplayName})
			}
		}
		next = page.NextLink
	}

	sort.Slice(all, func(i, j int) bool {
		a, b := strings.ToLower(all[i].Name), strings.ToLower(all[j].Name)
		if a != b {
			return a < b
		}
		return all[i].ID < all[j].ID
	})
	return all, nil
}
