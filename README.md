# DatumBridge MCP WebSocket Hub

WebSocket relay that bridges DatumBridge platform (HTTP/cloud) with DTBClaw devices (edge) running MCP servers. Forwards JSON-RPC over WebSocket with request/response correlation.

## Architecture

```
DatumBridge Platform          MCP WS Hub              DTBClaw Device
(HTTP client)                 (this service)          (MCP server)
                                                       
POST /api/v1/devices/         ┌──────────────┐         
  {device_id}/mcp  ─────────▶│  HTTP → WS   │         
  (JSON-RPC request)          │  Proxy with  │◀═══════▶ WebSocket
                              │  correlation │         (persistent)
  ◀──────────────────────────│  & timeout   │         
  (JSON-RPC response)         └──────────────┘         
```

## API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/health` | Health check |
| POST | `/mcp` | **MCP Streamable HTTP** (for DatumBridge `datumbridge-mcp` publish/approve): `initialize` → `Mcp-Session-Id` header, then `tools/list`, `tools/call` |
| POST | `/api/v1/devices/register` | Register a new device (returns device_id + token) |
| POST | `/api/v1/devices/register/confirm` | Confirm 6-digit pairing code |
| GET | `/api/v1/devices` | List connected devices |
| POST | `/api/v1/devices/{device_id}/mcp` | Forward JSON-RPC to device |
| DELETE | `/api/v1/devices/{device_id}` | Revoke device credential and disconnect |
| GET | `/api/v1/pairing/pending` | List pending pairing codes |
| WS | `/ws?device_id=...&token=...` | Device WebSocket connection |
| GET | `/` | Test UI |

## Device Registration Flow

### Direct Registration

```bash
curl -X POST http://localhost:8000/api/v1/devices/register \
  -H "Content-Type: application/json" \
  -H "X-API-Key: $HUB_REGISTER_API_KEY" \
  -d '{"device_id": "my-device"}'
# Returns: {"device_id":"my-device","token":"<64-char-hex>"}
```

### 6-Digit Pairing Flow

1. **Hub side**: `POST /api/v1/devices/register` with `{"pairing": true}` → returns `pairing_code`
2. **Device side**: `zeroclaw register` → prompts for the 6-digit code
3. **Device side**: `POST /api/v1/devices/register/confirm` with `{"pairing_code": "123456"}` → returns `device_id` + `token`
4. Device connects to `/ws?device_id=...&token=...`

Pairing codes expire after 5 minutes.

## Setup

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `HUB_PORT` | `8000` | HTTP/WS listen port |
| `HUB_CREDENTIALS_FILE` | `./data/devices.json` | Path to persist device credentials |
| `HUB_REGISTER_API_KEY` | _(empty = open)_ | API key for `/register` endpoint (X-API-Key or Bearer) |
| `HUB_ALLOWED_ORIGINS` | _(empty: WebSocket same-origin or no `Origin`; HTTP CORS still echoes caller `Origin`)_ | Comma-separated origins (exact match) for WebSocket; same list restricts CORS when set |
| `LOG_LEVEL` | `INFO` | Log level: DEBUG, INFO, WARN, ERROR |

### Run Locally

```bash
# Copy and edit environment
cp .env.example .env

# Run
go run ./cmd/api
```

### Docker

```bash
docker build -t datumbridge-mcp-ws-hub .
docker run -p 8000:8000 \
  -e HUB_REGISTER_API_KEY=my-secret-key \
  -v hub-data:/data \
  datumbridge-mcp-ws-hub
```

The image does not bake hub settings with `ENV` (so nothing sensitive or operational ends up in image metadata from `docker inspect`). Defaults: port `8000`, credentials file `data/devices.json` relative to `WORKDIR /` → `/data/devices.json` with the volume above. Override with `-e HUB_PORT`, `-e HUB_CREDENTIALS_FILE`, etc., as needed.

- **Health check**: `curl http://localhost:8000/health`
- **Test UI**: `http://localhost:8000/`

## Kubernetes: Studio proxy, edge URLs, and in-cluster registration

