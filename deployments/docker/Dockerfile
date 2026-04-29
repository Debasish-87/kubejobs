# ---------- BUILD STAGE ----------
FROM golang:1.23-alpine AS builder

WORKDIR /app

# Required for go modules
RUN apk add --no-cache git

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy full source
COPY . .

# Debug (remove later if you want)
RUN ls -R /app

# Build from ROOT (NOT cmd/server)
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -o app .


# ---------- RUNTIME STAGE ----------
FROM alpine:3.19

# Certificates needed for HTTPS (important)
RUN apk add --no-cache ca-certificates

WORKDIR /root/

# Copy binary
COPY --from=builder /app/app .

# Expose port (optional but good practice)
EXPOSE 8080

# Run
CMD ["./app"]