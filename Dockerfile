# syntax=docker/dockerfile:1

# Build stage. The module targets Go 1.27 (go.mod), so the toolchain image
# matches. The binary is static (CGO_ENABLED=0) so the runtime image needs no
# libc for it.
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/tcp-wake ./cmd/tcp-wake

# Runtime stage. debian:stable-slim gives a shell for the deployment check; the
# proxy runs as a non-root user (NFR-8).
FROM debian:stable-slim

# etherwake is the fixed wake tool and needs a raw socket. It is installed and
# given the narrowest privilege that lets it work, the CAP_NET_RAW file
# capability, instead of full root (ADR-0016). libcap2-bin provides setcap and
# getcap; getcap is used by scripts/check-deploy.sh inside the container. The
# proxy itself stays unprivileged.
RUN apt-get update \
    && apt-get install -y --no-install-recommends etherwake libcap2-bin \
    && rm -rf /var/lib/apt/lists/* \
    && setcap cap_net_raw+ep /usr/sbin/etherwake \
    && useradd --uid 10001 --no-create-home --shell /usr/sbin/nologin tcpwake

COPY --from=build /out/tcp-wake /usr/local/bin/tcp-wake

USER 10001:10001

# Default config discovery is --config, then $TCPWAKE_CONFIG, then
# /etc/tcp-wake/config.toml (ADR-0015), which is the file compose mounts.
ENTRYPOINT ["/usr/local/bin/tcp-wake"]
