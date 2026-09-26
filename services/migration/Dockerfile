FROM golang:1.26.8-bookworm AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/migrate/ ./cmd/migrate/
COPY shared/ ./shared/
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o migrate ./cmd/migrate

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=builder --chown=65532:65532 /app/migrate /app/migrate
USER 65532:65532
ENTRYPOINT ["/app/migrate"]
