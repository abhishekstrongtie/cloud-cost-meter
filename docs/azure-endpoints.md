# Checking the Azure endpoints by hand

CostMeter makes six read-only calls per tracked scope. These are the same calls as `az rest`
commands, so you can confirm on your Mac that your login can make them, and capture real
responses to test the parser against.

All dates are **UTC**, because that is what Azure's cost data uses. Compare any total with the
Azure Portal's Cost analysis for the same UTC date range.

## 0. Sign in and pick a subscription

```bash
az login
az account list --query "[].{name:name, id:id, state:state}" -o table
SUB=<subscription id from the table>
```

## 1. Date windows

The same windows the app uses. Works in zsh and bash on macOS (BSD `date`). Set `ASOF=2026-10-05`
first if you want to reproduce a specific day instead of today.

```bash
ASOF=${ASOF:-$(date -u +%Y-%m-%d)}
# d <+format> [-v adjustments...]: ASOF adjusted in the order given, printed with the format
d() { local fmt=$1; shift; date -u -j "$@" -f %Y-%m-%d "$ASOF" "$fmt"; }

TODAY=$ASOF
MONTH_START=$(d +%Y-%m-%d -v1d)

PREV_START=$(d +%Y-%m-%d -v1d -v-1m)
PREV_DAYS=$(d +%d -v1d -v-1d)              # days in the previous month
DAY=$(d +%d)
PREV_TO=${PREV_START:0:8}$(printf '%02d' $(( 10#$DAY < 10#$PREV_DAYS ? 10#$DAY : 10#$PREV_DAYS )))

TOMORROW=$(d +%Y-%m-%d -v+1d)
MONTH_END=$(d +%Y-%m-%d -v1d -v+1m -v-1d)

echo "month $MONTH_START..$TODAY | last month $PREV_START..$PREV_TO | forecast $TOMORROW..$MONTH_END"
```

## 2. The six calls

Each command prints JSON. Add `> file.json` to save it (see section 3).

### a. Subscriptions (`Accounts`, when `subscriptions: auto`)

```bash
az rest --method get \
  --url "https://management.azure.com/subscriptions?api-version=2022-12-01"
```

### b. Cost by service this month (month to date and the services list)

```bash
cat > /tmp/costmeter-services.json <<EOF
{"type":"ActualCost","timeframe":"Custom",
 "timePeriod":{"from":"${MONTH_START}T00:00:00Z","to":"${TODAY}T23:59:59Z"},
 "dataset":{"granularity":"None",
   "aggregation":{"totalCost":{"name":"Cost","function":"Sum"}},
   "grouping":[{"type":"Dimension","name":"ServiceName"}]}}
EOF
az rest --method post \
  --url "https://management.azure.com/subscriptions/$SUB/providers/Microsoft.CostManagement/query?api-version=2023-11-01" \
  --body @/tmp/costmeter-services.json
```

Expect `properties.columns` to include `Cost` (or `PreTaxCost`), `ServiceName` and `Currency`, and
one row per service. If `properties.nextLink` is not null there are more pages; the app follows
it automatically.

### c. Last month, same days (the "vs last month" figure)

```bash
cat > /tmp/costmeter-lastmonth.json <<EOF
{"type":"ActualCost","timeframe":"Custom",
 "timePeriod":{"from":"${PREV_START}T00:00:00Z","to":"${PREV_TO}T23:59:59Z"},
 "dataset":{"granularity":"None",
   "aggregation":{"totalCost":{"name":"Cost","function":"Sum"}}}}
EOF
az rest --method post \
  --url "https://management.azure.com/subscriptions/$SUB/providers/Microsoft.CostManagement/query?api-version=2023-11-01" \
  --body @/tmp/costmeter-lastmonth.json
```

Expect a single row with `Cost` and `Currency`.

### d. Forecast for the rest of the month

Skip this one on the last day of the month (`TODAY` equal to `MONTH_END`): the app does too.

