SIGN_ID ?= 96776319F63EFEBAFC132A30304DA2ABEA6298EA
TIMESTAMP := $(if $(filter -,$(SIGN_ID)),,--timestamp)
FRAMEWORK := Frameworks/WhoToAskCore.xcframework
ARCHS := arm64 x86_64
MACOS_MIN := 14.0
VERSION ?= $(shell git describe --tags --abbrev=0 --match 'v*' 2>/dev/null | sed 's/^v//')
APP := .build/WhoToAsk.app
DMG := .build/WhoToAsk.dmg
ICONSET := .build/AppIcon.iconset
ICNS := .build/AppIcon.icns
SAMPLE := .build/SampleCompany
TEAM_ID := U7L4P2YHWJ
BUNDLE_ID := com.dcadolph.whotoask
STORE_SIGN_ID ?= 23FB03D5E7DC84FDEF8DE147EEDC182E0F79C745
INSTALLER_ID ?= EC0D8D4059738272490666904F66A5027AA4942E
PROFILE ?=
BUILD ?=
PKG := .build/WhoToAsk.pkg
STORE_ENTITLEMENTS := .build/WhoToAsk-appstore.entitlements

.PHONY: build test host app verify appstore dmg run-host run clean

# fat joins one product's per-architecture builds into one binary at the given path.
fat = lipo -create -output $(2) $(foreach arch,$(ARCHS),.build/arch/$(arch)/$(1))

# build compiles every target in release mode once per architecture. Passing both to one
# swift build cannot link the core's Objective-C classes. The build engine writes every
# architecture to the one folder --show-bin-path names, so each architecture's products are
# copied aside into a fresh folder before the next build overwrites them. Reading fixed paths
# instead once lipoed a morning's leftover binaries into a new app. strip -S drops the debug
# stabs, which name every source and object file by its path on the build machine, and keeps
# the symbol table crash reports read.
build: $(FRAMEWORK)
	for arch in $(ARCHS); do \
		swift build -c release --arch $$arch || exit 1; \
		bin=$$(swift build -c release --arch $$arch --show-bin-path) || exit 1; \
		rm -rf .build/arch/$$arch && mkdir -p .build/arch/$$arch || exit 1; \
		cp "$$bin/WhoToAsk" "$$bin/Host" .build/arch/$$arch/ || exit 1; \
		strip -S .build/arch/$$arch/WhoToAsk .build/arch/$$arch/Host || exit 1; \
		lipo "$$bin/WhoToAsk" -verify_arch $$arch || exit 1; \
	done

# test runs the WhoToAskKit tests, which decode the core's fixtures.
test: $(FRAMEWORK)
	swift test

# host and app build each bundle by hand and sign it with the personal Developer ID, so Keychain
# items keep their access across rebuilds. SIGN_ID=- signs ad hoc for a quick loop.
host: build
	rm -rf .build/Host.app
	mkdir -p .build/Host.app/Contents/MacOS
	$(call fat,Host,.build/Host.app/Contents/MacOS/Host)
	cp Sources/Host/Info.plist .build/Host.app/Contents/Info.plist
	codesign --force --options runtime $(TIMESTAMP) --entitlements Sources/Host/Host.entitlements --sign "$(SIGN_ID)" .build/Host.app

app: build $(ICNS)
	rm -rf $(APP)
	mkdir -p $(APP)/Contents/MacOS $(APP)/Contents/Resources
	$(call fat,WhoToAsk,$(APP)/Contents/MacOS/WhoToAsk)
	cp Sources/WhoToAsk/Info.plist $(APP)/Contents/Info.plist
	plutil -replace CFBundleShortVersionString -string "$(VERSION)" $(APP)/Contents/Info.plist
	cp $(ICNS) $(APP)/Contents/Resources/AppIcon.icns
	rm -rf $(SAMPLE)
	cd .. && go run . demo --big --save-index mac/$(SAMPLE)
	mkdir -p $(APP)/Contents/Resources/SampleCompany
	install -m 644 $(SAMPLE)/index.json $(SAMPLE)/episodes.json $(APP)/Contents/Resources/SampleCompany/
	codesign --force --options runtime $(TIMESTAMP) --entitlements Sources/WhoToAsk/WhoToAsk.entitlements --sign "$(SIGN_ID)" $(APP)
	$(MAKE) verify

# verify fails on a built app that is not the shape a person can install.
verify:
	sh verify-bundle.sh $(APP) "$(VERSION)" $(MACOS_MIN)

