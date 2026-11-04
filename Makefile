BINARY  := peepwho
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/dcadolph/peepwho/cmd.version=$(VERSION)

.PHONY: build install test test-short e2e vet fmt lint core-deps release clean

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

# core-deps fails when the app core links the command line or a keychain. The
# host supplies every secret, and a sandboxed app cannot shell to a keychain.
CORE_BANNED := spf13/cobra|internal/secret|zalando/go-keyring|internal/keyring

core-deps:
	@bad=$$(go list -deps ./core | grep -E '$(CORE_BANNED)'); \
	if [ -n "$$bad" ]; then echo "core links banned packages:"; echo "$$bad"; exit 1; fi; \
	echo "core-deps: ok"

release:
	goreleaser release --clean

clean:
	rm -f $(BINARY)
