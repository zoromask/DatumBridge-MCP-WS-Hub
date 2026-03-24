# syntax=docker/dockerfile:1
# Build: copy only source trees (no full-context COPY — avoids baking .env / local secrets into layers).
# Runtime: do not use Dockerfile ENV/ARG for app secrets — they persist in image config (`docker inspect`).
#   Pass config at run time: `docker run -e HUB_REGISTER_API_KEY=... -e HUB_ALLOWED_ORIGINS=... -v hub-data:/data ...`
#   Optional: BuildKit secret mounts only if a future build step truly needs a secret (e.g. private module fetch).
FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 go build -o /mcp-ws-hub ./cmd/api

FROM alpine:3.19
RUN apk --no-cache add ca-certificates curl

RUN addgroup -g 1000 -S appuser && \
    adduser -u 1000 -S appuser -G appuser -s /sbin/nologin

COPY --from=builder /mcp-ws-hub /mcp-ws-hub

RUN mkdir -p /data && chown appuser:appuser /data

# Process CWD `/` + app default `data/devices.json` => persist credentials at /data/devices.json (mount a volume here).
WORKDIR /

EXPOSE 8082

HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:8082/health || exit 1

USER appuser

CMD ["/mcp-ws-hub"]
