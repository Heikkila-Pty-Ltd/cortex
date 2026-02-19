# Build stage
FROM golang:1.24-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git make

# Set working directory
WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-w -s" -o cortex ./cmd/cortex/

# Runtime stage
FROM alpine:latest

# Install runtime dependencies
RUN apk add --no-cache ca-certificates sqlite

# Create non-root user
RUN adduser -D -s /bin/sh cortex

# Set working directory
WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/cortex /usr/local/bin/cortex

# Change to non-root user
USER cortex

# Expose API port
EXPOSE 8900

# Health check
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8900/health || exit 1

# Default command
ENTRYPOINT ["cortex"]
CMD ["--config", "/etc/cortex/cortex.toml"]
