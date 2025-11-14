# Build stage
FROM golang:1.21-alpine AS builder

WORKDIR /build

# Copy go mod files first for better layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the application (static binary, no CGO dependencies)
# Explicitly build for linux/amd64 architecture
# -w: omit debug symbols, -s: omit symbol table (reduces binary size)
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -installsuffix cgo \
    -ldflags="-w -s" \
    -o configmap-event-handler .

# Runtime stage
FROM alpine:latest

# Labels for metadata
LABEL maintainer="eventhandler"
LABEL description="Event-driven Kubernetes service for patching ConfigMaps"
LABEL org.opencontainers.image.title="configmap-event-handler"
LABEL org.opencontainers.image.description="Event-driven service that receives HTTP POST events and patches ConfigMaps using native Kubernetes client"

# Install ca-certificates and wget for healthcheck
# Note: wget is only needed for Docker HEALTHCHECK, not for the application
RUN apk --no-cache add ca-certificates tzdata wget && \
    update-ca-certificates

WORKDIR /app

# Copy binary from builder (only the binary, no source code)
COPY --from=builder /build/configmap-event-handler .

# Create non-root user and set permissions
# Using numeric UID/GID for security (no username dependency)
RUN addgroup -g 1000 appuser && \
    adduser -D -u 1000 -G appuser appuser && \
    chown -R 1000:1000 /app && \
    chmod 755 /app/configmap-event-handler

# Switch to non-root user (using numeric UID for security)
# The application uses native Go Kubernetes client (k8s.io/client-go)
# No kubectl binary needed - connects directly to Kubernetes API
USER 1000

EXPOSE 8080

# Health check endpoint
# The application provides /health endpoint for liveness/readiness probes
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health || exit 1

# Run the application
# Uses ServiceAccount token for Kubernetes API authentication when running in-cluster
CMD ["./configmap-event-handler"]

