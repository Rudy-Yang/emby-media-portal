# Build stage
FROM golang:1.21-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache gcc musl-dev

# Copy go mod files
COPY go.mod go.sum* ./
RUN go mod download || true

# Copy source code
COPY . .

# Download dependencies
RUN go mod tidy

# Build binary
RUN CGO_ENABLED=1 GOOS=linux go build -o emby-media-portal ./cmd/server

# Runtime stage
FROM alpine:latest

WORKDIR /app

# Install runtime dependencies
RUN apk add --no-cache ca-certificates sqlite

# Copy binary
COPY --from=builder /app/emby-media-portal .

# Copy example config as the container default
COPY config.example.yaml ./config.yaml

# Create data directory
RUN mkdir -p /app/data

# Expose port
EXPOSE 8095

# Run
CMD ["./emby-media-portal"]
