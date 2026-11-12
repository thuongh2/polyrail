# syntax=docker/dockerfile:1
# Small single-binary image. Build: docker build -t polyrail .
# The build stage runs on the builder's own platform and cross-compiles (pure Go, CGO off), so
# multi-arch images need no QEMU emulation.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src
# go.mod replaces golang.org/x/* etc. with their GitHub mirrors (docs/DECISIONS.md D2);
# "direct" serves those, the proxy serves the rest. go.sum pins every hash.
ENV GOPROXY=https://proxy.golang.org,direct GOFLAGS=-mod=mod CGO_ENABLED=0 GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN apk add --no-cache git && go mod download
COPY . .
ARG VERSION=docker
# TAGS=alertsfull adds the smtp and generic alert services (docs/CONFIG.md "Build tags").
ARG TAGS=""
ARG TARGETOS TARGETARCH
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -tags "$TAGS" -ldflags="-s -w -X main.version=${VERSION}" -o /out/polyrail ./cmd/polyrail

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.source="https://github.com/thuongh2/polyrail" \
      org.opencontainers.image.description="Polyrail: one API for every model. Self-hosted, light, open." \
      org.opencontainers.image.licenses="Apache-2.0"
COPY --from=build /out/polyrail /polyrail
ENV DATA_DIR=/data LISTEN_ADDR=:8080
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s CMD ["/polyrail", "healthcheck"]
ENTRYPOINT ["/polyrail"]
