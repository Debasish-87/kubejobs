# ----------- BUILD STAGE -----------
FROM golang:1.23-alpine AS builder

WORKDIR /app

# git required for go mod download
RUN apk add --no-cache git

# Cache dependencies separately from source
COPY go.mod go.sum ./
RUN go mod download

# Copy full source
COPY . .

# Build binary from correct entry point
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o app ./cmd/server


# ----------- RUNTIME STAGE -----------
FROM alpine:3.20

WORKDIR /app

# ca-certificates needed for TLS (ElastiCache rediss://)
RUN apk update && apk add --no-cache ca-certificates

# Run as non-root user
RUN adduser -D -g '' appuser
USER appuser

# Copy only the binary
COPY --from=builder /app/app .

EXPOSE 8080

# Health check
HEALTHCHECK --interval=10s --timeout=3s --retries=3 \
  CMD wget -qO- http://localhost:8080/health || exit 1

CMD ["./app"]