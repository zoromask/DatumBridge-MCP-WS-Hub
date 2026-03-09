FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /mcp-ws-hub ./cmd/api

FROM alpine:3.19
RUN apk --no-cache add ca-certificates curl

# Create non-root user
RUN addgroup -g 1000 -S appuser && \
    adduser -u 1000 -S appuser -G appuser -s /sbin/nologin

COPY --from=builder /mcp-ws-hub /mcp-ws-hub

# Create data directory owned by appuser
RUN mkdir -p /data && chown appuser:appuser /data

ENV HUB_PORT=8082 \
    HUB_CREDENTIALS_FILE=/data/devices.json

EXPOSE 8082

HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:8082/health || exit 1

USER appuser

CMD ["/mcp-ws-hub"]
