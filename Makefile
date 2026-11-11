# Build env: only GitHub is reachable in the dev sandbox (see docs/DECISIONS.md D2).
export GOPROXY ?= direct
export GOSUMDB ?= off
export GOFLAGS ?= -mod=mod
export GOTOOLCHAIN ?= local
export CGO_ENABLED ?= 0

.PHONY: build test check run ui
# ui rebuilds the embedded web UI (needs Node 20+); the built files are committed, so Go builds without Node.
ui:
	cd web && npm ci && npm run build

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o bin/gateway ./cmd/gateway
test:
	go test ./...
check:
	go vet ./...
	CGO_ENABLED=1 go test -race -count=1 ./...
run: build
	./bin/gateway
