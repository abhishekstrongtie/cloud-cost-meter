package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"

	"costmeter/internal/config"
	"costmeter/internal/provider"
)

func TestMain(m *testing.M) {
	log.SetOutput(io.Discard) // forecast and budget problems are logged; tests assert on results instead
	os.Exit(m.Run())
}

// stubCred is an azcore.TokenCredential that counts how often it is asked for a token.
type stubCred struct {
	mu      sync.Mutex
	calls   int
	token   string
	expires time.Time
	err     error
}

func newStubCred() *stubCred {
	return &stubCred{token: "test-token", expires: time.Now().Add(time.Hour)}
}

func (s *stubCred) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return azcore.AccessToken{}, s.err
	}
	return azcore.AccessToken{Token: s.token, ExpiresOn: s.expires}, nil
}

func (s *stubCred) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type recorded struct{ Method, Path, Escaped, RawQuery, Auth, Body string }

type recorder struct {
	mu   sync.Mutex
	reqs []recorded
}

func (r *recorder) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(body))
		r.mu.Lock()
		r.reqs = append(r.reqs, recorded{req.Method, req.URL.Path, req.URL.EscapedPath(), req.URL.RawQuery, req.Header.Get("Authorization"), string(body)})
		r.mu.Unlock()
		next.ServeHTTP(w, req)
	})
}

func (r *recorder) all() []recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recorded(nil), r.reqs...)
}

type harness struct {
	p    *Provider
	rec  *recorder
	cred *stubCred
	srv  *httptest.Server
}

func newHarness(t *testing.T, handler http.Handler, cfg config.Azure) *harness {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(rec.wrap(handler))
	t.Cleanup(srv.Close)
	cred := newStubCred()
	return &harness{p: newProvider(newClient(srv.URL, cred), cfg), rec: rec, cred: cred, srv: srv}
}

func defaultCfg() config.Azure { return config.Default().Azure }

func (h *harness) get(ctx context.Context, path string) (*http.Response, error) {
	return h.p.c.do(ctx, http.MethodGet, h.p.c.endpoint(path, apiSubscriptions), nil)
}

// respond is what a test server answers with; the zero value is "200 with the default fixture".
type respond struct {
	status int
	header map[string]string
	body   string
}

// fixtureHandler serves the testdata fixtures for each Azure endpoint. over replaces the
// answer for one endpoint: "services", "lastmonth", "rgs", "forecast", "budgets" or "subscriptions".
func fixtureHandler(t *testing.T, over map[string]respond) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.ToLower(r.URL.Path)
		var key, file string
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/query"):
			var body queryRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			grouped := body.Dataset.Granularity == "None" && len(body.Dataset.Grouping) > 0
			switch {
			case grouped && body.Dataset.Grouping[0].Name == "ResourceGroupName":
				key, file = "rgs", "query_rgs.json"
			case grouped:
				key, file = "services", "query_services.json"
			case body.Dataset.Granularity == "None":
				key, file = "lastmonth", "query_lastmonth.json"
			default:
				http.Error(w, "unexpected query", http.StatusBadRequest)
				return
			}
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/forecast"):
			key, file = "forecast", "forecast.json"
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/budgets"):
			key, file = "budgets", "budgets.json"
		case r.Method == http.MethodGet && path == "/subscriptions":
			key, file = "subscriptions", "subscriptions.json"
		default:
			http.NotFound(w, r)
			return
		}

		o := over[key]
		for k, v := range o.header {
			w.Header().Set(k, v)
		}
		if o.status != 0 && o.status != http.StatusOK {
			w.WriteHeader(o.status)
			_, _ = io.WriteString(w, o.body)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if o.body != "" {
			_, _ = io.WriteString(w, o.body)
			return
		}
		_, _ = w.Write(loadFixture(t, file))
	})
}

var (
	now  = time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	acct = provider.Account{Provider: "azure", ID: "11111111-1111-1111-1111-111111111111", Name: "Office-Dev"}
)

