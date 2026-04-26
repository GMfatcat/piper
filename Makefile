# Piper Makefile.
# - `make build` cross-compiles for Linux amd64 + arm64 (production targets).
# - `make build-host` builds for the current OS/arch (Windows .exe in dev).
# - All builds use CGO_ENABLED=0 because modernc.org/sqlite is pure Go;
#   never enable CGO or the cross-compile breaks.

.PHONY: build build-amd64 build-arm64 build-host test test-race lint fmt vet clean

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-s -w -X github.com/GMfatcat/piper/internal/version.Version=$(VERSION)"

build: build-amd64 build-arm64

build-amd64:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build $(LDFLAGS) -o dist/piper ./cmd/piper

build-arm64:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build $(LDFLAGS) -o dist/piper-arm64 ./cmd/piper

build-host:
	CGO_ENABLED=0 go build $(LDFLAGS) -o dist/piper$(shell go env GOEXE) ./cmd/piper

test:
	go test -count=1 ./...

test-race:
	go test -count=1 -race ./...

lint: vet
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run || echo "(golangci-lint not installed, skipping)"

fmt:
	go fmt ./...

vet:
	go vet ./...

clean:
	rm -rf dist/
