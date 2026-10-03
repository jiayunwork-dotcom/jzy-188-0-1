# syntax=docker/dockerfile:1

# ---- build stage ----
FROM golang:1.23-alpine AS build

WORKDIR /src

# Cache dependencies first.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Build a static binary (CGO_ENABLED=0 so it runs on a scratch-style base).
# GOTOOLCHAIN=local pins the build to golang:1.23 (no toolchain auto-download).
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOTOOLCHAIN=local GOOS=linux go build -trimpath -ldflags="-s -w" \
        -o /out/msagrr ./cmd/server

# ---- runtime stage ----
FROM alpine:3.20

# CA certificates and a non-root user; tzdata for correct recorded_at
# rendering if clients send zone-less timestamps.
RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 10001 appuser

COPY --from=build /out/msagrr /usr/local/bin/msagrr

# The mounted data volume: bbolt file lives here, survives restarts.
RUN mkdir -p /data && chown appuser:appuser /data
VOLUME ["/data"]

USER appuser
ENV MSA_DATA_DIR=/data \
    MSA_HTTP_ADDR=:8080 \
    GIN_MODE=release

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/health || exit 1

ENTRYPOINT ["/usr/local/bin/msagrr"]
