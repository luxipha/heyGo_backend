# Build stage
FROM golang:1.26.8-bookworm AS builder
# Set working directory
WORKDIR /app
# Copy go mod and sum files
COPY go.mod go.sum ./
# Download dependencies
RUN go mod download
# Copy the source code
COPY services/trip-service/ ./services/trip-service/
COPY shared/ ./shared/
# Build the trip-service binary
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o main ./services/trip-service

# Final stage
FROM debian:bookworm-slim
# Install dependencies
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
# Set working directory
WORKDIR /app
# Copy the binary from builder stage
COPY --from=builder --chown=65532:65532 /app/main /app/main
# Expose port 9000
EXPOSE 9000
# Run without root privileges. Cloud Run injects PORT at runtime.
USER 65532:65532
ENTRYPOINT ["/app/main"]
