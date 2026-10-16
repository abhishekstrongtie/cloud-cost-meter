package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"

	"costmeter/internal/config"
	"costmeter/internal/provider"
)

const (
	baseURL          = "https://management.azure.com"
	requestTimeout   = 30 * time.Second
	maxResponseBytes = 64 << 20

	apiSubscriptions = "2022-12-01"
	apiCostMgmt      = "2023-11-01"
	apiBudgets       = "2023-05-01"
)

// Provider reads cost data for Azure subscriptions. It is safe for concurrent use.
type Provider struct {
	c *client

	mu  sync.RWMutex
	cfg config.Azure
}

var _ provider.CostProvider = (*Provider)(nil)

// New builds a provider that authenticates with cred.
func New(cred azcore.TokenCredential, cfg config.Azure) *Provider {
	return newProvider(newClient(baseURL, cred), cfg)
}

// NewCLI builds a provider that reuses the user's `az login` session.
func NewCLI(cfg config.Azure) (*Provider, error) {
	cred, err := azidentity.NewAzureCLICredential(nil)
	if err != nil {
		return nil, err
	}
	return New(cred, cfg), nil
}

func newProvider(c *client, cfg config.Azure) *Provider {
	return &Provider{c: c, cfg: cfg}
}

func (p *Provider) Name() string { return "Azure" }

// Configure swaps in a reloaded configuration; it applies from the next call on.
func (p *Provider) Configure(cfg config.Azure) {
	p.mu.Lock()
	p.cfg = cfg
	p.mu.Unlock()
}

func (p *Provider) config() config.Azure {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cfg
}

type client struct {
	http   *http.Client
	base   *url.URL
	tokens *tokenSource
}

func newClient(base string, cred azcore.TokenCredential) *client {
	u, err := url.Parse(base)
	if err != nil {
		panic("azure: bad base URL " + base) // only ever called with constants or test servers
	}
	return &client{
		http:   &http.Client{Timeout: requestTimeout},
		base:   u,
		tokens: newTokenSource(cred),
	}
}

func (c *client) endpoint(path, apiVersion string) string {
	return strings.TrimRight(c.base.String(), "/") + path + "?api-version=" + apiVersion
}

type accountKey struct{}

// withAccount names the account a request is for, so a 403 can say which one lacks access.
func withAccount(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, accountKey{}, name)
}

func accountFrom(ctx context.Context) string {
	name, _ := ctx.Value(accountKey{}).(string)
	return name
}

// allowedSuffixes are the only endpoints CostMeter ever calls, all read-only.
var allowedSuffixes = []string{
	"/providers/microsoft.costmanagement/query",
	"/providers/microsoft.costmanagement/forecast",
	"/providers/microsoft.consumption/budgets",
}

// check refuses any URL that is not one of our endpoints on the Azure management host,
// so the bearer token can never be sent elsewhere, even via a hostile nextLink.
func (c *client) check(u *url.URL) error {
	if u.Scheme != c.base.Scheme || u.Host != c.base.Host {
		return fmt.Errorf("azure: refusing request to %s://%s", u.Scheme, u.Host)
	}
	path := strings.ToLower(strings.TrimRight(u.Path, "/")) // nextLink casing varies, e.g. .../Forecast
	if path == "/subscriptions" {
		return nil
	}
	for _, suffix := range allowedSuffixes {
		if strings.HasSuffix(path, suffix) {
			return nil
		}
	}
	return fmt.Errorf("azure: refusing request to unexpected path %q", u.Path)
}

// do sends one request. rawURL is either built by endpoint or a nextLink used verbatim
// (it already carries api-version and $skiptoken). Non-2xx responses become errors.
func (c *client) do(ctx context.Context, method, rawURL string, body any) (*http.Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("azure: bad URL: %w", err)
	}
	if err := c.check(u); err != nil {
		return nil, err
	}

	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, payload)
	if err != nil {
		return nil, err
	}
	tok, err := c.tokens.token(ctx)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer resp.Body.Close()
	return nil, statusError(ctx, resp)
}

// doJSON is do plus decoding the body into out. It returns the HTTP status; 204 leaves out untouched.
func (c *client) doJSON(ctx context.Context, method, rawURL string, body, out any) (int, error) {
	resp, err := c.do(ctx, method, rawURL, body)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent || out == nil {
		return resp.StatusCode, nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(out); err != nil {
		return resp.StatusCode, fmt.Errorf("azure: decoding response: %w", err)
	}
	return resp.StatusCode, nil
}

func statusError(ctx context.Context, resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("%w: azure returned 401", provider.ErrNotAuthorized)
	case http.StatusForbidden:
		if name := accountFrom(ctx); name != "" {
			return provider.ForbiddenError{AccountName: name}
		}
	case http.StatusTooManyRequests:
		return errors.New("azure: rate limited by Azure (HTTP 429); it will be tried again at the next refresh")
	}
	return fmt.Errorf("azure: %s: %s", resp.Status, errorMessage(resp.Body))
}

// errorMessage extracts error.message from an Azure error body, else the raw text.
func errorMessage(r io.Reader) string {
	raw, _ := io.ReadAll(io.LimitReader(r, 4096))
	var env struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &env) == nil && env.Error.Message != "" {
		return env.Error.Message
	}
	if text := strings.TrimSpace(string(raw)); text != "" {
		return text
	}
	return "no details"
}

var errTooManyPages = errors.New("azure: too many result pages")
