package ui

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"costmeter/internal/provider"
	"costmeter/internal/scheduler"
	"costmeter/internal/settings"
	"costmeter/internal/summary"
)

// How many of each kind of menu item exist. systray cannot rebuild a menu cheaply, so the
// tray creates this many up front and shows, hides and retitles them; Build never exceeds them.
const (
	MaxTop                 = 4  // header, total, change, and the note about scopes in another currency
	MaxScopes              = 10 // tracked scopes; also the limit scope.MaxTracked enforces
	MaxServiceLines        = 6  // 5 services and the "Other" bucket
	MaxBottom              = 3  // forecast, budget and over-budget warning
	MaxPickerSubscriptions = 15
	MaxPickerGroups        = 10
)

// Line is a text row in the menu.
type Line struct {
	Text     string
	Disabled bool // greyed out, not clickable
}

// Model is the whole menu, ready to be shown. A section that would be empty carries a
// placeholder line, so the separators between sections never end up next to each other.
type Model struct {
	Title      string // the text in the menu bar
	Top        []Line // header, total, change, and a note about scopes in another currency
	Scopes     []ScopeRow
	ScopesNote Line   // "+N more subscriptions" or a placeholder when there are no rows; "" = hidden
	Bottom     []Line // forecast, budget and over-budget warning
	Status     Line
	Picker     Picker
	Settings   SettingsMenu
}

// SettingsMenu is the Settings submenu: two pick-lists, each with one choice checked.
// The options are those of settings.RefreshOptions and settings.ServicesOptions, in order.
type SettingsMenu struct {
	RefreshTitle  string // e.g. "Refresh every 4 hours"
	Refresh       []SettingOption
	ServicesTitle string // e.g. "Services listed: 5"
	Services      []SettingOption
}

type SettingOption struct {
	Title   string
	Checked bool
}

// ScopeRow is one tracked scope with its submenu of services.
type ScopeRow struct {
	Title     string
	Services  []Line
	PortalURL string
}

// Picker is the Scope submenu.
type Picker struct {
	Subscriptions []PickerSub
	More          string // "+N more subscriptions" when not all fit; "" = hidden
	CanReset      bool   // scopes have been chosen, so "Track all subscriptions" is offered
}

type PickerSub struct {
	Title   string
	Options []PickerOption
	More    string // "+N more resource groups ..." when not all fit; "" = hidden
}

type PickerOption struct {
	Title    string
	Checked  bool
	Disabled bool
	Change   Change // what choosing it does
}

// Change is a choice made in the picker: track Subscription whole (ResourceGroup == ""), or
// narrowed to a resource group, or stop tracking it (Remove).
type Change struct {
	Subscription  string
	Name          string
	ResourceGroup string
	Remove        bool
}

// Input is what Build needs besides the update itself.
type Input struct {
	Now   time.Time      // for the dates in the header and the month names
	Loc   *time.Location // for the times of day; nil means the local zone
	Saved bool           // a scope selection is saved
}

const defaultRefresh = 4 * time.Hour

func Build(u scheduler.Update, in Input) Model {
	loc := in.Loc
	if loc == nil {
		loc = time.Local
	}
	now := in.Now.UTC()

	refresh := u.Config.Refresh
	if refresh <= 0 {
		refresh = defaultRefresh
	}
	maxServices := u.Config.Azure.MaxServices
	if maxServices < 1 || maxServices > MaxServiceLines-1 {
		maxServices = MaxServiceLines - 1
	}
	view := summary.Build(u.Snapshots, in.Now, refresh, summary.WithMaxServices(maxServices))

	m := Model{
		Title:    title(view, u),
		Top:      topLines(view, now),
		Bottom:   bottomLines(view, now),
		Status:   statusLine(u, loc),
		Picker:   buildPicker(u, in.Saved),
		Settings: buildSettings(u),
	}
	for i, row := range view.Accounts {
		if i == MaxScopes {
			break
		}
		m.Scopes = append(m.Scopes, scopeRow(row))
	}
	m.ScopesNote = scopesNote(m, u)
	return m
}

