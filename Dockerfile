# Stage 1: Build
FROM golang:alpine AS builder

WORKDIR /build

# Copy dependency files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build static binary (stripped for smaller size)
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -trimpath \
    -o repull ./cmd/repull

# Stage 2: Runtime
FROM alpine:latest

# Install CA certificates for HTTPS
RUN apk --no-cache add ca-certificates

# Create non-root user
RUN addgroup -g 1000 repull && \
    adduser -D -u 1000 -G repull repull

# Copy binary from builder
COPY --from=builder /build/repull /usr/local/bin/repull

# Set ownership and make executable
RUN chmod +x /usr/local/bin/repull

# Switch to non-root user
USER repull

ENTRYPOINT ["/usr/local/bin/repull"]
