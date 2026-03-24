# Build only explicit source paths — never COPY entire context (avoids leaking .env / secrets into layers).
# Runtime secrets: pass via `docker run -e` or Kubernetes secrets + env; credential file: volume mount to /data.
FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 go build -o /mcp-ws-hub ./cmd/api

FROM alpine:3.19
RUN apk --no-cache add ca-certificates curl

# Create non-root user
RUN addgroup -g 1000 -S appuser && \
    adduser -u 1000 -S appuser -G appuser -s /sbin/nologin

COPY --from=builder /mcp-ws-hub /mcp-ws-hub

# Create data directory owned by appuser
RUN mkdir -p /data && chown appuser:appuser /data

# Non-secret defaults only (API keys and similar must be supplied at runtime, not at build).
ENV HUB_PORT=8082 \
    HUB_CREDENTIALS_FILE=/data/devices.json

EXPOSE 8082

HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:8082/health || exit 1

USER appuser

CMD ["/mcp-ws-hub"]