```bash
cat > /tmp/costmeter-forecast.json <<EOF
{"type":"ActualCost","timeframe":"Custom",
 "timePeriod":{"from":"${TOMORROW}T00:00:00Z","to":"${MONTH_END}T23:59:59Z"},
 "dataset":{"granularity":"Daily",
   "aggregation":{"totalCost":{"name":"Cost","function":"Sum"}}},
 "includeActualCost":false,"includeFreshPartialCost":false}
EOF
az rest --method post \
  --url "https://management.azure.com/subscriptions/$SUB/providers/Microsoft.CostManagement/forecast?api-version=2023-11-01" \
  --body @/tmp/costmeter-forecast.json
```

Expect rows with `CostStatus` = `Forecast`. The app adds them to the month to date. An empty
result or HTTP 204 is fine: the menu then simply shows no forecast.

### e. Budgets

```bash
az rest --method get \
  --url "https://management.azure.com/subscriptions/$SUB/providers/Microsoft.Consumption/budgets?api-version=2023-05-01"
```

Only budgets with `properties.timeGrain` = `Monthly` are used. `properties.currentSpend.amount` is
the spend Azure counts against the budget. No budgets at all is fine.

### f. Resource groups with spend (feeds the scope picker)

The menu's **Scope** picker offers the resource groups that have cost since the start of last
month. They come from Azure's cost data, grouped by resource group, always asked at
**subscription** scope (even when you track a single resource group).

```bash
cat > /tmp/costmeter-rgs.json <<EOF
{"type":"ActualCost","timeframe":"Custom",
 "timePeriod":{"from":"${PREV_START}T00:00:00Z","to":"${TODAY}T23:59:59Z"},
 "dataset":{"granularity":"None",
   "aggregation":{"totalCost":{"name":"Cost","function":"Sum"}},
   "grouping":[{"type":"Dimension","name":"ResourceGroupName"}]}}
EOF
az rest --method post \
  --url "https://management.azure.com/subscriptions/$SUB/providers/Microsoft.CostManagement/query?api-version=2023-11-01" \
  --body @/tmp/costmeter-rgs.json
```

Expect columns `Cost`, `ResourceGroupName` (the app also accepts `ResourceGroup`) and `Currency`,
one row per group. Azure often returns the names in lower case, and one row may have an empty
name: that is cost that belongs to no resource group, and the app ignores it. A login that can
only see one resource group gets an authorization error here; the app copes and simply offers no
list, so the Scope menu offers only the whole subscription (the Contributor-on-the-subscription login does not have this problem).

### Narrowing b–e to one resource group

When you track a single resource group, the app sends calls b, c, d and e to the resource group
instead of the subscription: insert `/resourceGroups/<name>` after the subscription id. For
example call b becomes

```bash
RG=<resource group name>
az rest --method post \
  --url "https://management.azure.com/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.CostManagement/query?api-version=2023-11-01" \
  --body @/tmp/costmeter-services.json
```

Call e at that scope lists budgets set on the resource group itself, which is usually none; a
subscription budget does not apply to a single resource group, so no budget is shown for it.

## 3. Saving responses for the tests

Real responses let the parser tests run against Azure's actual column names. Save them into
`internal/azure/testdata/real/`, named by what they are. **The file name prefix decides how the
test reads the file**, so it must be one of:

| Call | File name |
|---|---|
| a | `subscriptions_<anything>.json` |
| b, c, f | `query_<anything>.json` (use `query_rgs_<anything>.json` for f) |
| d | `forecast_<anything>.json` |
| e | `budgets_<anything>.json` |

Replace subscription and tenant GUIDs before saving, and look the file over (subscription display
names and costs are real):

```bash
az rest ... | sed -E 's/[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}/00000000-0000-0000-0000-000000000000/g' \
  > internal/azure/testdata/real/query_services_prod.json
```

Then `go test ./internal/azure/...` checks only structure (the file parses, the columns are found,
the total is not negative), so it cannot break on amounts.

## 4. When something is wrong

| You see | Likely cause |
|---|---|
| `AuthorizationFailed` / HTTP 403 | The login has no Cost Management access on this subscription. |
| Empty `rows` although the Portal shows costs | On an Enterprise Agreement an admin must enable "View charges" for subscription users. |
| HTTP 429 | Rate limited. Look at the `x-ms-ratelimit-*-retry-after` headers (`az rest --verbose`); raise `refresh` in the config. |
| The Portal total differs slightly | The Portal may show a different time zone or currency view; use "Actual cost" and the same UTC dates. |