func TestStatusMapping(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		header  map[string]string
		body    string
		account string
		check   func(t *testing.T, err error)
	}{
		{name: "429 says it was rate limited", status: 429, header: map[string]string{"Retry-After": "120"}, check: func(t *testing.T, err error) {
			if err == nil || !strings.Contains(err.Error(), "rate limited") || !strings.Contains(err.Error(), "429") {
				t.Errorf("err = %v", err)
			}
		}},
		{name: "403 names the account", status: 403, account: "Office-Dev", check: func(t *testing.T, err error) {
			var fe provider.ForbiddenError
			if !errors.As(err, &fe) || fe.AccountName != "Office-Dev" {
				t.Errorf("err = %v, want ForbiddenError{Office-Dev}", err)
			}
		}},
		{name: "403 with no account is a plain error", status: 403, body: `{"error":{"message":"nope"}}`, check: func(t *testing.T, err error) {
			var fe provider.ForbiddenError
			if err == nil || errors.As(err, &fe) || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "nope") {
				t.Errorf("err = %v", err)
			}
		}},
		{name: "401 means not signed in", status: 401, check: func(t *testing.T, err error) {
			if !errors.Is(err, provider.ErrNotAuthorized) {
				t.Errorf("err = %v, want ErrNotAuthorized", err)
			}
		}},
		{name: "other errors carry Azure's message", status: 500, body: `{"error":{"code":"Boom","message":"the service fell over"}}`, check: func(t *testing.T, err error) {
			if err == nil || !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "the service fell over") {
				t.Errorf("err = %v", err)
			}
		}},
		{name: "non-JSON error body is shown as text", status: 502, body: "bad gateway upstream", check: func(t *testing.T, err error) {
			if err == nil || !strings.Contains(err.Error(), "bad gateway upstream") {
				t.Errorf("err = %v", err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tt.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}), defaultCfg())

			ctx := context.Background()
			if tt.account != "" {
				ctx = withAccount(ctx, tt.account)
			}
			_, err := h.get(ctx, "/subscriptions")
			tt.check(t, err)
		})
	}
}

func TestRequestsCarryTheBearerToken(t *testing.T) {
	h := newHarness(t, fixtureHandler(t, nil), defaultCfg())
	resp, err := h.get(context.Background(), "/subscriptions")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if reqs := h.rec.all(); len(reqs) != 1 || reqs[0].Auth != "Bearer test-token" {
		t.Errorf("requests = %+v", reqs)
	}
}

func TestTokenErrorsAreMapped(t *testing.T) {
	tests := []struct {
		name    string
		credErr error
		is      error
	}{
		{"az missing", errors.New("AzureCLICredential: Azure CLI not found on path"), provider.ErrAzureCLIMissing},
		{"signed out", errors.New("AzureCLICredential: ERROR: Please run 'az login' to setup account."), provider.ErrNotAuthorized},
		{"anything else", errors.New("something odd"), provider.ErrNotAuthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, fixtureHandler(t, nil), defaultCfg())
			h.cred.err = tt.credErr

			_, err := h.get(context.Background(), "/subscriptions")
			if !errors.Is(err, tt.is) {
				t.Errorf("err = %v, want it to wrap %v", err, tt.is)
			}
			if !errors.Is(err, tt.credErr) {
				t.Errorf("err = %v, want it to wrap the original credential error", err)
			}
			if n := len(h.rec.all()); n != 0 {
				t.Errorf("%d HTTP requests were made without a token", n)
			}
		})
	}
}

func TestCancelledContextIsNotASignInProblem(t *testing.T) {
	h := newHarness(t, fixtureHandler(t, nil), defaultCfg())
	h.cred.err = errors.New("context canceled while running az")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := h.get(ctx, "/subscriptions")
	if !errors.Is(err, context.Canceled) || errors.Is(err, provider.ErrNotAuthorized) {
		t.Errorf("err = %v, want plain context.Canceled", err)
	}
}

