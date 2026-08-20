# syntax=docker/dockerfile:1

# ---- build ----
FROM golang:1.27rc2 AS build
WORKDIR /src

# Cache modules first.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
# VERSION is injected by CI (docker metadata-action) and stamped into the binary.
ARG VERSION=dev
# CGO disabled: modernc.org/sqlite is pure Go, so the binary is fully static
# and runs on distroless/static (and scratch).
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/go-bookshelf ./cmd/go-bookshelf

# The database, cover cache and thumbnails live in /data, pre-created and owned
# by the distroless nonroot user (65532:65532) since the final image has no
# shell to mkdir or chown at runtime.
RUN mkdir -p /out/data && chown 65532:65532 /out/data

# ---- runtime ----
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/go-bookshelf /usr/local/bin/go-bookshelf
# The only outbound HTTPS go-bookshelf makes is OIDC discovery and token
# exchange, and that is enough to need a CA bundle. Pin it explicitly rather
# than depend on a future base tag still shipping one.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chown=65532:65532 /out/data /data

ENV GOBOOKSHELF_DB_PATH=/data/go-bookshelf.db \
    GOBOOKSHELF_DATA_DIR=/data

VOLUME ["/data"]
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/go-bookshelf"]
