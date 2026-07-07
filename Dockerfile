# Stage 1: Build
# Base images pinned by digest: a compromised or re-pushed base tag would
# end up inside (builder) or underneath (runtime) the shipped repull image.
# Renovate keeps tag and digest current (pinDigests in renovate.json).
FROM golang:1.26.4-alpine@sha256:3ad57304ad93bbec8548a0437ad9e06a455660655d9af011d58b993f6f615648 AS builder

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
FROM alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b

# Install CA certificates for HTTPS
RUN apk --no-cache add ca-certificates

# Create non-root user
RUN addgroup -g 1000 repull && \
    adduser -D -u 1000 -G repull repull

# Copy binary from builder (permissions are preserved from the build stage)
COPY --from=builder /build/repull /usr/local/bin/repull

# Label so the running container can identify itself
LABEL io.repull.app="true"

# Switch to non-root user
USER repull

# No HEALTHCHECK on purpose: repull is a sleep-mostly loop, not a server.
# If the process dies the container exits and the restart policy handles it;
# a check like "kill -0 1" can never fail and only reports a misleading
# "healthy".

ENTRYPOINT ["/usr/local/bin/repull"]
