package ui

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"costmeter/internal/config"
	"costmeter/internal/provider"
	"costmeter/internal/provider/fake"
	"costmeter/internal/scheduler"
)

var now = time.Date(2026, 10, 15, 10, 30, 0, 0, time.UTC)

const (
	prodID = "11111111-1111-1111-1111-111111111111"
	devID  = "22222222-2222-2222-2222-222222222222"
)

// baseUpdate is what the app shows right after a successful refresh of the sample data.
func baseUpdate(t *testing.T) scheduler.Update {
	t.Helper()
	ctx := context.Background()
	p := fake.New()
	tracked, _, _ := p.Accounts(ctx)
	available, _ := p.Subscriptions(ctx)
	var snaps []provider.Snapshot
	for _, a := range tracked {
		s, err := p.Snapshot(ctx, a, now)
		if err != nil {
			t.Fatal(err)
		}
		snaps = append(snaps, s)
	}
	return scheduler.Update{
		Snapshots:   snaps,
		Config:      config.Default(),
		Tracked:     tracked,
		Available:   available,
		LastRefresh: now,
		NextRefresh: now.Add(4 * time.Hour),
	}
}

func build(u scheduler.Update) Model { return Build(u, Input{Now: now, Loc: time.UTC}) }

func texts(lines []Line) []string {
	var out []string
	for _, l := range lines {
		out = append(out, l.Text)
	}
	return out
}

func rowTitles(m Model) []string {
	var out []string
	for _, r := range m.Scopes {
		out = append(out, r.Title)
	}
	return out
}

func TestMenuMatchesTheMockup(t *testing.T) {
	m := build(baseUpdate(t))

	if m.Title != "$1,284" {
		t.Errorf("Title = %q", m.Title)
	}
	wantTop := []Line{
		{"Month to date · Oct 1–15", true},
		{"Total  $1,284.20", false},
		{"▲ 12% vs same day in September", true},
	}
	if !reflect.DeepEqual(m.Top, wantTop) {
		t.Errorf("Top = %+v\nwant  %+v", m.Top, wantTop)
	}

	wantRows := []string{"Azure · Office-Prod  $1,102.40", "Azure · Office-Dev  $181.80"}
	if got := rowTitles(m); !reflect.DeepEqual(got, wantRows) {
		t.Errorf("rows = %q, want %q", got, wantRows)
	}
	wantServices := []string{
		"Virtual Machines  $540.10", "Kubernetes Service  $310.25", "Storage  $118.40",
		"SQL Database  $92.00", "Key Vault  $12.40", "Other (5 services)  $29.25",
	}
	if got := texts(m.Scopes[0].Services); !reflect.DeepEqual(got, wantServices) {
		t.Errorf("Prod services = %q\nwant %q", got, wantServices)
	}
	if want := "https://portal.azure.com/#resource/subscriptions/" + prodID + "/overview"; m.Scopes[0].PortalURL != want {
		t.Errorf("portal URL = %q", m.Scopes[0].PortalURL)
	}
	if m.ScopesNote.Text != "" {
		t.Errorf("ScopesNote = %+v, want hidden", m.ScopesNote)
	}

	wantBottom := []string{"Forecast for October  $2,450", "Budget $2,200 — 58% used", "⚠ Forecast is $250 over budget"}
	if got := texts(m.Bottom); !reflect.DeepEqual(got, wantBottom) {
		t.Errorf("Bottom = %q\nwant   %q", got, wantBottom)
	}
	if want := (Line{"Updated 10:30 · next 14:30", true}); m.Status != want {
		t.Errorf("Status = %+v, want %+v", m.Status, want)
	}
}

func TestTimesShowInTheGivenZone(t *testing.T) {
	cdt := time.FixedZone("CDT", -5*3600)
	m := Build(baseUpdate(t), Input{Now: now, Loc: cdt})
	if m.Status.Text != "Updated 05:30 · next 09:30" {
		t.Errorf("Status = %q", m.Status.Text)
	}
}

