SIGN_ID ?= 96776319F63EFEBAFC132A30304DA2ABEA6298EA
TIMESTAMP := $(if $(filter -,$(SIGN_ID)),,--timestamp)
FRAMEWORK := Frameworks/PeepwhoCore.xcframework
BIN = $$(swift build -c release --show-bin-path)
APP := .build/Peepwho.app
DMG := .build/Peepwho.dmg
ICONSET := .build/AppIcon.iconset
ICNS := .build/AppIcon.icns

.PHONY: build test host app dmg run-host run clean

# build compiles every target in release mode.
build: $(FRAMEWORK)
	swift build -c release

# test runs the PeepwhoKit tests, which decode the core's fixtures.
test: $(FRAMEWORK)
	swift test

# host and app build each bundle by hand and sign it with the personal Developer ID, so Keychain
# items keep their access across rebuilds. SIGN_ID=- signs ad hoc for a quick loop.
host: build
	rm -rf .build/Host.app
	mkdir -p .build/Host.app/Contents/MacOS
	cp "$(BIN)/Host" .build/Host.app/Contents/MacOS/Host
	cp Sources/Host/Info.plist .build/Host.app/Contents/Info.plist
	codesign --force --options runtime $(TIMESTAMP) --entitlements Sources/Host/Host.entitlements --sign "$(SIGN_ID)" .build/Host.app

app: build $(ICNS)
	rm -rf $(APP)
	mkdir -p $(APP)/Contents/MacOS $(APP)/Contents/Resources
	cp "$(BIN)/Peepwho" $(APP)/Contents/MacOS/Peepwho
	cp Sources/Peepwho/Info.plist $(APP)/Contents/Info.plist
	cp $(ICNS) $(APP)/Contents/Resources/AppIcon.icns
	codesign --force --options runtime $(TIMESTAMP) --entitlements Sources/Peepwho/Peepwho.entitlements --sign "$(SIGN_ID)" $(APP)

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
# ticket inside the image. docs/APPS.md has the notarization steps.
dmg:
	@test -d $(APP) || { echo "missing $(APP): run make app first"; exit 1; }
	rm -rf .build/dmg $(DMG)
	mkdir -p .build/dmg
	ditto $(APP) .build/dmg/Peepwho.app
	ln -s /Applications .build/dmg/Applications
	hdiutil create -quiet -volname peepwho -srcfolder .build/dmg -format UDZO -ov $(DMG)
	$(if $(filter-out -,$(SIGN_ID)),codesign --force --sign "$(SIGN_ID)" --timestamp $(DMG))

$(FRAMEWORK):
	@echo "missing $(FRAMEWORK): run make mac from the repository root"; exit 1

run-host: host
	-pkill -x Host
	open .build/Host.app

run: app
	-pkill -x Peepwho
	open .build/Peepwho.app

clean:
	rm -rf .build Frameworks