func TestTokenIsCachedUntilItNearsExpiry(t *testing.T) {
	h := newHarness(t, fixtureHandler(t, nil), defaultCfg())
	clock := time.Now()
	h.p.c.tokens.now = func() time.Time { return clock }
	h.cred.expires = clock.Add(time.Hour)

	for i := 0; i < 2; i++ {
		resp, err := h.get(context.Background(), "/subscriptions")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if n := h.cred.callCount(); n != 1 {
		t.Errorf("credential called %d times for two requests, want 1", n)
	}

	clock = clock.Add(55*time.Minute + time.Second) // inside the 5 minute margin before expiry
	resp, err := h.get(context.Background(), "/subscriptions")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if n := h.cred.callCount(); n != 2 {
		t.Errorf("credential called %d times after the margin, want 2", n)
	}
}

func TestAllowList(t *testing.T) {
	h := newHarness(t, fixtureHandler(t, nil), defaultCfg())
	srv := h.srv.URL
	ctx := context.Background()

	refused := []struct{ name, method, url string }{
		{"other resource provider", "GET", srv + "/subscriptions/x/providers/Microsoft.Compute/virtualMachines?api-version=1"},
		{"role assignments (a write endpoint)", "POST", srv + "/subscriptions/x/providers/Microsoft.Authorization/roleAssignments?api-version=1"},
		{"resource groups", "GET", srv + "/subscriptions/x/resourcegroups?api-version=1"},
		{"another host", "GET", "http://evil.example.com/subscriptions?api-version=1"},
		{"another scheme", "GET", strings.Replace(srv, "http://", "https://", 1) + "/subscriptions"},
		{"userinfo trick", "GET", "http://" + strings.TrimPrefix(srv, "http://") + "@evil.example.com/subscriptions"},
		{"no host", "GET", "/subscriptions"},
	}
	for _, tt := range refused {
		t.Run("refuses "+tt.name, func(t *testing.T) {
			_, err := h.p.c.do(ctx, tt.method, tt.url, nil)
			if err == nil || !strings.Contains(err.Error(), "refusing") {
				t.Errorf("err = %v, want a refusal", err)
			}
		})
	}
	if n := len(h.rec.all()); n != 0 {
		t.Errorf("%d requests reached the server", n)
	}
	if n := h.cred.callCount(); n != 0 {
		t.Errorf("a token was fetched %d times for refused requests", n)
	}

	for _, path := range []string{
		"/subscriptions",
		"/subscriptions/",
		"/subscriptions/x/providers/Microsoft.CostManagement/query",
		"/subscriptions/x/providers/Microsoft.CostManagement/Forecast",
		"/subscriptions/x/providers/microsoft.consumption/budgets",
	} {
		u, _ := h.p.c.base.Parse(path)
		if err := h.p.c.check(u); err != nil {
			t.Errorf("%s should be allowed: %v", path, err)
		}
	}
}

func TestNextLinkIsFollowedVerbatimWithTheSameBody(t *testing.T) {
	const cols = `"columns":[{"name":"Cost"},{"name":"UsageDate"},{"name":"CostStatus"},{"name":"Currency"}]`
	calls := 0
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			// Azure's real nextLink spells the operation with a capital letter.
			next := "http://" + r.Host + "/subscriptions/S/providers/Microsoft.CostManagement/Forecast?api-version=2023-11-01&$skiptoken=abc"
			fmt.Fprintf(w, `{"properties":{"nextLink":%q,%s,"rows":[[1.0,20261006,"Forecast","USD"]]}}`, next, cols)
			return
		}
		fmt.Fprintf(w, `{"properties":{"nextLink":null,%s,"rows":[[2.0,20261007,"Forecast","USD"]]}}`, cols)
	}), defaultCfg())

	pages, err := h.p.fetchRows(context.Background(), provider.Account{ID: "S"}, "forecast", forecastRequest(windowsFor(now)))
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 {
		t.Fatalf("pages = %d, want 2", len(pages))
	}
	reqs := h.rec.all()
	if len(reqs) != 2 {
		t.Fatalf("requests = %+v", reqs)
	}
	if reqs[1].Method != "POST" || reqs[1].Path != "/subscriptions/S/providers/Microsoft.CostManagement/Forecast" ||
		reqs[1].RawQuery != "api-version=2023-11-01&$skiptoken=abc" {
		t.Errorf("second request = %+v, want the nextLink verbatim", reqs[1])
	}
	if reqs[0].Body == "" || reqs[0].Body != reqs[1].Body {
		t.Errorf("bodies differ:\n%s\n%s", reqs[0].Body, reqs[1].Body)
	}
}

func TestFetchRowsGivesUpOnEndlessPaging(t *testing.T) {
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next := "http://" + r.Host + "/subscriptions/S/providers/Microsoft.CostManagement/query?api-version=2023-11-01"
		fmt.Fprintf(w, `{"properties":{"nextLink":%q,"columns":[],"rows":[]}}`, next)
	}), defaultCfg())

	_, err := h.p.fetchRows(context.Background(), provider.Account{ID: "S"}, "query", servicesRequest(windowsFor(now)))
	if !errors.Is(err, errTooManyPages) {
		t.Errorf("err = %v, want errTooManyPages", err)
	}
	if n := len(h.rec.all()); n != maxPages {
		t.Errorf("made %d requests, want %d", n, maxPages)
	}
}

func TestNoContentMeansNoRows(t *testing.T) {
	h := newHarness(t, fixtureHandler(t, map[string]respond{"forecast": {status: 204}}), defaultCfg())
	pages, err := h.p.fetchRows(context.Background(), provider.Account{ID: "S"}, "forecast", forecastRequest(windowsFor(now)))
	if err != nil || len(pages) != 0 {
		t.Errorf("pages = %d, err = %v; want none", len(pages), err)
	}
}

