FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git gcc musl-dev

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=1 go build -o flatrun-agent ./cmd/agent

FROM alpine:3.19

RUN apk add --no-cache ca-certificates docker-cli docker-cli-compose wget curl

WORKDIR /app

COPY --from=builder /app/flatrun-agent .
COPY test/e2e/config.lua.yml /app/config.yml

RUN mkdir -p /deployments

EXPOSE 8090

CMD ["./flatrun-agent", "-config", "/app/config.yml"]
