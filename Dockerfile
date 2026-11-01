FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /queue_bot ./cmd/server

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app

COPY --from=builder /queue_bot /queue_bot
COPY --from=builder /app/migrations /app/migrations
COPY --from=builder /app/config.yaml /app/config.yaml

HEALTHCHECK --interval=2s --timeout=1s --start-period=60s --retries=3 CMD test -f /tmp/queuebot-ready

CMD ["/queue_bot"]
