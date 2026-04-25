# ----------- BUILD STAGE -----------
FROM golang:1.23-alpine AS builder

WORKDIR /app

# install dependencies
RUN apk add --no-cache git

# better caching
COPY go.mod go.sum ./
RUN go mod download

# copy source
COPY . .

# build optimized static binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o app ./cmd/server


# ----------- RUNTIME STAGE -----------
FROM alpine:3.20

WORKDIR /app

# install certs (required for HTTPS)
RUN apk add --no-cache ca-certificates

# copy binary
COPY --from=builder /app/app .

# create non-root user
RUN adduser -D -g '' appuser
USER appuser

EXPOSE 8080

# optional healthcheck (works with docker/k8s)
HEALTHCHECK --interval=10s --timeout=3s --retries=3 \
  CMD wget -qO- http://localhost:8080/health || exit 1

CMD ["./app"]