func TestChangeLineGoesDownWhenSpendFalls(t *testing.T) {
	u := baseUpdate(t)
	u.Snapshots[0].LastMonthToDate, u.Snapshots[1].LastMonthToDate = 2000, 400 // spend is below last month's
	if got := build(u).Top[2].Text; got != "▼ 46% vs same day in September" {
		t.Errorf("change line = %q", got)
	}
}

func TestChangeLineIsOmittedWithoutAComparison(t *testing.T) {
	u := baseUpdate(t)
	u.Snapshots[0].LastMonthToDate, u.Snapshots[1].LastMonthToDate = 0, 0
	if got := texts(build(u).Top); len(got) != 2 {
		t.Errorf("Top = %q, want only the header and the total", got)
	}
}

func TestTitle(t *testing.T) {
	stale := baseUpdate(t)
	stale.Snapshots[1].Stale = true
	old := baseUpdate(t)
	old.Snapshots[0].FetchedAt = now.Add(-9 * time.Hour) // older than 2 x the 4 hour refresh
	signedOut := baseUpdate(t)
	signedOut.CycleErr = provider.ErrNotAuthorized
	badConfig := baseUpdate(t)
	badConfig.ConfigErr = "saved settings: oops"

	tests := []struct {
		name string
		u    scheduler.Update
		want string
	}{
		{"loading", scheduler.Update{Refreshing: true}, "…"},
		{"normal", baseUpdate(t), "$1,284"},
		{"an account is stale", stale, "$1,284 ⚠"},
		{"data is old", old, "$1,284 ⚠"},
		{"signed out with old data", signedOut, "$1,284 ⚠"},
		{"signed out and no data", scheduler.Update{CycleErr: provider.ErrNotAuthorized}, "… ⚠"},
		{"settings error", badConfig, "$1,284 ⚠"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := build(tt.u).Title; got != tt.want {
				t.Errorf("Title = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStatusLine(t *testing.T) {
	forbidden := baseUpdate(t)
	forbidden.Snapshots[1].Stale = true
	forbidden.Snapshots[1].Err = provider.ForbiddenError{AccountName: "Office-Dev"}.Error()
	boom := baseUpdate(t)
	boom.Snapshots[0].Err = "boom"
	both := baseUpdate(t)
	both.CycleErr, both.ConfigErr = provider.ErrNotAuthorized, "saved settings: oops"
	configAndAccount := baseUpdate(t)
	configAndAccount.ConfigErr = "saved settings: refresh must be at least 1h0m0s, got 10m0s"
	configAndAccount.Snapshots[1].Err = "boom"
	refreshing := baseUpdate(t)
	refreshing.Refreshing = true
	neverUpdated := scheduler.Update{}
	long := baseUpdate(t)
	long.Snapshots[0].Err = strings.Repeat("x", 300)

	tests := []struct {
		name string
		u    scheduler.Update
		want string
	}{
		{"normal", baseUpdate(t), "Updated 10:30 · next 14:30"},
		{"signed out", func() scheduler.Update {
			u := baseUpdate(t)
			u.CycleErr = fmt.Errorf("%w: expired", provider.ErrNotAuthorized)
			return u
		}(), `Sign in required — run "az login"`},
		{"az missing", func() scheduler.Update { u := baseUpdate(t); u.CycleErr = provider.ErrAzureCLIMissing; return u }(), "Azure CLI not found"},
		{"no access to one subscription", forbidden, "No cost access on Office-Dev"},
		{"another account error names the scope", boom, "Azure · Office-Prod: boom"[len("Azure · "):]},
		{"settings error", func() scheduler.Update {
			u := baseUpdate(t)
			u.ConfigErr = "saved settings: refresh is too short"
			return u
		}(), "Settings error: refresh is too short"},
		{"sign-in beats settings error", both, `Sign in required — run "az login"`},
		{"settings error beats an account error", configAndAccount, "Settings error: refresh must be at least 1h0m0s, got 10m0s"},
		{"refreshing", refreshing, "Refreshing…"},
		{"nothing yet", neverUpdated, "Not updated yet"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := build(tt.u).Status
			if got.Text != tt.want || !got.Disabled {
				t.Errorf("Status = %+v, want disabled %q", got, tt.want)
			}
		})
	}

	if got := build(long).Status.Text; len([]rune(got)) > 90 || !strings.HasSuffix(got, "…") {
		t.Errorf("a long error must be shortened: %q", got)
	}
}

func TestUnknownCycleErrorIsShown(t *testing.T) {
	u := baseUpdate(t)
	u.CycleErr = errors.New("something unexpected")
	if got := build(u).Status.Text; got != "something unexpected" {
		t.Errorf("Status = %q", got)
	}
}

func TestResourceGroupScopesReadAsSuch(t *testing.T) {
	u := baseUpdate(t)
	p := fake.New()
	web, err := p.Snapshot(context.Background(), provider.Account{Provider: "azure", ID: prodID, Name: "Office-Prod", ResourceGroup: "rg-web"}, now)
	if err != nil {
		t.Fatal(err)
	}
	u.Snapshots[0] = web
	u.Tracked[0] = web.Account
	m := build(u)

	want := []string{"Azure · Office-Prod / rg-web  $600.00", "Azure · Office-Dev  $181.80"}
	if got := rowTitles(m); !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %q, want %q", got, want)
	}
	if got := m.Scopes[0].PortalURL; got != "https://portal.azure.com/#resource/subscriptions/"+prodID+"/resourceGroups/rg-web/overview" {
		t.Errorf("portal URL = %q", got)
	}
	if got := m.Top[1].Text; got != "Total  $781.80" {
		t.Errorf("total = %q", got)
	}
	// Only Office-Dev has a budget now ($200, $181.80 spent), so the figure is partial and the
	// over-budget amount compares Dev alone: forecast $350 against $200.
	wantBottom := []string{"Forecast for October  $2,450", "Budget $200 (1 of 2 scopes) — 91% used", "⚠ Forecast is $150 over budget"}
	if got := texts(m.Bottom); !reflect.DeepEqual(got[1:], wantBottom[1:]) {
		t.Errorf("Bottom = %q, want it to end with %q", got, wantBottom[1:])
	}
}

func TestResourceGroupNamesAreEscapedInThePortalLink(t *testing.T) {
	a := provider.Account{ID: "sub", ResourceGroup: "rg x/y"}
	if got := portalURL(a); got != "https://portal.azure.com/#resource/subscriptions/sub/resourceGroups/rg%20x%2Fy/overview" {
		t.Errorf("portalURL = %q", got)
	}
}

func TestStaleRowIsMarked(t *testing.T) {
	u := baseUpdate(t)
	u.Snapshots[1].Stale = true
	got := rowTitles(build(u))
	if got[0] != "Azure · Office-Prod  $1,102.40" || got[1] != "Azure · Office-Dev  $181.80 ⚠" {
		t.Errorf("rows = %q", got)
	}
}

func TestServiceLinesFollowMaxServices(t *testing.T) {
	u := baseUpdate(t)
	u.Config.Azure.MaxServices = 3
	got := texts(build(u).Scopes[0].Services)
	if len(got) != 4 || got[3] != "Other (7 services)  $133.65" {
		t.Errorf("services = %q", got)
	}
	u.Config.Azure.MaxServices = 0 // not set: fall back to the menu's capacity
	if got := texts(build(u).Scopes[0].Services); len(got) != 6 {
		t.Errorf("default services = %q", got)
	}
}

func TestPlaceholdersKeepEverySectionNonEmpty(t *testing.T) {
	tests := []struct {
		name      string
		u         scheduler.Update
		wantTop   string
		wantNote  string
		wantBelow string
	}{
		{"loading", scheduler.Update{Refreshing: true}, "CostMeter", "Loading…", "No forecast or budget yet"},
		{"never refreshed", scheduler.Update{}, "CostMeter", "Loading…", "No forecast or budget yet"},
		{"signed out", scheduler.Update{CycleErr: provider.ErrNotAuthorized, LastRefresh: now}, "CostMeter", "No cost data", "No forecast or budget yet"},
		{"nothing tracked", scheduler.Update{LastRefresh: now, Config: config.Default()}, "CostMeter", "No subscriptions tracked — choose under Scope", "No forecast or budget yet"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := build(tt.u)
			if len(m.Top) != 1 || m.Top[0].Text != tt.wantTop || !m.Top[0].Disabled {
				t.Errorf("Top = %+v", m.Top)
			}
			if m.ScopesNote.Text != tt.wantNote || !m.ScopesNote.Disabled {
				t.Errorf("ScopesNote = %+v, want %q", m.ScopesNote, tt.wantNote)
			}
			if len(m.Bottom) != 1 || m.Bottom[0].Text != tt.wantBelow || !m.Bottom[0].Disabled {
				t.Errorf("Bottom = %+v", m.Bottom)
			}
			if m.Status.Text == "" {
				t.Error("the status line must never be empty")
			}
		})
	}
}

func TestTruncatedSubscriptionsNote(t *testing.T) {
	u := baseUpdate(t)
	u.Truncated = 2
	if got := build(u).ScopesNote; got != (Line{"+2 more subscriptions — choose them under Scope", true}) {
		t.Errorf("ScopesNote = %+v", got)
	}
}

func TestAScopeInAnotherCurrencyIsShownButNotAddedUp(t *testing.T) {
	u := baseUpdate(t)
	u.Snapshots[1].Currency = "EUR"
	m := build(u)

	wantTop := []string{
		"Month to date · Oct 1–15",
		"Total  $1,102.40",
		"▲ 12% vs same day in September",
		"⚠ 1 scope uses another currency — left out of the total",
	}
	if got := texts(m.Top); !reflect.DeepEqual(got, wantTop) {
		t.Errorf("Top = %q\nwant %q", got, wantTop)
	}
	if m.Top[3].Text != "" && !m.Top[3].Disabled {
		t.Error("the note is information, not a button")
	}
	wantBottom := []string{"Forecast for October  $2,100", "Budget $2,000 — 55% used", "⚠ Forecast is $100 over budget"}
	if got := texts(m.Bottom); !reflect.DeepEqual(got, wantBottom) {
		t.Errorf("Bottom = %q\nwant   %q", got, wantBottom)
	}
	if got := rowTitles(m); !reflect.DeepEqual(got, []string{"Azure · Office-Prod  $1,102.40", "Azure · Office-Dev  €181.80"}) {
		t.Errorf("rows = %q; the euro scope keeps its own currency", got)
	}
	if m.Title != "$1,102" {
		t.Errorf("Title = %q", m.Title)
	}

	// Two or more are counted in the plural.
	extra := u.Snapshots[1]
	extra.Account.ID, extra.Account.Name = "ccc", "Office-QA"
	u.Snapshots = append(u.Snapshots, extra)
	u.Tracked = append(u.Tracked, extra.Account)
	if got := build(u).Top[3].Text; got != "⚠ 2 scopes use another currency — left out of the total" {
		t.Errorf("plural note = %q", got)
	}
}

func TestNothingExceedsTheMenuCapacity(t *testing.T) {
	u := baseUpdate(t)
	base := u.Snapshots[0]
	u.Snapshots, u.Tracked, u.Available = nil, nil, nil
	for i := 0; i < 12; i++ {
		s := base
		s.Account.ID = fmt.Sprintf("sub-%02d", i)
		s.Account.Name = fmt.Sprintf("Sub-%02d", i)
		s.Currency = []string{"USD", "EUR"}[i%2] // some scopes in another currency
		u.Snapshots = append(u.Snapshots, s)
		u.Tracked = append(u.Tracked, s.Account)
		u.Available = append(u.Available, provider.Account{Provider: "azure", ID: s.Account.ID, Name: s.Account.Name})
	}
	for i := 12; i < 20; i++ {
		u.Available = append(u.Available, provider.Account{Provider: "azure", ID: fmt.Sprintf("sub-%02d", i), Name: fmt.Sprintf("Sub-%02d", i)})
	}
	m := build(u)

	if len(m.Top) > MaxTop || len(m.Bottom) > MaxBottom || len(m.Scopes) > MaxScopes {
		t.Errorf("top %d/%d, bottom %d/%d, scopes %d/%d", len(m.Top), MaxTop, len(m.Bottom), MaxBottom, len(m.Scopes), MaxScopes)
	}
	for _, r := range m.Scopes {
		if len(r.Services) > MaxServiceLines {
			t.Errorf("%s has %d service lines, capacity %d", r.Title, len(r.Services), MaxServiceLines)
		}
	}
	if len(m.Picker.Subscriptions) != MaxPickerSubscriptions || m.Picker.More != "+5 more subscriptions" {
		t.Errorf("picker has %d subscriptions, More = %q", len(m.Picker.Subscriptions), m.Picker.More)
	}
	for _, ps := range m.Picker.Subscriptions {
		if len(ps.Options) > 2+MaxPickerGroups {
			t.Errorf("%s has %d options", ps.Title, len(ps.Options))
		}
	}
}

// ---- the Scope picker ----

func optionTitles(ps PickerSub) []string {
	var out []string
	for _, o := range ps.Options {
		out = append(out, o.Title)
	}
	return out
}

func checked(ps PickerSub) []string {
	var out []string
	for _, o := range ps.Options {
		if o.Checked {
			out = append(out, o.Title)
		}
	}
	return out
}

func TestPickerOffersEverySubscriptionAndItsResourceGroups(t *testing.T) {
	p := build(baseUpdate(t)).Picker
	if len(p.Subscriptions) != 2 || p.More != "" || p.CanReset {
		t.Fatalf("picker = %+v", p)
	}
	prod := p.Subscriptions[0]
	if prod.Title != "Office-Prod — Entire subscription" {
		t.Errorf("title = %q", prod.Title)
	}
	want := []string{"Not tracked", "Entire subscription", "rg-web  $600.00", "rg-data  $350.40", "rg-shared  $152.00"}
	if got := optionTitles(prod); !reflect.DeepEqual(got, want) {
		t.Errorf("options = %q\nwant    %q", got, want)
	}
	if got := checked(prod); !reflect.DeepEqual(got, []string{"Entire subscription"}) {
		t.Errorf("checked = %q", got)
	}
	if prod.Options[1].Change != (Change{Subscription: prodID, Name: "Office-Prod"}) {
		t.Errorf("Entire subscription change = %+v", prod.Options[1].Change)
	}
	if prod.Options[2].Change != (Change{Subscription: prodID, Name: "Office-Prod", ResourceGroup: "rg-web"}) {
		t.Errorf("rg-web change = %+v", prod.Options[2].Change)
	}
	if prod.Options[0].Change != (Change{Subscription: prodID, Name: "Office-Prod", Remove: true}) {
		t.Errorf("Not tracked change = %+v", prod.Options[0].Change)
	}
	for _, o := range prod.Options {
		if o.Disabled {
			t.Errorf("%q should be enabled", o.Title)
		}
	}
}

func TestPickerShowsTheTrackedResourceGroup(t *testing.T) {
	u := baseUpdate(t)
	web, _ := fake.New().Snapshot(context.Background(), provider.Account{Provider: "azure", ID: prodID, Name: "Office-Prod", ResourceGroup: "rg-web"}, now)
	u.Snapshots[0], u.Tracked[0] = web, web.Account

	prod := build(u).Picker.Subscriptions[0]
	if prod.Title != "Office-Prod — rg-web" {
		t.Errorf("title = %q", prod.Title)
	}
	if got := checked(prod); !reflect.DeepEqual(got, []string{"rg-web  $600.00"}) {
		t.Errorf("checked = %q; the other groups must stay listed", got)
	}
	if len(prod.Options) != 5 {
		t.Errorf("options = %q", optionTitles(prod))
	}
}

func TestPickerForAnUntrackedSubscription(t *testing.T) {
	u := baseUpdate(t)
	u.Tracked, u.Snapshots = u.Tracked[:1], u.Snapshots[:1] // Office-Dev is no longer tracked

	dev := build(u).Picker.Subscriptions[1]
	if dev.Title != "Office-Dev — not tracked" {
		t.Errorf("title = %q", dev.Title)
	}
	if got := optionTitles(dev); !reflect.DeepEqual(got, []string{"Not tracked", "Entire subscription"}) {
		t.Errorf("options = %q; resource groups are only known once a subscription is tracked", got)
	}
	if got := checked(dev); !reflect.DeepEqual(got, []string{"Not tracked"}) {
		t.Errorf("checked = %q", got)
	}
}

func TestPickerAtTheTrackingLimit(t *testing.T) {
	u := baseUpdate(t)
	u.Tracked = nil
	for i := 0; i < MaxScopes; i++ {
		u.Tracked = append(u.Tracked, provider.Account{Provider: "azure", ID: fmt.Sprintf("other-%d", i), Name: "Other"})
	}
	u.Tracked[0] = provider.Account{Provider: "azure", ID: prodID, Name: "Office-Prod"} // Prod is among the ten

	p := build(u).Picker
	prod, dev := p.Subscriptions[0], p.Subscriptions[1]
	for _, o := range prod.Options {
		if o.Disabled {
			t.Errorf("a tracked subscription can always be changed: %q is disabled", o.Title)
		}
	}
	if dev.Options[0].Disabled || !dev.Options[0].Checked {
		t.Errorf("Not tracked must stay available: %+v", dev.Options[0])
	}
	if !dev.Options[1].Disabled {
		t.Errorf("starting to track an 11th subscription must be disabled: %+v", dev.Options[1])
	}
}

func TestPickerListsTheTenBiggestGroupsAndAlwaysTheTrackedOne(t *testing.T) {
	u := baseUpdate(t)
	var groups []provider.ResourceGroupCost
	for i := 0; i < 13; i++ {
		groups = append(groups, provider.ResourceGroupCost{Name: fmt.Sprintf("rg-%02d", i), Amount: float64(1000 - i*10)})
	}
	u.Snapshots[0].ResourceGroups = groups

	t.Run("none tracked: ten shown, the rest counted", func(t *testing.T) {
		prod := build(u).Picker.Subscriptions[0]
		if len(prod.Options) != 2+MaxPickerGroups || prod.More != "+3 more resource groups not shown" {
			t.Errorf("%d options, More = %q", len(prod.Options), prod.More)
		}
		if prod.Options[2].Title != "rg-00  $1,000.00" || prod.Options[11].Title != "rg-09  $910.00" {
			t.Errorf("first/last group = %q / %q", prod.Options[2].Title, prod.Options[11].Title)
		}
	})

	t.Run("a tracked group that fell off the list is still offered", func(t *testing.T) {
		u := u
		u.Tracked = append([]provider.Account(nil), u.Tracked...)
		u.Tracked[0].ResourceGroup = "rg-12" // the cheapest one, beyond the top ten
		prod := build(u).Picker.Subscriptions[0]
		if len(prod.Options) != 2+MaxPickerGroups {
			t.Fatalf("%d options", len(prod.Options))
		}
		if got := checked(prod); !reflect.DeepEqual(got, []string{"rg-12  $880.00"}) {
			t.Errorf("checked = %q", got)
		}
		if prod.More != "+3 more resource groups not shown" {
			t.Errorf("More = %q (rg-09 dropped to make room, rg-10 and rg-11 were never shown)", prod.More)
		}
	})

	t.Run("a tracked group Azure no longer lists", func(t *testing.T) {
		u := u
		u.Tracked = append([]provider.Account(nil), u.Tracked...)
		u.Tracked[0].ResourceGroup = "rg-new"
		prod := build(u).Picker.Subscriptions[0]
		if got := checked(prod); !reflect.DeepEqual(got, []string{"rg-new"}) {
			t.Errorf("checked = %q; it has no amount to show", got)
		}
		if prod.Title != "Office-Prod — rg-new" {
			t.Errorf("title = %q", prod.Title)
		}
	})
}

func TestPickerBeforeTheFirstListingUsesTheTrackedScopes(t *testing.T) {
	u := baseUpdate(t)
	u.Available = nil // only the cache has been read so far
	p := build(u).Picker
	if len(p.Subscriptions) != 2 || p.Subscriptions[1].Title != "Office-Dev — Entire subscription" {
		t.Errorf("picker = %+v", p)
	}
}

func TestPickerOffersResetOnlyWithASavedSelection(t *testing.T) {
	u := baseUpdate(t)
	if Build(u, Input{Now: now, Loc: time.UTC, Saved: false}).Picker.CanReset {
		t.Error("nothing is saved, so there is nothing to reset")
	}
	if !Build(u, Input{Now: now, Loc: time.UTC, Saved: true}).Picker.CanReset {
		t.Error("a saved selection must be resettable")
	}
}

// Picking a resource group you cannot read must not make the scope disappear: it stays as a
// row with the reason, and the picker still offers the way back.
func TestAScopeThatCannotBeReadStaysVisible(t *testing.T) {
	u := baseUpdate(t)
	denied := provider.Account{Provider: "azure", ID: devID, Name: "Office-Dev", ResourceGroup: "rg-private"}
	u.Tracked[1] = denied
	u.Snapshots[1] = provider.Snapshot{Account: denied, Stale: true, Err: provider.ForbiddenError{AccountName: denied.Label()}.Error()}
	m := build(u)

	want := []string{"Azure · Office-Prod  $1,102.40", "Azure · Office-Dev / rg-private  no data ⚠"}
	if got := rowTitles(m); !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %q, want %q", got, want)
	}
	if len(m.Scopes[1].Services) != 0 || !strings.HasSuffix(m.Scopes[1].PortalURL, "/resourceGroups/rg-private/overview") {
		t.Errorf("the placeholder has no services but still links to the portal: %+v", m.Scopes[1])
	}
	if m.Status.Text != "No cost access on Office-Dev / rg-private" {
		t.Errorf("Status = %q", m.Status.Text)
	}
	if m.Title != "$1,102 ⚠" {
		t.Errorf("Title = %q; only fetched data counts", m.Title)
	}
	if got := m.Top[1].Text; got != "Total  $1,102.40" {
		t.Errorf("total = %q", got)
	}

	dev := m.Picker.Subscriptions[1]
	if dev.Title != "Office-Dev — rg-private" {
		t.Errorf("picker title = %q", dev.Title)
	}
	if got := checked(dev); !reflect.DeepEqual(got, []string{"rg-private"}) {
		t.Errorf("checked = %q; the unreadable choice must still be shown as current", got)
	}
	if got := optionTitles(dev); !reflect.DeepEqual(got, []string{"Not tracked", "Entire subscription", "rg-private"}) {
		t.Errorf("options = %q; the way back to the whole subscription must be offered", got)
	}
}

