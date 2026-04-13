FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go test ./...

RUN go build -o /queue_bot ./cmd/server/

FROM alpine:latest
RUN apk add --no-cache ca-certificates
WORKDIR /app 

COPY --from=builder /queue_bot /queue_bot
COPY --from=builder /app/migrations /app/migrations

CMD ["/queue_bot"]