func title(view summary.View, u scheduler.Update) string {
	t := "…"
	if view.HasTotals {
		t = MoneyShort(view.Totals.MonthToDate, view.Totals.Currency)
	}
	if view.Stale || u.CycleErr != nil || u.ConfigErr != "" {
		t += " ⚠"
	}
	return t
}

func topLines(view summary.View, now time.Time) []Line {
	if !view.HasTotals {
		return []Line{{Text: "CostMeter", Disabled: true}}
	}
	c := view.Totals
	lastMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1).Format("January")
	lines := []Line{
		{Text: fmt.Sprintf("Month to date · %s 1–%d", now.Format("Jan"), now.Day()), Disabled: true},
		{Text: "Total  " + Money(c.MonthToDate, c.Currency)},
	}
	if c.HasChange {
		arrow := "▲"
		if c.ChangePercent < 0 {
			arrow = "▼"
		}
		lines = append(lines, Line{
			Text:     fmt.Sprintf("%s %d%% vs same day in %s", arrow, int(math.Round(math.Abs(c.ChangePercent))), lastMonth),
			Disabled: true,
		})
	}
	if n := view.OtherCurrency; n > 0 {
		// CostMeter adds amounts up as they are, so a scope in another currency is not in the total.
		text := fmt.Sprintf("⚠ %d scopes use another currency — left out of the total", n)
		if n == 1 {
			text = "⚠ 1 scope uses another currency — left out of the total"
		}
		lines = append(lines, Line{Text: text, Disabled: true})
	}
	return lines
}

func bottomLines(view summary.View, now time.Time) []Line {
	var lines []Line
	if view.HasTotals {
		c := view.Totals
		if c.HasForecast {
			text := "Forecast for " + now.Format("January") + "  " + MoneyShort(c.Forecast, c.Currency)
			if c.ForecastPartial {
				text += " (partial)"
			}
			lines = append(lines, Line{Text: text})
		}
		if c.HasBudget {
			label := "Budget " + MoneyShort(c.BudgetTotal, c.Currency)
			if c.BudgetPartial {
				label += fmt.Sprintf(" (%d of %d scopes)", c.BudgetAccounts, c.Accounts)
			}
			lines = append(lines, Line{Text: label + fmt.Sprintf(" — %d%% used", int(math.Round(c.BudgetUsagePercent)))})
		}
		if c.OverBudget > 0 {
			lines = append(lines, Line{Text: fmt.Sprintf("⚠ Forecast is %s over budget", MoneyShort(c.OverBudget, c.Currency))})
		}
	}
	if len(lines) == 0 {
		return []Line{{Text: "No forecast or budget yet", Disabled: true}}
	}
	return lines
}

func providerName(p string) string {
	switch strings.ToLower(p) {
	case "azure":
		return "Azure"
	case "aws":
		return "AWS"
	}
	return p
}

func scopeRow(row summary.AccountRow) ScopeRow {
	if row.NoData { // the first fetch failed: no amounts yet, but the row must not vanish
		return ScopeRow{
			Title:     fmt.Sprintf("%s · %s  no data ⚠", providerName(row.Account.Provider), row.Account.Label()),
			PortalURL: portalURL(row.Account),
		}
	}
	t := fmt.Sprintf("%s · %s  %s", providerName(row.Account.Provider), row.Account.Label(), Money(row.MonthToDate, row.Currency))
	if row.Stale {
		t += " ⚠"
	}
	r := ScopeRow{Title: t, PortalURL: portalURL(row.Account)}
	for _, s := range row.Services {
		r.Services = append(r.Services, Line{Text: s.Name + "  " + Money(s.Amount, row.Currency)})
	}
	return r
}

func portalURL(a provider.Account) string {
	u := "https://portal.azure.com/#resource/subscriptions/" + url.PathEscape(a.ID)
	if a.ResourceGroup != "" {
		u += "/resourceGroups/" + url.PathEscape(a.ResourceGroup)
	}
	return u + "/overview"
}

