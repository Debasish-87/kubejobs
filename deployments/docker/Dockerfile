# ----------- BUILD STAGE -----------
FROM golang:1.23-alpine AS builder

WORKDIR /app

# install git (needed by go mod download)
RUN apk add --no-cache git

# cache dependencies separately from source
COPY go.mod go.sum ./
RUN go mod download

# copy source code
COPY . .

# build a statically-linked binary (no CGO, no libc dependency)
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o app ./cmd/server


# ----------- RUNTIME STAGE -----------
FROM alpine:3.20

WORKDIR /app

# ca-certificates needed if the app ever makes outbound HTTPS calls
RUN apk update && apk add --no-cache ca-certificates

# copy only the compiled binary
COPY --from=builder /app/app .

# run as a non-root user
RUN adduser -D -g '' appuser
USER appuser

EXPOSE 8080

# Docker / K8s health check
HEALTHCHECK --interval=10s --timeout=3s --retries=3 \
  CMD wget -qO- http://localhost:8080/health || exit 1

CMD ["./app"]
