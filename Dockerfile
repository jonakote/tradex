# Development target with Go compiler for live-mounted development
FROM golang:alpine AS dev
WORKDIR /app
COPY go.mod ./
RUN go mod download
COPY . .
CMD ["go", "run", "./cmd/tradex", "signals"]

# Production builder
FROM golang:alpine AS builder
WORKDIR /app
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /tradex ./cmd/tradex

# Production minimal runtime
FROM alpine:latest AS prod
RUN apk --no-cache add ca-certificates tzdata \
    && addgroup -S appgroup && adduser -S appuser -G appgroup
COPY --from=builder /tradex /usr/local/bin/tradex
USER appuser
ENTRYPOINT ["tradex"]
CMD ["signals"]