func snapshot(t *testing.T, h *harness, at time.Time) provider.Snapshot {
	t.Helper()
	s, err := h.p.Snapshot(context.Background(), acct, at)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	return s
}

func TestSnapshotFromFixtures(t *testing.T) {
	h := newHarness(t, fixtureHandler(t, nil), defaultCfg())
	s := snapshot(t, h, now)

	if s.Account != acct || s.Currency != "USD" || !s.FetchedAt.Equal(now) {
		t.Errorf("account %+v currency %q fetched %v", s.Account, s.Currency, s.FetchedAt)
	}
	if !near(s.MonthToDate, 330.5) || !near(s.LastMonthToDate, 1000) {
		t.Errorf("month to date %.2f, last month %.2f; want 330.50 and 1000", s.MonthToDate, s.LastMonthToDate)
	}
	if len(s.ByService) != 3 || s.ByService[0].Service != "Virtual Machines" || !near(s.ByService[0].Amount, 275.5) {
		t.Errorf("ByService = %+v", s.ByService)
	}
	if s.ForecastMonth == nil || !near(*s.ForecastMonth, 546) { // 330.5 + 70 + 70 + 75.5; the Actual row is ignored
		t.Errorf("ForecastMonth = %v, want 546", s.ForecastMonth)
	}
	if s.Budget == nil || s.Budget.Name != "monthly-big" || s.Budget.Amount != 2000 || s.Budget.CurrentSpend != 412.75 {
		t.Errorf("Budget = %+v", s.Budget)
	}
	if s.Stale || s.Err != "" {
		t.Errorf("stale=%v err=%q", s.Stale, s.Err)
	}

	wantGroups := []provider.ResourceGroupCost{{Name: "rg-web", Amount: 600}, {Name: "rg-data", Amount: 350.4}, {Name: "rg-shared", Amount: 152}}
	if len(s.ResourceGroups) != len(wantGroups) {
		t.Fatalf("ResourceGroups = %+v, want %+v", s.ResourceGroups, wantGroups)
	}
	for i, want := range wantGroups {
		if got := s.ResourceGroups[i]; got.Name != want.Name || !near(got.Amount, want.Amount) {
			t.Errorf("ResourceGroups[%d] = %+v, want %+v", i, got, want)
		}
	}

	reqs := h.rec.all()
	if len(reqs) != 5 {
		t.Fatalf("requests = %+v, want daily, last month, forecast, budgets, resource groups", reqs)
	}
	for i, wantSuffix := range []string{"/query", "/query", "/forecast", "/budgets", "/query"} {
		if !strings.HasSuffix(strings.ToLower(reqs[i].Path), wantSuffix) || reqs[i].Auth != "Bearer test-token" {
			t.Errorf("request %d = %+v, want %s", i, reqs[i], wantSuffix)
		}
	}
	if !strings.Contains(reqs[0].RawQuery, "api-version=2023-11-01") || !strings.Contains(reqs[3].RawQuery, "api-version=2023-05-01") {
		t.Errorf("api versions: %q / %q", reqs[0].RawQuery, reqs[3].RawQuery)
	}
	if !strings.Contains(reqs[0].Body, `"ServiceName"`) || strings.Contains(reqs[1].Body, `"grouping"`) ||
		!strings.Contains(reqs[0].Body, `"granularity":"None"`) || !strings.Contains(reqs[1].Body, `"granularity":"None"`) {
		t.Errorf("query bodies: %s / %s", reqs[0].Body, reqs[1].Body)
	}
}

func TestConfigureTakesEffectOnTheNextCall(t *testing.T) {
	h := newHarness(t, fixtureHandler(t, nil), defaultCfg())
	if accts, _, err := h.p.Accounts(context.Background()); err != nil || len(accts) != 3 {
		t.Fatalf("before Configure (auto): %d accounts, %v", len(accts), err)
	}

	cfg := defaultCfg()
	cfg.Subscriptions = config.Subscriptions{List: []config.Subscription{{ID: "aaa", Name: "Prod", ResourceGroup: "rg-web"}}}
	h.p.Configure(cfg)
	accts, _, err := h.p.Accounts(context.Background())
	if err != nil || len(accts) != 1 || accts[0].ResourceGroup != "rg-web" {
		t.Errorf("after Configure: %+v, %v", accts, err)
	}
}

