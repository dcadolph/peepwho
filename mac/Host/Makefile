APP := .build/Host.app
SIGN_ID ?= 96776319F63EFEBAFC132A30304DA2ABEA6298EA
ENTITLEMENTS ?= Host.entitlements
FRAMEWORK := Frameworks/PeepwhoCore.xcframework

.PHONY: app run clean

# app builds the host bundle by hand and signs it with the personal Developer ID, so Keychain
# items keep their access across rebuilds. SIGN_ID=- signs ad hoc for a quick loop.
app: $(FRAMEWORK)
	swift build -c release
	rm -rf $(APP)
	mkdir -p $(APP)/Contents/MacOS
	cp "$$(swift build -c release --show-bin-path)/Host" $(APP)/Contents/MacOS/Host
	cp Info.plist $(APP)/Contents/Info.plist
	codesign --force --options runtime --entitlements $(ENTITLEMENTS) --sign "$(SIGN_ID)" $(APP)

$(FRAMEWORK):
	@echo "missing $(FRAMEWORK): run make host from the repository root"; exit 1

run: app
	-pkill -x Host
	open $(APP)

clean:
	rm -rf .build Frameworks
