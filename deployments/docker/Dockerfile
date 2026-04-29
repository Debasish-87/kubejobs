# ---------- BUILD STAGE ----------
FROM golang:1.22-alpine AS builder

WORKDIR /app

# deps
RUN apk add --no-cache git

# cache deps
COPY go.mod go.sum ./
RUN go mod download

# copy full project
COPY . .

# 🔥 FIX: explicit main.go path
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o app ./cmd/server/main.go


# ---------- RUNTIME ----------
FROM alpine:3.19

WORKDIR /root/

RUN apk add --no-cache ca-certificates

COPY --from=builder /app/app .

EXPOSE 8080

CMD ["./app"]