BINARY  := peepwho
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/dcadolph/peepwho/cmd.version=$(VERSION)

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
# with GOFLAGS=-a or the cached runtime objects keep the old floor.
MAC_FRAMEWORK := mac/Frameworks/PeepwhoCore.xcframework
MACOS_MIN := 14.0

mac:
	gomobile bind -target macos -macosversion $(MACOS_MIN) -o $(MAC_FRAMEWORK) ./core
	$(MAKE) -C mac host app

host: mac

release:
	goreleaser release --clean

clean:
	rm -f $(BINARY)