func TestNothingButAnUnreadableScope(t *testing.T) {
	denied := provider.Account{Provider: "azure", ID: devID, Name: "Office-Dev", ResourceGroup: "rg-private"}
	u := scheduler.Update{
		Snapshots: []provider.Snapshot{{Account: denied, Stale: true, Err: "boom"}},
		Tracked:   []provider.Account{denied},
		Config:    config.Default(), LastRefresh: now,
	}
	m := build(u)
	if m.Title != "… ⚠" || len(m.Scopes) != 1 || m.ScopesNote.Text != "" {
		t.Errorf("title %q, rows %d, note %q", m.Title, len(m.Scopes), m.ScopesNote.Text)
	}
	if m.Top[0].Text != "CostMeter" || m.Status.Text != "Office-Dev / rg-private: boom" {
		t.Errorf("top %q, status %q", m.Top[0].Text, m.Status.Text)
	}
}

// ---- the Settings submenu ----

func settingTitles(opts []SettingOption) []string {
	var out []string
	for _, o := range opts {
		out = append(out, o.Title)
	}
	return out
}

func settingChecked(opts []SettingOption) []string {
	var out []string
	for _, o := range opts {
		if o.Checked {
			out = append(out, o.Title)
		}
	}
	return out
}

func TestSettingsMenuShowsTheCurrentChoices(t *testing.T) {
	m := build(baseUpdate(t)).Settings // refresh every 4 hours, 5 services

	if want := []string{"1 hour", "2 hours", "4 hours", "8 hours", "12 hours", "24 hours"}; !reflect.DeepEqual(settingTitles(m.Refresh), want) {
		t.Errorf("refresh options = %q", settingTitles(m.Refresh))
	}
	if got := settingChecked(m.Refresh); !reflect.DeepEqual(got, []string{"4 hours"}) {
		t.Errorf("refresh checked = %q", got)
	}
	if want := []string{"3 services", "4 services", "5 services"}; !reflect.DeepEqual(settingTitles(m.Services), want) {
		t.Errorf("services options = %q", settingTitles(m.Services))
	}
	if got := settingChecked(m.Services); !reflect.DeepEqual(got, []string{"5 services"}) {
		t.Errorf("services checked = %q", got)
	}
	if m.RefreshTitle != "Refresh every 4 hours" || m.ServicesTitle != "Services listed: 5" {
		t.Errorf("titles = %q / %q", m.RefreshTitle, m.ServicesTitle)
	}
}

