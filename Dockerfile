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

# Runtime stage. debian:stable-slim gives a shell for the deployment check and
# the setuid behaviour T1 verified; the proxy runs as a non-root user (NFR-8).
FROM debian:stable-slim

# The proxy process is unprivileged. The wake command is bind-mounted from the
# host and keeps its own file mode and owner (ADR-0005, NFR-7); it is the only
# privileged part of the system.
RUN useradd --system --uid 10001 --no-create-home --shell /usr/sbin/nologin tcpwake

COPY --from=build /out/tcp-wake /usr/local/bin/tcp-wake

USER 10001:10001

# Default config discovery is --config, then $TCPWAKE_CONFIG, then
# /etc/tcp-wake/config.toml (ADR-0015), which is the file compose mounts.
ENTRYPOINT ["/usr/local/bin/tcp-wake"]