func scopesNote(m Model, u scheduler.Update) Line {
	switch {
	case len(m.Scopes) > 0 && u.Truncated > 0:
		return Line{Text: fmt.Sprintf("+%d more subscriptions — choose them under Scope", u.Truncated), Disabled: true}
	case len(m.Scopes) > 0:
		return Line{}
	case u.Refreshing || (u.LastRefresh.IsZero() && u.CycleErr == nil):
		return Line{Text: "Loading…", Disabled: true}
	case u.CycleErr != nil:
		return Line{Text: "No cost data", Disabled: true}
	}
	return Line{Text: "No subscriptions tracked — choose under Scope", Disabled: true}
}

// statusLine shows the most important thing wrong, else when the data was refreshed. Only one
// line fits: a failed sign-in comes first because nothing works without it.
func statusLine(u scheduler.Update, loc *time.Location) Line {
	switch {
	case errors.Is(u.CycleErr, provider.ErrNotAuthorized):
		return Line{Text: `Sign in required — run "az login"`, Disabled: true}
	case errors.Is(u.CycleErr, provider.ErrAzureCLIMissing):
		return Line{Text: "Azure CLI not found", Disabled: true}
	case u.CycleErr != nil:
		return Line{Text: shorten(u.CycleErr.Error()), Disabled: true}
	case u.ConfigErr != "":
		return Line{Text: "Settings error: " + shorten(strings.TrimPrefix(u.ConfigErr, "saved settings: ")), Disabled: true}
	}
	for _, s := range u.Snapshots {
		if s.Err != "" {
			return Line{Text: accountError(s), Disabled: true}
		}
	}
	switch {
	case u.Refreshing:
		return Line{Text: "Refreshing…", Disabled: true}
	case u.LastRefresh.IsZero():
		return Line{Text: "Not updated yet", Disabled: true}
	}
	return Line{Text: fmt.Sprintf("Updated %s · next %s", clock(u.LastRefresh, loc), clock(u.NextRefresh, loc)), Disabled: true}
}

func accountError(s provider.Snapshot) string {
	if strings.HasPrefix(strings.ToLower(s.Err), "no cost access on") { // provider.ForbiddenError
		return shorten(upperFirst(s.Err))
	}
	return shorten(s.Account.Label() + ": " + s.Err)
}

func upperFirst(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[size:]
}

// shorten keeps a menu line to a readable length.
func shorten(s string) string {
	const limit = 90
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit-1]) + "…"
}

func buildPicker(u scheduler.Update, saved bool) Picker {
	subs := u.Available
	if len(subs) == 0 { // before the first listing, the cache only knows the tracked scopes
		seen := map[string]bool{}
		for _, a := range u.Tracked {
			if id := strings.ToLower(a.ID); !seen[id] {
				seen[id] = true
				subs = append(subs, provider.Account{Provider: a.Provider, ID: a.ID, Name: a.Name})
			}
		}
	}

	p := Picker{CanReset: saved}
	for i, sub := range subs {
		if i == MaxPickerSubscriptions {
			p.More = fmt.Sprintf("+%d more subscriptions", len(subs)-MaxPickerSubscriptions)
			break
		}
		p.Subscriptions = append(p.Subscriptions, pickerSub(u, sub))
	}
	return p
}

