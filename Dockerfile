# Build stage - pin to host arch for native compilation.
# Go cross-compiles natively via GOOS/GOARCH — no QEMU needed.
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS builder

ARG TARGETOS=linux
ARG TARGETARCH=amd64

# Install build dependencies
RUN apk add --no-cache git make

# Set working directory
WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the binaries
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /bin/diagnostic-bot ./cmd/bot
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /bin/mcp-server ./cmd/mcp-server

# Final stage
FROM alpine:3.19

# Install runtime dependencies including LaTeX, Pandoc, and Claude Code
RUN apk add --no-cache \
    ca-certificates \
    tzdata \
    ttf-dejavu \
    ttf-liberation \
    fontconfig \
    texlive \
    texlive-luatex \
    texlive-xetex \
    texmf-dist-latexextra \
    texmf-dist-fontsextra \
    pandoc \
    make \
    nodejs \
    npm \
    wget \
    aws-cli

# Install Claude Code globally (installs as 'claude' in /usr/local/bin/)
RUN npm install -g @anthropic-ai/claude-code

# Create non-root user with home directory
RUN addgroup -g 1000 bot && \
    adduser -D -u 1000 -G bot bot && \
    mkdir -p /home/bot/.claude && \
    chown -R bot:bot /home/bot

# Set working directory
WORKDIR /app

# Copy binaries from builder
COPY --from=builder /bin/diagnostic-bot /app/diagnostic-bot
COPY --from=builder /bin/mcp-server /app/mcp-server

# Investigation templates are mounted from Vault secrets at runtime

# Copy engineering standards
COPY --chown=bot:bot CLAUDE.md /app/docs/CLAUDE.md

# Copy MCP configuration
COPY --chown=bot:bot .mcp.json /app/.mcp.json

# Copy LaTeX templates (not in reports/ - that's for generated PDFs)
COPY --chown=bot:bot latex-templates/ /app/latex-templates/

# Copy entrypoint script
COPY --chown=root:root entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh

# Create tmp directory for Claude Code prompt files
RUN mkdir -p /tmp && chown bot:bot /tmp

# Switch to non-root user
USER bot

# Expose no ports (Socket Mode doesn't need inbound connections)

# Set default environment variables
ENV INVESTIGATION_DIR=/app/investigations \
    CLAUDE_MD_PATH=/app/docs/CLAUDE.md \
    PATH=/usr/local/bin:/usr/bin:/bin

# Health check (check if process is running)
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD pgrep -f diagnostic-bot || exit 1

# Run the bot via entrypoint script
ENTRYPOINT ["/app/entrypoint.sh"]
