# 1. Build Stage
FROM golang:1.26-alpine AS builder

# Set work directory
WORKDIR /app

# Install git/build-base if any native compilation is needed (unlikely for pure Go, but good practice)
RUN apk add --no-cache git build-base

# Copy dependency manifest
COPY go.mod go.sum ./
# RUN go mod download (if any external packages exist, but we will run tidy in scripts)

# Copy source code
COPY main.go ./
COPY pkg/ ./pkg/

# Fetch and cache dependencies
RUN go mod tidy

# Compile statically linked binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o envsync main.go

# 2. Production Stage
FROM alpine:3.22

# Set runtime workspace
WORKDIR /app

# Install CA certificates for secure external database connections
RUN apk add --no-cache ca-certificates \
    && addgroup -S appgroup \
    && adduser -S appuser -G appgroup

# Copy the compiled binary
COPY --from=builder /app/envsync .

# Run as non-root
USER appuser

# Command to execute the manager
ENTRYPOINT ["./envsync"]
