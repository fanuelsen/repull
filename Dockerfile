# Stage 1: Build
FROM golang:1.26.0-alpine AS builder

WORKDIR /build

# Copy dependency files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build static binary (stripped for smaller size)
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -trimpath \
    -o repull ./cmd/repull

# Stage 2: Runtime
FROM alpine:3.23

# Install CA certificates for HTTPS
RUN apk --no-cache add ca-certificates

# Create non-root user
RUN addgroup -g 1000 repull && \
    adduser -D -u 1000 -G repull repull

# Copy binary from builder
COPY --from=builder /build/repull /usr/local/bin/repull

# Set ownership and make executable
RUN chmod +x /usr/local/bin/repull

# Label so the running container can identify itself
LABEL io.repull.app="true"

# Switch to non-root user
USER repull

# Verify the process is alive (pid 1 exists in the container)
HEALTHCHECK --interval=60s --timeout=3s --start-period=5s --retries=3 \
    CMD kill -0 1 || exit 1

ENTRYPOINT ["/usr/local/bin/repull"]
