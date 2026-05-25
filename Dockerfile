# 1. Build Stage
FROM golang:1.26-alpine AS builder

# Set work directory
WORKDIR /app

# Install git/build-base if any native compilation is needed (unlikely for pure Go, but good practice)
RUN apk add --no-cache git build-base

# Copy dependency manifest
COPY go.mod go.sum ./

# Download exact module versions recorded in go.sum (reproducible, does not modify go.mod/go.sum)
RUN go mod download

# Copy source code
COPY main.go ./
COPY pkg/ ./pkg/

# Compile statically linked binary
RUN CGO_ENABLED=0 GOOS=linux go build -mod=readonly -ldflags="-w -s" -o envsync main.go

# 2. Production Stage
FROM alpine:3.22

# Set runtime workspace
WORKDIR /app

# Install CA certificates for secure external database connections
RUN apk add --no-cache ca-certificates \
    && addgroup -S appgroup \
    && adduser -S -u 1000 appuser -G appgroup

# Copy the compiled binary
COPY --from=builder /app/envsync .

# Run as non-root
USER appuser

# Command to execute the manager
ENTRYPOINT ["./envsync"]
