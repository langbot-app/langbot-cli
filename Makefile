GO ?= go
VERSION ?= dev
COMMIT ?= $(shell git describe --always --dirty)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS = -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)

.PHONY: build lint test check cross-build
build:
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/lbctl ./cmd/lbctl

test:
	$(GO) test -race ./...

lint:
	@unformatted=$$(gofmt -l cmd internal) && \
		{ test -z "$$unformatted" || { printf 'Run gofmt -w on:\n%s\n' "$$unformatted"; exit 1; }; }
	$(GO) vet ./...

check: lint test

cross-build:
	GO='$(GO)' VERSION='$(VERSION)' COMMIT='$(COMMIT)' BUILD_DATE='$(BUILD_DATE)' sh scripts/build.sh
