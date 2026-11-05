SIGN_ID ?= 96776319F63EFEBAFC132A30304DA2ABEA6298EA
TIMESTAMP := $(if $(filter -,$(SIGN_ID)),,--timestamp)
FRAMEWORK := Frameworks/PeepwhoCore.xcframework
BIN = $$(swift build -c release --show-bin-path)

.PHONY: build test host app run-host run clean

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

app: build
	rm -rf .build/Peepwho.app
	mkdir -p .build/Peepwho.app/Contents/MacOS
	cp "$(BIN)/Peepwho" .build/Peepwho.app/Contents/MacOS/Peepwho
	cp Sources/Peepwho/Info.plist .build/Peepwho.app/Contents/Info.plist
	codesign --force --options runtime $(TIMESTAMP) --entitlements Sources/Peepwho/Peepwho.entitlements --sign "$(SIGN_ID)" .build/Peepwho.app

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
