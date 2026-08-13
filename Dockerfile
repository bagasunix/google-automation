# Go orchestrator build stage
FROM golang:1.25-alpine AS builder

WORKDIR /build

# Copy go mod files first for layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY . .

# Build binary
RUN CGO_ENABLED=0 go build -o /google-automation ./cmd/main.go

# Final stage: minimal image
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /google-automation /app/google-automation
COPY --from=builder /build/config/config.yaml /app/config/config.yaml
COPY --from=builder /build/internal/storage/schema.sql /app/internal/storage/schema.sql

RUN mkdir -p /app/data /app/screenshots /app/results

ENV DB_PATH=/app/data/search_automation.db

EXPOSE 50051

ENTRYPOINT ["/app/google-automation"]
CMD ["--config", "config/config.yaml", "--db", "/app/data/search_automation.db"]