func pickerSub(u scheduler.Update, sub provider.Account) PickerSub {
	var tracked bool
	var current string // the tracked resource group, "" for the whole subscription
	for _, a := range u.Tracked {
		if strings.EqualFold(a.ID, sub.ID) {
			tracked, current = true, a.ResourceGroup
		}
	}
	atLimit := !tracked && len(u.Tracked) >= MaxScopes // no room to start tracking another

	state := "not tracked"
	switch {
	case tracked && current == "":
		state = "Entire subscription"
	case tracked:
		state = current
	}
	ps := PickerSub{Title: sub.Name + " — " + state}

	ps.Options = append(ps.Options,
		PickerOption{Title: "Not tracked", Checked: !tracked, Change: Change{Subscription: sub.ID, Name: sub.Name, Remove: true}},
		PickerOption{Title: "Entire subscription", Checked: tracked && current == "", Disabled: atLimit,
			Change: Change{Subscription: sub.ID, Name: sub.Name}},
	)

	// Resource groups are known from the snapshot of whichever scope of this subscription is tracked.
	var groups []provider.ResourceGroupCost
	currency := ""
	for _, s := range u.Snapshots {
		if strings.EqualFold(s.Account.ID, sub.ID) && len(s.ResourceGroups) > 0 {
			groups, currency = s.ResourceGroups, s.Currency
			break
		}
	}

	shown := groups
	if len(shown) > MaxPickerGroups {
		shown = shown[:MaxPickerGroups]
	}
	// The group being tracked is always offered, even if it fell off the list or is unknown.
	listed := false
	for _, g := range shown {
		if current != "" && strings.EqualFold(g.Name, current) {
			listed = true
		}
	}
	extra := provider.ResourceGroupCost{}
	if current != "" && !listed {
		extra = provider.ResourceGroupCost{Name: current}
		for _, g := range groups {
			if strings.EqualFold(g.Name, current) {
				extra = g
			}
		}
		if len(shown) == MaxPickerGroups {
			shown = shown[:MaxPickerGroups-1]
		}
	}
	for _, g := range shown {
		ps.Options = append(ps.Options, groupOption(sub, g, current, currency, atLimit))
	}
	if extra.Name != "" {
		ps.Options = append(ps.Options, groupOption(sub, extra, current, currency, atLimit))
	}

	offered := len(shown)
	if extra.Name != "" && extra.Amount != 0 { // it came from the list, so it is not "more"
		offered++
	}
	if more := len(groups) - offered; more > 0 {
		ps.More = fmt.Sprintf("+%d more resource groups not shown", more)
	}
	return ps
}

func groupOption(sub provider.Account, g provider.ResourceGroupCost, current, currency string, atLimit bool) PickerOption {
	t := g.Name
	if g.Amount != 0 {
		t += "  " + Money(g.Amount, currency)
	}
	return PickerOption{
		Title:    t,
		Checked:  current != "" && strings.EqualFold(g.Name, current),
		Disabled: atLimit,
		Change:   Change{Subscription: sub.ID, Name: sub.Name, ResourceGroup: g.Name},
	}
}

// RefreshTitle names a refresh interval: "1 hour", "4 hours", or "1h30m0s" for an unusual one.
func RefreshTitle(d time.Duration) string {
	switch hours := int(d / time.Hour); {
	case d%time.Hour != 0 || hours < 1:
		return d.String()
	case hours == 1:
		return "1 hour"
	default:
		return fmt.Sprintf("%d hours", hours)
	}
}

// ServicesTitle names a services-listed choice.
func ServicesTitle(n int) string { return fmt.Sprintf("%d services", n) }

func buildSettings(u scheduler.Update) SettingsMenu {
	cfg := u.Config
	if cfg.Refresh <= 0 {
		cfg.Refresh = defaultRefresh
	}
	if cfg.Azure.MaxServices < 1 {
		cfg.Azure.MaxServices = MaxServiceLines - 1
	}
	m := SettingsMenu{
		RefreshTitle:  "Refresh every " + RefreshTitle(cfg.Refresh),
		ServicesTitle: fmt.Sprintf("Services listed: %d", cfg.Azure.MaxServices),
	}
	for _, d := range settings.RefreshOptions {
		m.Refresh = append(m.Refresh, SettingOption{Title: RefreshTitle(d), Checked: d == cfg.Refresh})
	}
	for _, n := range settings.ServicesOptions {
		m.Services = append(m.Services, SettingOption{Title: ServicesTitle(n), Checked: n == cfg.Azure.MaxServices})
	}
	return m
}
