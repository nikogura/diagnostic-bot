# Build stage - Debian bookworm, matching the distroless-debian12 runtime base
# (build on the same platform we run on). Go cross-compiles natively via
# GOOS/GOARCH — no QEMU needed.
FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS builder

# TARGETOS / TARGETARCH are supplied automatically by `docker buildx build`
# when invoked with --platform. We deliberately declare them WITHOUT defaults
# so a bare `docker build` (no buildx, no --platform) fails loudly instead of
# silently producing a wrong-arch binary that still ships inside a per-arch
# manifest tag. See issue #15 (v0.0.42 arm64 manifest carried an amd64 ELF).
ARG TARGETOS
ARG TARGETARCH

# VERSION is stamped into the binary's telemetry via ldflags. CI passes the
# computed semver; a bare `docker build` defaults to "dev".
ARG VERSION=dev

# Set working directory
WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the binary, stamping the version into the telemetry service.version.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -ldflags="-X main.serviceVersion=${VERSION}" -o /bin/diagnostic-bot ./cmd/bot

# Final stage — distroless static. The bot is a pure-Go static binary with no
# external-binary dependencies (whois and PDF rendering are both in-process), so
# the runtime image carries only the binary plus CA certs and tzdata. No shell,
# no package manager, runs as the distroless nonroot user (UID 65532).
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

# Copy the static binary.
COPY --from=builder /bin/diagnostic-bot /app/diagnostic-bot

# Investigation skills are mounted at runtime.
ENV INVESTIGATION_DIR=/app/investigations

# Socket Mode is outbound-only; no inbound ports. Health is the /healthz and
# /readyz endpoints on the metrics port — a shell-based HEALTHCHECK is neither
# possible nor needed on distroless.
ENTRYPOINT ["/app/diagnostic-bot"]
