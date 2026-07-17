# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/go/pkg/mod \
    go mod download

COPY . .
RUN --mount=type=cache,target=/root/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/hubcdn ./cmd/hubcdn

# An empty directory to seed /data's ownership in the final stage (see
# below) - distroless has no shell, so mkdir has to happen here.
RUN mkdir -p /out/data

# Distroless: no shell, no package manager, non-root by default - the
# smallest attack surface available for a Go static binary. Ships CA
# certificates, which is all a hubCDN node needs (HTTPS origins, ACME).
FROM gcr.io/distroless/static-debian12:nonroot AS final

COPY --from=build /out/hubcdn /usr/local/bin/hubcdn

# Docker seeds a brand-new named volume from whatever already exists on the
# image at its mount point, ownership included. Without this, a fresh /data
# volume is created root-owned, and the nonroot user below can never write
# a certificate to it - silently, since certmagic's storage errors don't
# get logged on this path. 65532:65532 is distroless's nonroot UID/GID.
COPY --from=build --chown=65532:65532 /out/data /data

# Unprivileged (>1024), so the container never needs root or
# CAP_NET_BIND_SERVICE. Map this to 443 from outside (host port mapping, a
# reverse proxy, or NAT) as needed for your deployment. hubCDN is TLS-only -
# there is no HTTP port to expose.
ENV HUBCDN_DATA_DIR=/data \
    HUBCDN_HTTPS_ADDR=:4403

VOLUME ["/data"]
EXPOSE 4403

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/usr/local/bin/hubcdn", "healthcheck"]

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/hubcdn"]
