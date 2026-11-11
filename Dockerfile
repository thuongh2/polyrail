# syntax=docker/dockerfile:1
# Small single-binary image. Build: docker build -t agent-gateway .
FROM golang:1.24-alpine AS build
WORKDIR /src
# go.mod replaces golang.org/x/* etc. with their GitHub mirrors (docs/DECISIONS.md D2);
# "direct" serves those, the proxy serves the rest. go.sum pins every hash.
ENV GOPROXY=https://proxy.golang.org,direct GOFLAGS=-mod=mod CGO_ENABLED=0 GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN apk add --no-cache git && go mod download
COPY . .
ARG VERSION=docker
RUN go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/gateway ./cmd/gateway

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/gateway /gateway
ENV DATA_DIR=/data LISTEN_ADDR=:8080
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s CMD ["/gateway", "healthcheck"]
ENTRYPOINT ["/gateway"]
