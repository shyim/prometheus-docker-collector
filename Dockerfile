# Build stage with automatic platform detection
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS builder

# Install ca-certificates for HTTPS support
RUN apk add --no-cache ca-certificates

# Set working directory
WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Set ARGs for cross-compilation
ARG TARGETOS TARGETARCH

# Build the application using the target OS and architecture
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-w -s" -o prometheus-docker-collector .

# Final stage using distroless for minimal image size
FROM --platform=$TARGETPLATFORM gcr.io/distroless/static:latest

# Copy ca-certificates from builder
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy the binary from builder
COPY --from=builder /app/prometheus-docker-collector /prometheus-docker-collector

# Note: Running as root (default) to access Docker socket
# In production, consider using Docker socket proxy or adjusting socket permissions

# Expose port
EXPOSE 8080

# Run the application
ENTRYPOINT ["/prometheus-docker-collector"]