func TestLastDayOfMonthSkipsTheForecastCall(t *testing.T) {
	h := newHarness(t, fixtureHandler(t, nil), defaultCfg())
	s := snapshot(t, h, time.Date(2026, 10, 31, 10, 0, 0, 0, time.UTC))

	for _, r := range h.rec.all() {
		if strings.HasSuffix(strings.ToLower(r.Path), "/forecast") {
			t.Errorf("forecast was requested on the last day of the month: %+v", r)
		}
	}
	if s.ForecastMonth == nil || *s.ForecastMonth != s.MonthToDate {
		t.Errorf("ForecastMonth = %v, want month to date %.2f", s.ForecastMonth, s.MonthToDate)
	}
}

func TestForecastProblemsNeverFailTheSnapshot(t *testing.T) {
	bad := map[string]respond{
		"no content":         {status: 204},
		"server error":       {status: 500, body: "boom"},
		"rate limited":       {status: 429},
		"forbidden":          {status: 403},
		"only actual rows":   {body: `{"properties":{"columns":[{"name":"Cost"},{"name":"UsageDate"},{"name":"CostStatus"},{"name":"Currency"}],"rows":[[5.0,20261005,"Actual","USD"]]}}`},
		"no rows":            {body: `{"properties":{"columns":[{"name":"Cost"},{"name":"UsageDate"},{"name":"CostStatus"},{"name":"Currency"}],"rows":[]}}`},
		"unreadable columns": {body: `{"properties":{"columns":[{"name":"Foo"}],"rows":[]}}`},
	}
	for name, r := range bad {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, fixtureHandler(t, map[string]respond{"forecast": r}), defaultCfg())
			s := snapshot(t, h, now)
			if s.ForecastMonth != nil {
				t.Errorf("ForecastMonth = %v, want nil", *s.ForecastMonth)
			}
			if s.Budget == nil || !near(s.MonthToDate, 330.5) {
				t.Errorf("the rest of the snapshot must survive: budget %+v, mtd %.2f", s.Budget, s.MonthToDate)
			}
		})
	}
}

func TestBudgetProblemsNeverFailTheSnapshot(t *testing.T) {
	for name, r := range map[string]respond{
		"server error": {status: 500, body: "boom"},
		"forbidden":    {status: 403},
		"no monthly":   {body: `{"value":[{"name":"q","properties":{"timeGrain":"Quarterly","amount":1}}]}`},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, fixtureHandler(t, map[string]respond{"budgets": r}), defaultCfg())
			s := snapshot(t, h, now)
			if s.Budget != nil {
				t.Errorf("Budget = %+v, want nil", s.Budget)
			}
			if s.ForecastMonth == nil || !near(s.MonthToDate, 330.5) {
				t.Errorf("the rest of the snapshot must survive: forecast %v, mtd %.2f", s.ForecastMonth, s.MonthToDate)
			}
		})
	}
}

