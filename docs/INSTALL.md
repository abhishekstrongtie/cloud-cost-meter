# Installing CostMeter

CostMeter works with any Azure subscription your login can read costs for. It only reads cost
data (it never creates or changes anything in Azure) and keeps no secrets: it uses your own
`az login` session. If your Mac is managed by an employer, check its policy on developer tools and
self-built apps before installing.

There are two ways to install it: download the ready-made app (no Go needed), or build it from
source. Either way you need macOS 13 or newer on an Apple silicon Mac (M1 or newer). Intel Macs are not
supported.

## Option A: download the app

1. Install the Azure CLI and sign in:
   ```bash
   brew install azure-cli
   az login
   ```
2. Download `CostMeter-<version>.dmg` from the [latest release](../../../releases/latest).
3. Open the DMG and drag **CostMeter** onto **Applications**.
4. The app is ad-hoc signed, not notarized by Apple, so macOS blocks a downloaded copy as coming
   from an unidentified developer. Remove that block once:
   ```bash
   xattr -dr com.apple.quarantine /Applications/CostMeter.app
   ```
5. Open **CostMeter** from Applications. A cloud with this month's spend appears at the right of the
   menu bar. To start it at login, add it under **System Settings › General › Login Items**.

Each release also has a `.sha256` file. To check your download:
`shasum -a 256 CostMeter-<version>.dmg` should print the same value.

## Option B: build from source

This also sets up the login agent, which restarts the app after a crash.

| Tool | Why | How to get it |
|---|---|---|
| Xcode Command Line Tools | the build needs a C compiler | `xcode-select --install` |
| Go 1.27 or newer | to build the app | `brew install go` |
| Azure CLI | CostMeter signs in through it | `brew install azure-cli`, then `az login` |

### 1. Get the source

```bash
git clone https://github.com/abhishekstrongtie/cloud-cost-meter.git
cd cloud-cost-meter
```

### 2. Check that your login can read the costs

Sign in, then run the commands in [azure-endpoints.md](azure-endpoints.md). If they return data,
the app will work. Save the responses as that guide describes if you want the tests to check
Azure's real column names.

```bash
az login
make test            # all tests must pass before installing
```

### 3. Install

```bash
make install
```

This builds `CostMeter.app`, copies it to `~/Applications`, and starts it. It also registers a
login agent, so it starts every time you log in. A cloud with this month's spend appears at the
right of the menu bar. Running `make install` again is safe: it stops the running copy, replaces
it and starts the new one.

To update after changing the code, raise `BUILD` in the [Makefile](../Makefile) and run
`make install` again.

## Use it

Click the cloud. **Scope** chooses subscriptions and resource groups; **Settings** sets how often
it refreshes and how many services are listed. Everything you choose is saved automatically.

## Uninstall

```bash
make uninstall
```

This stops CostMeter and removes the app and its login agent. Your saved choices, the cache and
the log stay in `~/Library`; delete them too for a complete removal:

```bash
rm -rf ~/Library/Application\ Support/costmeter ~/Library/Caches/costmeter ~/Library/Logs/costmeter.log
```

## Giving the app to another Mac

```bash
make dmg      # builds build/CostMeter-<version>.dmg
```

Send the DMG; the other Mac follows Option A from step 3. It only runs on Apple silicon Macs.
`make app` alone builds just `build/CostMeter.app`.

## Where things are

| What | Where |
|---|---|
| The app | `~/Applications/CostMeter.app` |
| Login agent | `~/Library/LaunchAgents/local.costmeter.plist` |
| Saved choices | `~/Library/Application Support/costmeter/settings.json` |
| Cached data | `~/Library/Caches/costmeter/snapshots.json` |
| Log | `~/Library/Logs/costmeter.log` |

## Troubleshooting

| You see | What to do |
|---|---|
| The menu says **Sign in required** | Run `az login`, then choose **Refresh Now**. |
| **Azure CLI not found** | CostMeter looks in `/opt/homebrew/bin` and `/usr/local/bin`. If `az` lives elsewhere, link it: `sudo ln -s "$(which az)" /usr/local/bin/az`. |
| **No cost access on …** | Your login lacks Cost Management read access on that subscription or resource group. Ask for the Cost Management Reader role. |
| Totals are empty although the Portal shows costs | On an Enterprise Agreement an admin must enable "View charges" for subscription users. |
| **Rate limited by Azure** | It is retried at the next refresh. Choose a longer interval under **Settings › Refresh every**. |
| **Settings error: …** | The saved settings file is damaged or has an invalid value. Delete `settings.json` (see above) to return to the defaults. |
| No cloud in the menu bar | The menu bar may be full (or the notch is hiding it). Quit a few other menu bar apps. Check the log, and `launchctl print gui/$(id -u)/local.costmeter` for the agent's state. |
| `make install` says it is already running | Another copy is running. Quit it from its menu, or `make uninstall` first. |
