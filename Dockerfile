# Build Stage
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Install git and ca-certificates
RUN apk add --no-cache git ca-certificates

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build static binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/emdash-worker cmd/emdash-worker/main.go

# Production Stage
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /bin/emdash-worker /usr/local/bin/emdash-worker

# Default environment configuration
ENV EMDASH_BROKER_ADDR="ws://localhost:8080"

ENTRYPOINT ["/usr/local/bin/emdash-worker"]
