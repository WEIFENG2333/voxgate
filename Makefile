BINARY ?= voxgate
GO ?= go

.PHONY: build test test-e2e probe doctor clean release-snapshot

build:
	CGO_ENABLED=1 $(GO) build -o bin/$(BINARY) ./cmd/voxgate

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

release-snapshot:
	goreleaser release --snapshot --clean
