BINARY ?= voxgate
GO ?= go
PREFIX ?= $(HOME)/.local

.PHONY: build install test fmt vet test-e2e probe doctor clean release-check release-snapshot

build:
	CGO_ENABLED=1 $(GO) build -o bin/$(BINARY) ./cmd/voxgate

install: build
	install -d $(PREFIX)/bin
	install -m 0755 bin/$(BINARY) $(PREFIX)/bin/$(BINARY)

fmt:
	gofmt -w $$(git ls-files '*.go')

vet:
	CGO_ENABLED=1 $(GO) vet ./...

test:
	CGO_ENABLED=1 $(GO) test ./...

test-e2e: build
	tests/e2e/run.sh

probe: build
	tests/e2e/protocol_probe.sh

doctor: build
	bin/$(BINARY) doctor

clean:
	rm -rf bin dist

release-check:
	@command -v goreleaser >/dev/null 2>&1 || { echo "goreleaser is required for release-check"; exit 1; }
	goreleaser check

release-snapshot:
	@command -v goreleaser >/dev/null 2>&1 || { echo "goreleaser is required for release-snapshot"; exit 1; }
	goreleaser release --snapshot --clean --single-target
