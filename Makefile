.PHONY: build test vet fmt-check run-fake app dmg install uninstall clean

# Release numbers for the app (there is no git to derive them from). Raise BUILD for every
# installable change; raise VERSION for a release.
VERSION ?= 0.1.0
BUILD   ?= 1

APP                ?= build/CostMeter.app
INSTALL_DIR        ?= $(HOME)/Applications
LAUNCH_AGENTS_DIR  ?= $(HOME)/Library/LaunchAgents
AGENT_ID           ?= local.costmeter
AGENT_PLIST         = $(LAUNCH_AGENTS_DIR)/$(AGENT_ID).plist
UID                := $(shell id -u)

build:
	go build ./...

test:
	go test ./...

vet:
	go vet ./...

fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

run-fake:
	go run ./cmd/costmeter --fake

# app builds build/CostMeter.app: the binary, Info.plist, the icon, and an ad-hoc signature
# (no Apple developer account needed; it only has to be valid on the Mac that built it).
app:
	rm -rf $(APP)
	mkdir -p $(APP)/Contents/MacOS $(APP)/Contents/Resources
	go build -ldflags "-X costmeter/internal/ui.Version=$(VERSION)" -o $(APP)/Contents/MacOS/costmeter ./cmd/costmeter
	sed -e 's|{{VERSION}}|$(VERSION)|g' -e 's|{{BUILD}}|$(BUILD)|g' packaging/Info.plist > $(APP)/Contents/Info.plist
	iconutil -c icns packaging/icon/AppIcon.iconset -o $(APP)/Contents/Resources/AppIcon.icns
	codesign --force --deep -s - $(APP)
	@echo "built $(APP) (version $(VERSION), build $(BUILD))"

# dmg wraps the app in a disk image with an Applications shortcut, for copying to another Mac.
# It does not set up start at login (only `make install` does) and the app is still unnotarized.
DMG ?= build/CostMeter-$(VERSION).dmg
dmg: app
	rm -rf build/dmg $(DMG)
	mkdir -p build/dmg
	cp -R $(APP) build/dmg/CostMeter.app
	ln -s /Applications build/dmg/Applications
	hdiutil create -volname CostMeter -srcfolder build/dmg -ov -format UDZO $(DMG)
	rm -rf build/dmg
	@echo "built $(DMG)"

# install puts the app in ~/Applications and starts it at every login. It is safe to run
# again: the running copy is stopped first, so an update replaces it cleanly.
install: app
	-launchctl bootout gui/$(UID)/$(AGENT_ID) 2>/dev/null
	mkdir -p $(INSTALL_DIR) $(LAUNCH_AGENTS_DIR)
	rm -rf $(INSTALL_DIR)/CostMeter.app
	cp -R $(APP) $(INSTALL_DIR)/CostMeter.app
	sed -e 's|{{LABEL}}|$(AGENT_ID)|g' -e 's|{{APP}}|$(INSTALL_DIR)/CostMeter.app|g' -e 's|{{HOME}}|$(HOME)|g' \
		packaging/local.costmeter.plist > $(AGENT_PLIST)
	launchctl bootstrap gui/$(UID) $(AGENT_PLIST)
	@echo "installed $(INSTALL_DIR)/CostMeter.app and started it (it will also start at login)"

# uninstall stops CostMeter and removes the app and its login agent. Your settings, cache and
# log in ~/Library are kept.
uninstall:
	-launchctl bootout gui/$(UID)/$(AGENT_ID) 2>/dev/null
	rm -f $(AGENT_PLIST)
	rm -rf $(INSTALL_DIR)/CostMeter.app
	@echo "uninstalled CostMeter (settings and logs in ~/Library are kept)"

clean:
	rm -rf build
