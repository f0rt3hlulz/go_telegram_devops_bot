# syntax=docker/dockerfile:1.7

FROM golang:1.22-bullseye AS builder
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o devops-bot ./cmd/bot

FROM alpine:3.20
RUN addgroup -S bot && adduser -S bot -G bot \
    && apk add --no-cache ca-certificates curl

USER bot
WORKDIR /home/bot
COPY --from=builder /app/devops-bot /usr/local/bin/devops-bot

ENV HEALTH_ADDR=:8080
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
  CMD curl --fail http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/devops-bot"]