func TestSnapshotFailsOnMainDataErrors(t *testing.T) {
	tests := []struct {
		name  string
		over  map[string]respond
		check func(t *testing.T, err error)
	}{
		{"cost query 403", map[string]respond{"services": {status: 403}}, func(t *testing.T, err error) {
			var fe provider.ForbiddenError
			if !errors.As(err, &fe) || fe.AccountName != "Office-Dev" {
				t.Errorf("err = %v, want ForbiddenError{Office-Dev}", err)
			}
		}},
		{"cost query 429", map[string]respond{"services": {status: 429}}, func(t *testing.T, err error) {
			if err == nil || !strings.Contains(err.Error(), "rate limited") {
				t.Errorf("err = %v", err)
			}
		}},
		{"cost query 401", map[string]respond{"services": {status: 401}}, func(t *testing.T, err error) {
			if !errors.Is(err, provider.ErrNotAuthorized) {
				t.Errorf("err = %v", err)
			}
		}},
		{"last month 500", map[string]respond{"lastmonth": {status: 500, body: "boom"}}, func(t *testing.T, err error) {
			if err == nil || !strings.Contains(err.Error(), "500") {
				t.Errorf("err = %v", err)
			}
		}},
		{"unknown layout", map[string]respond{"services": {body: `{"properties":{"columns":[{"name":"Foo"}],"rows":[]}}`}}, func(t *testing.T, err error) {
			if err == nil || !strings.Contains(err.Error(), "found [Foo]") || !strings.Contains(err.Error(), "Office-Dev") {
				t.Errorf("err = %v", err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, fixtureHandler(t, tt.over), defaultCfg())
			_, err := h.p.Snapshot(context.Background(), acct, now)
			tt.check(t, err)
		})
	}
}

func TestSnapshotWhenSignedOut(t *testing.T) {
	h := newHarness(t, fixtureHandler(t, nil), defaultCfg())
	h.cred.err = errors.New("Please run 'az login'")
	_, err := h.p.Snapshot(context.Background(), acct, now)
	if !errors.Is(err, provider.ErrNotAuthorized) || len(h.rec.all()) != 0 {
		t.Errorf("err = %v, requests = %d", err, len(h.rec.all()))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.cred.err = nil
	if _, err := h.p.Snapshot(ctx, acct, now); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled snapshot: err = %v", err)
	}
}

func TestSnapshotRefusesMixedCurrencies(t *testing.T) {
	body := `{"properties":{"columns":[{"name":"Cost"},{"name":"ServiceName"},{"name":"Currency"}],
		"rows":[[10.0,"VM","USD"],[10.0,"Storage","USD"],[999.0,"VM","EUR"]]}}`
	h := newHarness(t, fixtureHandler(t, map[string]respond{"services": {body: body}}), defaultCfg())

	_, err := h.p.Snapshot(context.Background(), acct, now)
	if err == nil || !strings.Contains(err.Error(), "more than one currency (EUR, USD)") || !strings.Contains(err.Error(), "Office-Dev") {
		t.Errorf("err = %v; guessing which currency to add up would be wrong", err)
	}
}

func TestSnapshotRefusesCurrenciesThatDifferFromLastMonth(t *testing.T) {
	lastEUR := `{"properties":{"columns":[{"name":"Cost"},{"name":"Currency"}],"rows":[[40.0,"EUR"]]}}`
	h := newHarness(t, fixtureHandler(t, map[string]respond{"lastmonth": {body: lastEUR}}), defaultCfg())
	if _, err := h.p.Snapshot(context.Background(), acct, now); err == nil || !strings.Contains(err.Error(), "more than one currency") {
		t.Errorf("err = %v", err)
	}
}

func TestSnapshotWithNoCostRows(t *testing.T) {
	empty := `{"properties":{"columns":[{"name":"Cost"},{"name":"ServiceName"},{"name":"Currency"}],"rows":[]}}`
	emptyLast := `{"properties":{"columns":[{"name":"Cost"},{"name":"Currency"}],"rows":[]}}`
	h := newHarness(t, fixtureHandler(t, map[string]respond{"services": {body: empty}, "lastmonth": {body: emptyLast}}), defaultCfg())
	s := snapshot(t, h, now)

	if s.Currency != fallbackCurrency || s.MonthToDate != 0 || s.LastMonthToDate != 0 || len(s.ByService) != 0 {
		t.Errorf("snapshot = %+v", s)
	}
}

func TestSnapshotTakesTheCurrencyFromLastMonthWhenThisMonthIsEmpty(t *testing.T) {
	empty := `{"properties":{"columns":[{"name":"Cost"},{"name":"ServiceName"},{"name":"Currency"}],"rows":[]}}`
	lastEUR := `{"properties":{"columns":[{"name":"Cost"},{"name":"Currency"}],"rows":[[40.0,"EUR"]]}}`
	h := newHarness(t, fixtureHandler(t, map[string]respond{"services": {body: empty}, "lastmonth": {body: lastEUR}}), defaultCfg())
	if s := snapshot(t, h, now); s.Currency != "EUR" || s.LastMonthToDate != 40 {
		t.Errorf("currency %q, last month %.2f", s.Currency, s.LastMonthToDate)
	}
}

func TestAccountsAuto(t *testing.T) {
	h := newHarness(t, fixtureHandler(t, nil), defaultCfg())
	accts, truncated, err := h.p.Accounts(context.Background())
	if err != nil || truncated != 0 {
		t.Fatalf("err = %v, truncated = %d", err, truncated)
	}
	var names []string
	for _, a := range accts {
		names = append(names, a.Name)
		if a.Provider != "azure" {
			t.Errorf("%+v: provider = %q", a, a.Provider)
		}
	}
	// Disabled subscriptions are dropped; the rest are sorted by name, ignoring case.
	if strings.Join(names, ",") != "alpha-lab,Office-Dev,Office-Prod" {
		t.Errorf("names = %v", names)
	}
	if accts[1].ID != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("Office-Dev id = %q", accts[1].ID)
	}
	if reqs := h.rec.all(); len(reqs) != 1 || reqs[0].RawQuery != "api-version=2022-12-01" {
		t.Errorf("requests = %+v", reqs)
	}
}