func TestSettingsMenuFollowsTheConfigInUse(t *testing.T) {
	u := baseUpdate(t)
	u.Config.Refresh = 2 * time.Hour
	u.Config.Azure.MaxServices = 3
	m := build(u).Settings
	if got := settingChecked(m.Refresh); !reflect.DeepEqual(got, []string{"2 hours"}) || m.RefreshTitle != "Refresh every 2 hours" {
		t.Errorf("refresh checked %q, title %q", got, m.RefreshTitle)
	}
	if got := settingChecked(m.Services); !reflect.DeepEqual(got, []string{"3 services"}) || m.ServicesTitle != "Services listed: 3" {
		t.Errorf("services checked %q, title %q", got, m.ServicesTitle)
	}
}

func TestAnUnusualRefreshIntervalChecksNothingButIsStillNamed(t *testing.T) {
	u := baseUpdate(t)
	u.Config.Refresh = 90 * time.Minute // only possible by editing the settings file by hand
	m := build(u).Settings
	if got := settingChecked(m.Refresh); len(got) != 0 {
		t.Errorf("checked = %q", got)
	}
	if m.RefreshTitle != "Refresh every 1h30m0s" {
		t.Errorf("title = %q", m.RefreshTitle)
	}
}

func TestSettingsMenuBeforeAnyUpdateFallsBackToTheDefaults(t *testing.T) {
	m := build(scheduler.Update{}).Settings
	if got := settingChecked(m.Refresh); !reflect.DeepEqual(got, []string{"4 hours"}) {
		t.Errorf("refresh checked = %q", got)
	}
	if got := settingChecked(m.Services); !reflect.DeepEqual(got, []string{"5 services"}) {
		t.Errorf("services checked = %q", got)
	}
}

func TestRefreshTitle(t *testing.T) {
	tests := map[time.Duration]string{
		time.Hour: "1 hour", 2 * time.Hour: "2 hours", 24 * time.Hour: "24 hours",
		90 * time.Minute: "1h30m0s", 30 * time.Minute: "30m0s", 0: "0s",
	}
	for d, want := range tests {
		if got := RefreshTitle(d); got != want {
			t.Errorf("RefreshTitle(%v) = %q, want %q", d, got, want)
		}
	}
}
