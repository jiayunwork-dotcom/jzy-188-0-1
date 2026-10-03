# syntax=docker/dockerfile:1
# Two-stage build: compile in the Go toolchain image, ship a minimal
# Alpine image containing only the static binary.
FROM golang:1.23-alpine AS build

WORKDIR /src

# Cache dependencies independently of source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Pure-Go SQLite (modernc.org/sqlite) -> fully static binary, CGO disabled.
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/grrd ./cmd/grrd

FROM alpine:3.20 AS runtime

# Non-root runtime user.
RUN addgroup -S grr && adduser -S -G grr grr && \
    mkdir -p /data && chown grr:grr /data
USER grr

COPY --from=build /out/grrd /usr/local/bin/grrd

ENV GRR_DATA_DIR=/data \
    GRR_ADDR=:8080

EXPOSE 8080
VOLUME ["/data"]

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
    CMD wget -qO- http://127.0.0.1:8080/api/v1/health || exit 1

ENTRYPOINT ["/usr/local/bin/grrd"]