func TestAccountsFollowsPaging(t *testing.T) {
	calls := 0
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			next := "http://" + r.Host + "/subscriptions?api-version=2022-12-01&$skiptoken=x"
			fmt.Fprintf(w, `{"value":[{"subscriptionId":"b","displayName":"B","state":"Enabled"}],"nextLink":%q}`, next)
			return
		}
		fmt.Fprint(w, `{"value":[{"subscriptionId":"a","displayName":"A","state":"Enabled"}]}`)
	}), defaultCfg())

	accts, _, err := h.p.Accounts(context.Background())
	if err != nil || len(accts) != 2 || accts[0].Name != "A" || accts[1].Name != "B" {
		t.Fatalf("accts = %+v, err = %v", accts, err)
	}
	if reqs := h.rec.all(); len(reqs) != 2 || reqs[1].RawQuery != "api-version=2022-12-01&$skiptoken=x" {
		t.Errorf("requests = %+v", reqs)
	}
}

func TestAccountsTruncatesToTen(t *testing.T) {
	var items []string
	for i := 12; i >= 1; i-- { // deliberately in reverse order
		items = append(items, fmt.Sprintf(`{"subscriptionId":"id-%02d","displayName":"sub-%02d","state":"Enabled"}`, i, i))
	}
	body := `{"value":[` + strings.Join(items, ",") + `]}`
	h := newHarness(t, fixtureHandler(t, map[string]respond{"subscriptions": {body: body}}), defaultCfg())

	accts, truncated, err := h.p.Accounts(context.Background())
	if err != nil || len(accts) != 10 || truncated != 2 {
		t.Fatalf("len = %d, truncated = %d, err = %v", len(accts), truncated, err)
	}
	if accts[0].Name != "sub-01" || accts[9].Name != "sub-10" {
		t.Errorf("kept %s .. %s, want sub-01 .. sub-10", accts[0].Name, accts[9].Name)
	}
}

func TestAccountsExplicitListNeedsNoRequest(t *testing.T) {
	cfg := defaultCfg()
	cfg.Subscriptions = config.Subscriptions{List: []config.Subscription{{ID: "aaa", Name: "Prod", ResourceGroup: "rg-web"}, {ID: "bbb"}}}
	h := newHarness(t, fixtureHandler(t, nil), cfg)

	accts, truncated, err := h.p.Accounts(context.Background())
	if err != nil || truncated != 0 || len(accts) != 2 {
		t.Fatalf("accts = %+v, truncated = %d, err = %v", accts, truncated, err)
	}
	if accts[0] != (provider.Account{Provider: "azure", ID: "aaa", Name: "Prod", ResourceGroup: "rg-web"}) || accts[1].Name != "bbb" {
		t.Errorf("accts = %+v", accts)
	}
	if n := len(h.rec.all()); n != 0 {
		t.Errorf("%d requests for an explicit list", n)
	}
}

func TestAccountsError(t *testing.T) {
	h := newHarness(t, fixtureHandler(t, map[string]respond{"subscriptions": {status: 403}}), defaultCfg())
	if _, _, err := h.p.Accounts(context.Background()); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("err = %v", err)
	}
}

func rgAccount(rg string) provider.Account {
	a := acct
	a.ResourceGroup = rg
	return a
}

func TestResourceGroupScopeSendsEverythingToTheResourceGroup(t *testing.T) {
	h := newHarness(t, fixtureHandler(t, nil), defaultCfg())
	a := rgAccount("rg-web")
	s, err := h.p.Snapshot(context.Background(), a, now)
	if err != nil {
		t.Fatal(err)
	}
	if s.Account != a {
		t.Errorf("Account = %+v, want the scope it was asked for", s.Account)
	}

	base := "/subscriptions/" + acct.ID
	want := []struct{ method, path string }{
		{"POST", base + "/resourceGroups/rg-web/providers/Microsoft.CostManagement/query"},    // daily
		{"POST", base + "/resourceGroups/rg-web/providers/Microsoft.CostManagement/query"},    // last month
		{"POST", base + "/resourceGroups/rg-web/providers/Microsoft.CostManagement/forecast"}, // forecast
		{"GET", base + "/resourceGroups/rg-web/providers/Microsoft.Consumption/budgets"},      // resource-group budgets
		{"POST", base + "/providers/Microsoft.CostManagement/query"},                          // the group list is asked at subscription scope
	}
	reqs := h.rec.all()
	if len(reqs) != len(want) {
		t.Fatalf("requests = %+v", reqs)
	}
	for i, w := range want {
		if reqs[i].Method != w.method || reqs[i].Path != w.path {
			t.Errorf("request %d = %s %s, want %s %s", i, reqs[i].Method, reqs[i].Path, w.method, w.path)
		}
	}
	if len(s.ResourceGroups) != 3 {
		t.Errorf("the other resource groups should still be listed for the picker: %+v", s.ResourceGroups)
	}
}

