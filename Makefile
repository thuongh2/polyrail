# Build env: only GitHub is reachable in the dev sandbox (see docs/DECISIONS.md D2).
export GOPROXY ?= direct
export GOSUMDB ?= off
export GOFLAGS ?= -mod=mod
export GOTOOLCHAIN ?= local
export CGO_ENABLED ?= 0

.PHONY: build dist test check run ui
# ui rebuilds the embedded web UI (needs Node 20+); the built files are committed, so Go builds without Node.
ui:
	cd web && npm ci && npm run build

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# TAGS=alertsfull adds the smtp and generic alert services (docs/CONFIG.md "Build tags").
TAGS ?=
build:
	go build -trimpath -tags "$(TAGS)" -ldflags="-s -w -X main.version=$(VERSION)" -o bin/polyrail ./cmd/polyrail
# dist cross-builds bin/polyrail-<os>-<arch> and bin/mockprovider-<os>-<arch> (used by scripts/run-local.sh).
dist:
	for p in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do \
	  os=$${p%/*}; arch=$${p#*/}; \
	  GOOS=$$os GOARCH=$$arch go build -trimpath -tags "$(TAGS)" -ldflags="-s -w -X main.version=$(VERSION)" -o bin/polyrail-$$os-$$arch ./cmd/polyrail || exit 1; \
	  GOOS=$$os GOARCH=$$arch go build -trimpath -o bin/mockprovider-$$os-$$arch ./tools/mockprovider || exit 1; \
	done
test:
	go test ./...
check:
	go vet ./...
	CGO_ENABLED=1 go test -race -count=1 ./...
run: build
	./bin/polyrail