# appstore builds the Mac App Store package, signed with the team's Apple Distribution and Mac
# Installer Distribution identities. PROFILE is the downloaded Mac App Store Connect profile and
# BUILD is the CFBundleVersion, which must rise with every upload. The profile is checked before
# anything is built. Downloaded profiles carry a quarantine attribute that App Store processing
# refuses, so the bundle is cleared of extended attributes before the final signature. The store
# entitlements are the app's own four plus the application and team identifiers the profile
# grants. Export compliance is answered in App Store Connect per build, not in Info.plist.
appstore:
	@echo "$(BUILD)" | grep -Eqx '[0-9]+' || { echo "BUILD=<build number, higher than the last upload> is required" >&2; exit 1; }
	sh check-profile.sh "$(PROFILE)" $(TEAM_ID) $(BUNDLE_ID)
	$(MAKE) app SIGN_ID=$(STORE_SIGN_ID)
	plutil -replace CFBundleVersion -string "$(BUILD)" $(APP)/Contents/Info.plist
	cp "$(PROFILE)" $(APP)/Contents/embedded.provisionprofile
	xattr -cr $(APP)
	cp Sources/WhoToAsk/WhoToAsk.entitlements $(STORE_ENTITLEMENTS)
	/usr/libexec/PlistBuddy -c "Add :com.apple.application-identifier string $(TEAM_ID).$(BUNDLE_ID)" $(STORE_ENTITLEMENTS)
	/usr/libexec/PlistBuddy -c "Add :com.apple.developer.team-identifier string $(TEAM_ID)" $(STORE_ENTITLEMENTS)
	codesign --force --options runtime --timestamp --entitlements $(STORE_ENTITLEMENTS) --sign "$(STORE_SIGN_ID)" $(APP)
	$(MAKE) verify
	@codesign -dv $(APP) 2>&1 | grep -qx 'TeamIdentifier=$(TEAM_ID)' || { echo "store app is not signed by team $(TEAM_ID)" >&2; exit 1; }
	rm -f $(PKG)
	productbuild --component $(APP) /Applications --sign "$(INSTALLER_ID)" $(PKG)
	@pkgutil --check-signature $(PKG) | grep -q '($(TEAM_ID))' || { echo "$(PKG) is not signed by team $(TEAM_ID)" >&2; exit 1; }
	@echo "appstore: $(PKG) ready for Transporter"

# The sample company is built from the simulated company at app build time and bundled as data,
# so the app links no simorg. The sources sidecar stays out since a sample never merges.

# The app icon is the site's favicon tile inset to the macOS icon grid, rendered at build time so
# no binary art is committed.
$(ICNS): ../site/favicon.svg
	rm -rf $(ICONSET)
	mkdir -p $(ICONSET)
	{ echo '<svg xmlns="http://www.w3.org/2000/svg" width="1024" height="1024" viewBox="0 0 1024 1024"><g transform="translate(100 100) scale(0.8046875)">'; \
		sed -e '1d' -e '$$d' ../site/favicon.svg; echo '</g></svg>'; } > .build/AppIcon.svg
	sips -s format png .build/AppIcon.svg --out .build/AppIcon-1024.png >/dev/null
	for size in 16 32 128 256 512; do \
		sips -z $$size $$size .build/AppIcon-1024.png --out $(ICONSET)/icon_$${size}x$${size}.png >/dev/null; \
		sips -z $$((size * 2)) $$((size * 2)) .build/AppIcon-1024.png --out $(ICONSET)/icon_$${size}x$${size}@2x.png >/dev/null; \
	done
	iconutil -c icns $(ICONSET) -o $(ICNS)

# dmg packs the app already built beside an Applications link with the built-in hdiutil and
# signs the image. It never rebuilds the app, so an app notarized and stapled first keeps its
# ticket inside the image. docs/APPS.md has the notarization steps. It refuses unless the tree
# sits clean on the tag the app was stamped with, so a shipped version names the code inside it.
dmg:
	@test -d $(APP) || { echo "missing $(APP): run make app first"; exit 1; }
	@stamped="v$$(plutil -extract CFBundleShortVersionString raw -o - $(APP)/Contents/Info.plist)"; \
		here="$$(git describe --tags --exact-match --match 'v*' --dirty 2>/dev/null)"; \
		test "$$here" = "$$stamped" || { echo "the app says $$stamped but the tree is at '$$here': tag, then make app"; exit 1; }
	rm -rf .build/dmg $(DMG)
	mkdir -p .build/dmg
	ditto $(APP) .build/dmg/WhoToAsk.app
	ln -s /Applications .build/dmg/Applications
	hdiutil create -quiet -volname whotoask -srcfolder .build/dmg -format UDZO -ov $(DMG)
	$(if $(filter-out -,$(SIGN_ID)),codesign --force --sign "$(SIGN_ID)" --timestamp $(DMG))

$(FRAMEWORK):
	@echo "missing $(FRAMEWORK): run make mac from the repository root"; exit 1

run-host: host
	-pkill -x Host
	open .build/Host.app

run: app
	-pkill -x WhoToAsk
	open .build/WhoToAsk.app

clean:
	rm -rf .build Frameworks
