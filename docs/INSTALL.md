# Installing CostMeter on the office Mac

CostMeter only reads cost data (it never creates or changes anything in Azure) and keeps no
secrets: it uses your own `az login` session. Self-built apps and developer tools may need
approval, so check your company's policy before installing.

## What you need

| Tool | Why | How to get it |
|---|---|---|
| Xcode Command Line Tools | the build needs a C compiler | `xcode-select --install` |
| Go 1.27 or newer | to build the app | `brew install go` |
| Azure CLI | CostMeter signs in through it | `brew install azure-cli`, then `az login` |

## 1. Get the source onto the Mac

There is no git remote. On the Mac where the code lives:

```bash
cd ~/Desktop/Projects
zip -r costmeter.zip costmeter -x 'costmeter/build/*'
```

Copy `costmeter.zip` over (scp, a shared drive, AirDrop) and unzip it. Alternatively build the app
on the first Mac and copy only the finished app (see "Copying a built app" below).

## 2. Check that your login can read the costs

Sign in, then run the commands in [azure-endpoints.md](azure-endpoints.md). If they return data,
the app will work. Save the responses as that guide describes if you want the tests to check
Azure's real column names.

```bash
az login
cd costmeter
make test            # all tests must pass before installing
```

## 3. Install

```bash
make install
```

This builds `CostMeter.app`, copies it to `~/Applications`, and starts it. It also registers a
login agent, so it starts every time you log in. A cloud with this month's spend appears at the
right of the menu bar. Running `make install` again is safe: it stops the running copy, replaces
it and starts the new one.

To update after changing the code, raise `BUILD` in the [Makefile](../Makefile) and run
`make install` again.

## 4. Use it

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

## Copying a built app instead of building

```bash
make app
ditto -c -k --keepParent build/CostMeter.app CostMeter.zip
```

On the other Mac, unzip it and drag `CostMeter.app` to `~/Applications`. If macOS says it is from
an unidentified developer, run `xattr -dr com.apple.quarantine ~/Applications/CostMeter.app`. A
built app only runs on the same kind of Mac (Apple silicon or Intel) it was built on. Add it to
**System Settings › General › Login Items** to start it at login (this route does not set up the
automatic restart after a crash that `make install` does).

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