Inside the cluster, `datumbridge-mcp` typically registers tools using an **internal** URL like
`http://{slug}-{version}.{namespace}.svc.cluster.local:{port}`. **Edge devices and browsers outside the cluster cannot use that DNS name.**

In the **DatumBridge stack**, you front the hub through **DatumBridge Studio**—same pattern as **`/api/mcp`** and the agent API. Studio reverse-proxies **`/api/ws-hub`** to the **`datumbridge-mcp-ws-hub`** Service (container port **`8000`** by default; align `server_port` in Tool Registry with the hub process).

- **Edge / browser HTTP base**: `https://<studio-host>/api/ws-hub`
- **Device WebSocket URL**: `wss://<studio-host>/api/ws-hub/ws`

Your **Studio** ingress (or load balancer) must allow **WebSocket upgrades** on `/api/ws-hub/` (pass `Upgrade` and `Connection`, with sufficient proxy read/send timeouts—see Studio `nginx.conf`).

1. **Service**: Ensure a Kubernetes `Service` for the hub Deployment is reachable from Studio at the name/port configured in Studio (e.g. `http://datumbridge-mcp-ws-hub:8000/`).

2. **TLS**: Terminate HTTPS at Studio; edge clients use **`https://`** / **`wss://`** via the Studio host, not a separate public hostname for the hub.

3. **`HUB_ALLOWED_ORIGINS`**: Set to the exact Studio origin allowed to open browser WebSocket connections (e.g. `https://studio.example.com`). If unset, same-origin / missing-`Origin` behavior applies (see code).

4. **CORS**: The hub's middleware echoes `Origin` when allowed; keep values aligned with your Studio origin.

5. **405 on POST (register / MCP)** — If your ingress forwards the **full** path (`/api/ws-hub/api/v1/...`) to the hub instead of stripping the prefix (Studio `nginx.conf` strips it), the request used to fall through to the static file server (**405**). The hub now strips a leading **`/api/ws-hub`** before routing. Set **`HUB_HTTP_STRIP_PREFIX=0`** only if your proxy never sends that prefix.

**Standalone hub** (without Studio): expose the hub Service with its own Ingress/LB/NodePort, enable WebSockets on that path, and point clients at that host directly. The embedded test UI remains at `/` on the hub.

## DatumBridge MCP publish / Request Approve

The platform’s MCP service calls `POST {baseURL}/mcp` with JSON-RPC `initialize` and `tools/list` (same contract as `google-drive-mcp` via FastMCP HTTP). This hub implements that subset so tools can be registered after deploy.

- Set **`server_port` to `8000`** (or your `HUB_PORT`) when publishing so `datumbridge-mcp` builds the correct Kubernetes endpoint.
- Advertised tools: **`hub_info`** (hub + device summary) and **`forward_jsonrpc_to_device`** (same behavior as `POST /api/v1/devices/{device_id}/mcp`).

## Security

- **Token hashing**: Device tokens are stored as bcrypt hashes. Plain-text tokens are returned only once at registration.
- **Registration protection**: Set `HUB_REGISTER_API_KEY` to require an API key for device registration.
- **CORS control**: Set `HUB_ALLOWED_ORIGINS` to restrict WebSocket and HTTP origins in production.
- **Non-root container**: Docker image runs as unprivileged `appuser`.
- **Body size limit**: HTTP request bodies are limited to 1 MB.
- **Connection health**: WebSocket connections use ping/pong heartbeats (54s interval, 60s timeout).

## Testing

```bash
go test ./internal/hub/ -v
```

## Project Structure

```
datumbridge-mcp-ws-hub/
├── cmd/api/
│   ├── main.go              # Entry point, routing, graceful shutdown
│   └── web/index.html       # Embedded test UI
├── internal/hub/
│   ├── hub.go               # Core Hub: connection registry, request correlation
│   ├── ws.go                # WebSocket handler with ping/pong
│   ├── http.go              # REST handlers, standardized error responses
│   ├── auth.go              # Credential store (bcrypt), pairing flow
│   ├── middleware.go         # Logging + CORS middleware
│   ├── hub_test.go          # Hub unit tests
│   └── auth_test.go         # Auth unit tests
├── Dockerfile               # Multi-stage build, non-root, healthcheck
├── .env.example
└── README.md
```