func TestResourceGroupNamesCannotChangeTheRoute(t *testing.T) {
	h := newHarness(t, fixtureHandler(t, nil), defaultCfg())
	if _, err := h.p.Snapshot(context.Background(), rgAccount("rg x/../y"), now); err != nil {
		t.Fatal(err)
	}
	want := "/subscriptions/" + acct.ID + "/resourceGroups/rg%20x%2F..%2Fy/providers/Microsoft.CostManagement/query"
	if got := h.rec.all()[0].Escaped; got != want {
		t.Errorf("escaped path = %s\n          want   %s", got, want)
	}
}

func TestForbiddenResourceGroupNamesTheScope(t *testing.T) {
	h := newHarness(t, fixtureHandler(t, map[string]respond{"services": {status: 403}}), defaultCfg())
	_, err := h.p.Snapshot(context.Background(), rgAccount("rg-web"), now)
	var fe provider.ForbiddenError
	if !errors.As(err, &fe) || fe.AccountName != "Office-Dev / rg-web" {
		t.Errorf("err = %v, want ForbiddenError naming the resource group", err)
	}
}

func TestResourceGroupListProblemsNeverFailTheSnapshot(t *testing.T) {
	bad := map[string]respond{
		"server error":       {status: 500, body: "boom"},
		"forbidden":          {status: 403}, // a login that can only see one resource group
		"unreadable columns": {body: `{"properties":{"columns":[{"name":"Foo"}],"rows":[]}}`},
		"no rows":            {body: `{"properties":{"columns":[{"name":"Cost"},{"name":"ResourceGroupName"},{"name":"Currency"}],"rows":[]}}`},
	}
	for name, r := range bad {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, fixtureHandler(t, map[string]respond{"rgs": r}), defaultCfg())
			s := snapshot(t, h, now)
			if len(s.ResourceGroups) != 0 {
				t.Errorf("ResourceGroups = %+v, want none", s.ResourceGroups)
			}
			if !near(s.MonthToDate, 330.5) || s.Budget == nil || s.ForecastMonth == nil {
				t.Errorf("the rest of the snapshot must survive: mtd %.2f budget %+v forecast %v", s.MonthToDate, s.Budget, s.ForecastMonth)
			}
		})
	}
}

func TestResourceGroupListAcceptsTheShorterColumnName(t *testing.T) {
	body := `{"properties":{"columns":[{"name":"Cost"},{"name":"ResourceGroup"},{"name":"Currency"}],"rows":[[5.0,"rg-a","USD"]]}}`
	h := newHarness(t, fixtureHandler(t, map[string]respond{"rgs": {body: body}}), defaultCfg())
	if s := snapshot(t, h, now); len(s.ResourceGroups) != 1 || s.ResourceGroups[0].Name != "rg-a" {
		t.Errorf("ResourceGroups = %+v", s.ResourceGroups)
	}
}

func TestSubscriptionsListsEveryEnabledSubscriptionUntruncated(t *testing.T) {
	var items []string
	for i := 12; i >= 1; i-- {
		items = append(items, fmt.Sprintf(`{"subscriptionId":"id-%02d","displayName":"sub-%02d","state":"Enabled"}`, i, i))
	}
	items = append(items, `{"subscriptionId":"gone","displayName":"gone","state":"Disabled"}`)
	body := `{"value":[` + strings.Join(items, ",") + `]}`
	h := newHarness(t, fixtureHandler(t, map[string]respond{"subscriptions": {body: body}}), defaultCfg())

	all, err := h.p.Subscriptions(context.Background())
	if err != nil || len(all) != 12 || all[0].Name != "sub-01" || all[11].Name != "sub-12" {
		t.Fatalf("len = %d, err = %v", len(all), err)
	}
	// Accounts still tracks only the first ten in auto mode.
	tracked, truncated, err := h.p.Accounts(context.Background())
	if err != nil || len(tracked) != 10 || truncated != 2 {
		t.Errorf("Accounts: %d tracked, %d truncated, err %v", len(tracked), truncated, err)
	}
}
