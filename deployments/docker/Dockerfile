FROM golang:1.23-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN ls -R /app

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -o app ./cmd/server


FROM alpine:3.19

RUN apk add --no-cache ca-certificates

WORKDIR /root/

COPY --from=builder /app/app .

CMD ["./app"]
