# Build stage
FROM golang:1.26.8-bookworm AS builder
# Install dependencies
RUN apt-get update && apt-get install -y pkg-config build-essential librdkafka-dev
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
RUN CGO_ENABLED=1 GOOS=linux CC=gcc go build -a -installsuffix cgo -o main ./services/trip-service

# Final stage
FROM debian:bookworm-slim
# Install dependencies
RUN apt-get update && apt-get install -y librdkafka1 ca-certificates
# Set working directory
WORKDIR /root/
# Copy the binary from builder stage
COPY --from=builder /app/main .
# Expose port 9000
EXPOSE 9000
# Run the binary
CMD ["./main"]
