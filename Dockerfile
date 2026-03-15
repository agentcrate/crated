# ──────────────────────────────────────────────────────────────────────
# agentcrate/base — the base image for all agent containers
#
# This image contains the crated binary pre-installed. Agent images
# built by `crate build` layer their Agentfile and stdio tools on top
# of this base, eliminating the need for Go compilation during agent
# builds.
#
# Build:
#   docker build -t agentcrate/base:dev .
#
# Usage in agent Dockerfiles:
#   FROM agentcrate/base:latest
#   COPY Agentfile /agent/Agentfile
#   COPY tools/ /agent/tools/
# ──────────────────────────────────────────────────────────────────────

# ── Stage 1: Build ────────────────────────────────────────────────────
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS builder

ARG VERSION=dev
# TARGETOS and TARGETARCH are automatically set by Buildx when using
# `docker buildx build --platform linux/amd64,linux/arm64`.
ARG TARGETOS
ARG TARGETARCH

RUN apk add --no-cache git ca-certificates

WORKDIR /src

# Copy go module files first for layer caching.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/crated ./cmd/crated/

# ── Stage 2: Runtime ──────────────────────────────────────────────────
FROM alpine:3.21

# Core runtime deps + Node.js (npx) + Python (uvx) for stdio skills.
# Pin versions for reproducible builds.
RUN apk add --no-cache ca-certificates tini nodejs npm python3 py3-pip \
    && npm install -g npm@10 \
    && pip3 install --break-system-packages uv==0.6.0 \
    && ln -sf /usr/bin/python3 /usr/bin/python

# Labels for OCI image metadata.
LABEL org.opencontainers.image.title="agentcrate-base" \
    org.opencontainers.image.description="Base image for AgentCrate agent containers" \
    org.opencontainers.image.vendor="AgentCrate" \
    org.opencontainers.image.source="https://github.com/agentcrate/crated" \
    org.agentcrate.image-type="base"

# Create non-root user for agent processes.
RUN addgroup -S agent && adduser -S agent -G agent

WORKDIR /agent

# Install the runtime binary.
COPY --from=builder /out/crated /agent/crated

# Ensure tools directory exists for agent builds to COPY into.
RUN mkdir -p /agent/tools && chown -R agent:agent /agent

USER agent

# Health check for container orchestrators (Kubernetes, Docker Compose).
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://localhost:8080/healthz || exit 1

# Expose health check port and playground port.
EXPOSE 8080 3000

# Use tini as PID 1 for proper signal handling.
ENTRYPOINT ["/sbin/tini", "--", "/agent/crated"]
CMD ["--agentfile", "/agent/Agentfile"]
