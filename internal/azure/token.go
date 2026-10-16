// Package azure implements provider.CostProvider on the Azure Cost Management REST API.
// Credentials come from the user's `az login` session; nothing is stored by CostMeter.
package azure

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"

	"costmeter/internal/provider"
)

const (
	armScope           = "https://management.azure.com/.default"
	tokenRefreshMargin = 5 * time.Minute
)

// tokenSource caches the access token until shortly before it expires.
// AzureCLICredential does not cache, so without this every request would spawn `az`.
type tokenSource struct {
	cred azcore.TokenCredential
	now  func() time.Time

	mu  sync.Mutex
	tok azcore.AccessToken
}

func newTokenSource(cred azcore.TokenCredential) *tokenSource {
	return &tokenSource{cred: cred, now: time.Now}
}

func (t *tokenSource) token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.tok.Token != "" && t.now().Before(t.tok.ExpiresOn.Add(-tokenRefreshMargin)) {
		return t.tok.Token, nil
	}
	tok, err := t.cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{armScope}})
	if err != nil {
		return "", mapTokenError(ctx, err)
	}
	t.tok = tok
	return tok.Token, nil
}

// mapTokenError turns a credential failure into the sentinel errors the UI reacts to.
// A signed-out CLI fails here, inside GetToken, before any HTTP request is made.
func mapTokenError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr // a cancelled refresh is not a sign-in problem
	}
	if strings.Contains(strings.ToLower(err.Error()), "not found on path") {
		return fmt.Errorf("%w: %w", provider.ErrAzureCLIMissing, err)
	}
	return fmt.Errorf("%w: %w", provider.ErrNotAuthorized, err)
}
