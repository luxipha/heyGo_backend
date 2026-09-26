# Builder Stage
FROM golang:1.26.8-bookworm AS builder
# Install dependencies
RUN apt-get update && apt-get install -y --no-install-recommends pkg-config build-essential librdkafka-dev \
    && rm -rf /var/lib/apt/lists/*
# Set working directory
WORKDIR /app
# Copy go mod and sum files
COPY go.mod go.sum ./
# Download dependencies
RUN go mod download
# Copy the source code
COPY services/payment-service/ ./services/payment-service/
COPY shared/ ./shared/
# Build the binary
RUN CGO_ENABLED=1 GOOS=linux CC=gcc go build -a -installsuffix cgo -o main ./services/payment-service

# Final Stage
FROM debian:bookworm-slim
# Install dependencies
RUN apt-get update && apt-get install -y --no-install-recommends librdkafka1 ca-certificates \
    && rm -rf /var/lib/apt/lists/*
# Set working directory
WORKDIR /app
# Copy the binary from builder stage
COPY --from=builder --chown=65532:65532 /app/main /app/main
# Expose the legacy local port. Cloud Run routes to its injected PORT instead.
EXPOSE 9200
# Run without root privileges. Cloud Run injects PORT at runtime.
USER 65532:65532
ENTRYPOINT ["/app/main"]
