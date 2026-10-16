package ui

import "strings"

// Version is the release number shown in About. `make app` sets it from the Makefile with
// -ldflags "-X costmeter/internal/ui.Version=...". A plain `go run` leaves it as "dev".
var Version = "dev"

// Developer is who the About window credits.
const Developer = "Abhishek Bhogal"

// AboutText is the body of the About window.
func AboutText(version string) string {
	if version == "" {
		version = "dev"
	}
	return strings.Join([]string{
		"CostMeter " + version,
		"",
		"Your Azure spend, always in the menu bar.",
		"Shows this month's cost, the forecast and your budgets for the subscriptions",
		"and resource groups you choose.",
		"",
		"Read-only: it never changes anything in Azure and stores no secrets.",
		"It signs in through your own Azure CLI login (az login).",
		"",
		"Built by " + Developer + ".",
	}, "\n")
}
