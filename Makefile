BINARY  := whotoask
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/dcadolph/whotoask/cmd.version=$(VERSION)

.PHONY: build install test test-short e2e vet fmt lint core-deps mac host release clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

install:
	go install -ldflags "$(LDFLAGS)" .

test:
	go test ./...

test-short:
	go test -short ./...

e2e:
	go test -count=1 -v -run TestFullPipeline ./internal/simorg

vet:
	go vet ./...

fmt:
	gofmt -w .

lint:
	golangci-lint run

# core-deps fails when the app core links the command line, a keychain, or the
# simulated company. The host supplies every secret, a sandboxed app cannot shell
# to a keychain, and the app bundles the sample company as data.
CORE_BANNED := spf13/cobra|internal/secret|zalando/go-keyring|internal/keyring|internal/simorg|net/http/httptest

core-deps:
	@bad=$$(go list -deps ./core | grep -E '$(CORE_BANNED)'); \
	if [ -n "$$bad" ]; then echo "core links banned packages:"; echo "$$bad"; exit 1; fi; \
	echo "core-deps: ok"

# mac binds the core into the Swift package and builds the test host and the app, signed. host
# is the same target under its first name. See docs/APPS.md.
# -macosversion sets the floor on every cgo object. gomobile overrides CGO_CFLAGS, so the
# flag is the only way in, and the build cache ignores it: after changing it, build once
# with GOFLAGS=-a or the cached runtime objects keep the old floor. -trimpath keeps build
# machine paths out of the app binary. Go still records the folder gomobile's temporary module
# replaces this one with, so the core is bound from a copy of the working tree at BIND_DIR, a
# path that names no person. The copy takes untracked files too, so local edits reach the build.
BIND_DIR := /private/tmp/whotoask-bind/whotoask
MAC_FRAMEWORK := mac/Frameworks/WhoToAskCore.xcframework
MACOS_MIN := 14.0

mac:
	rm -rf $(BIND_DIR)
	mkdir -p $(BIND_DIR)
	git ls-files -co --exclude-standard | while IFS= read -r f; do [ -e "$$f" ] && echo "$$f"; done \
		| tar -cf - -T - | tar -xf - -C $(BIND_DIR)
	cd $(BIND_DIR) && gomobile bind -trimpath -target macos -macosversion $(MACOS_MIN) \
		-o $(CURDIR)/$(MAC_FRAMEWORK) ./core
	$(MAKE) -C mac host app

host: mac

release:
	goreleaser release --clean

clean:
	rm -f $(BINARY)
