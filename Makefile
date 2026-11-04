BINARY  := peepwho
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/dcadolph/peepwho/cmd.version=$(VERSION)

.PHONY: build install test test-short e2e vet fmt lint release clean

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

release:
	goreleaser release --clean

clean:
	rm -f $(BINARY)
