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

Pairing codes expire after **`HUB_PAIRING_TTL`** (default `1m`; see Environment Variables).

## Installation & setup (Kubernetes + DatumBridge Studio + DTBClaw)

Typical layout: **hub** runs in one namespace (e.g. `mcp-tools`), **DatumBridge Studio** in another (e.g. `datumbridge-adk-db`). Edge devices and operators use the **same public Studio URL** as for `/api/mcp` and `/api/adk`, under **`/api/ws-hub`**.

### 1. Deploy the hub

1. Apply your hub `Deployment` and `Service` (container listens on **`8000`** by default unless `HUB_PORT` is set).
2. Note the **Kubernetes Service name** and **namespace** (Helm often suffixes the name, e.g. `datumbridge-mcp-ws-hub-main` in `mcp-tools`).
3. Confirm endpoints: `kubectl -n <hub-ns> get svc,endpoints <hub-service-name>`.

### 2. Configure DatumBridge Studio (nginx)

Studio’s image patches `nginx.conf` at container start (see `datumbridge-studio/docker-entrypoint.d/99-datumbridge-nginx-resolver.sh`).

- Set on the **Studio** pod:

  **`WS_HUB_UPSTREAM`** = **`<hub-service>.<hub-namespace>.svc.cluster.local:<port>`**  
  Example: `datumbridge-mcp-ws-hub-main.mcp-tools.svc.cluster.local:8000`

  A **short** name like `datumbridge-mcp-ws-hub:8000` only works when the Service is in the **same** namespace as Studio.

- Reference manifest: `dtb-agent-kit/k8s/studio-deployment.yaml` (env + NodePort example).

Rebuild/redeploy Studio after changing `nginx.conf` or the entrypoint script.

### 3. Verify routing

From a machine that reaches Studio (replace host/port):

```bash
curl -sS -o /dev/null -w "%{http_code}\n" "http://<studio-host>:30080/api/ws-hub/health"
# expect 200
```

From **inside** the Studio pod (optional):

```bash
kubectl -n <studio-ns> exec deploy/datumbridge-studio -c studio -- wget -qO- "http://127.0.0.1/api/ws-hub/health"
wget -qO- "http://<hub-fqdn>:8000/health"   # direct to hub Service
```

### 4. Register DTBClaw (OctoClaw) and run the gateway

Use the **public Studio base** (not `*.svc.cluster.local`) so the device can reach the hub from outside the cluster:

```bash
octoclaw register --hub-url "http://<studio-host>:<nodePort>/api/ws-hub"
```

Complete pairing (6-digit code from Studio **MCP WS Hub** UI or hub logs). Then:

```bash
octoclaw gateway
```

Credentials are stored under **`[gateway.mcp_hub]`** (`url`, `device_id`, `token`). Env overrides: `MCP_HUB_URL`, `MCP_DEVICE_ID`, `MCP_HUB_TOKEN`.

The gateway opens **WebSocket** `wss://` or `ws://` to `{url}/ws?device_id=...&token=...` (same `url` as above).

### 5. Tool Registry (datumbridge-mcp)

- **In-cluster** tool execute URL should target the **hub Service**, not Studio, e.g.  
  `baseURL`: `http://<hub-service>.<hub-namespace>.svc.cluster.local:8000/api/v1/devices/<device_id>`  
  (datumbridge-mcp appends `/mcp` when calling.)
- Publish/approve MCP Streamable HTTP uses **`POST {baseURL}/mcp`**, which is satisfied via the hub’s `/mcp` route.

### 6. Troubleshooting

| Symptom | Likely cause |
|--------|----------------|
| Studio pod **crash loop** on nginx “host not found in upstream” | Use variable `proxy_pass` + `WS_HUB_UPSTREAM` + resolver script (current Studio image). |
| **502** on `/api/ws-hub/*` | Wrong `WS_HUB_UPSTREAM`, hub has no Endpoints, or NetworkPolicy blocks Studio → hub. |
| `wget: bad address` to hub FQDN | Service name/namespace typo; confirm with `kubectl get svc -n <hub-ns>`. |
| **`error decoding response body`** on `octoclaw register` | Response was HTML (SPA): Ingress stripped `/api/ws-hub` or Studio nginx missing hub locations; use current `datumbridge-studio/nginx.conf` (proxies `/api/ws-hub/*`, `/api/v1/devices/*`, `/mcp`, `/ws`). |
| **405** on register | Request hit hub static `FileServer`; fixed by hub **`HUB_HTTP_STRIP_PREFIX`** path strip or correct proxy path stripping (see below). |

## Setup

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `HUB_PORT` | `8000` | HTTP/WS listen port |
| `HUB_CREDENTIALS_FILE` | `./data/devices.json` | Path to persist device credentials |
| `HUB_REGISTER_API_KEY` | _(empty = open)_ | API key for `/register` endpoint (X-API-Key or Bearer) |
| `HUB_ALLOWED_ORIGINS` | _(empty: WebSocket same-origin or no `Origin`; HTTP CORS still echoes caller `Origin`)_ | Comma-separated origins (exact match) for WebSocket; same list restricts CORS when set |
| `HUB_PAIRING_TTL` | `1m` | Pairing code lifetime (Go duration) |
| `HUB_HTTP_STRIP_PREFIX` | _(on)_ | Set `0` / `false` / `no` to disable stripping leading `/api/ws-hub` from incoming paths (only if your proxy never sends that prefix) |
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

## Kubernetes: extra notes (TLS, CORS, publish URLs)

Step-by-step install is in **[Installation & setup (Kubernetes + DatumBridge Studio + DTBClaw)](#installation--setup-kubernetes--datumbridge-studio--dtbclaw)** above.

Inside the cluster, `datumbridge-mcp` uses **in-cluster** tool/base URLs like  
`http://{slug}-{version}.{namespace}.svc.cluster.local:{port}` (or the hub Service FQDN). **Edge devices** must use the **Studio** edge URL: **`https://<studio-host>/api/ws-hub`** and **`wss://<studio-host>/api/ws-hub/ws`**.

- **Ingress / TLS**: Terminate HTTPS at Studio; allow **WebSocket** upgrades on `/api/ws-hub/` (`Upgrade`, `Connection`, long read/send timeouts — see `datumbridge-studio/nginx.conf`).
- **`HUB_ALLOWED_ORIGINS`**: Set to your Studio origin(s) in production so browser clients can open WebSockets.
- **CORS**: Hub echoes allowed `Origin`; align with Studio.
- **405 / path prefix**: Hub may strip **`/api/ws-hub`** for misconfigured upstreams; disable with **`HUB_HTTP_STRIP_PREFIX=0`** only if your proxy never sends that prefix.
- **Standalone hub** (no Studio): expose the hub with its own Ingress/LB/NodePort and point `octoclaw register --hub-url` at that host (e.g. `http://hub.example.com:8000`).

## DatumBridge MCP publish / Request Approve

The platform’s MCP service calls `POST {baseURL}/mcp` with JSON-RPC `initialize` and `tools/list` (same contract as `google-drive-mcp` via FastMCP HTTP). This hub implements that subset so tools can be registered after deploy.

- Set **`server_port` to `8000`** (or your `HUB_PORT`) when publishing so `datumbridge-mcp` builds the correct Kubernetes endpoint.
- Advertised tools:
  - **`hub_info`** — hub + device summary (no device MCP).
  - **`forward_jsonrpc_to_device`** — same behavior as `POST /api/v1/devices/{device_id}/mcp`.
  - **Native DTBClaw / OctoClaw tools** (for example `shell`, `file_read`, `memory_store`, `browser`, …) — one MCP tool per native tool; each call requires **`device_id`** and is forwarded as `tools/call` on the device. The list is embedded from `internal/hub/dtbclaw_edge_catalog.json`, generated by the octoclaw crate: `cargo run --bin export_dtbclaw_edge_catalog` in **DTBClaw** (copy output into this file when the tool surface changes).

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
│   ├── mcp_http.go          # MCP Streamable HTTP (`/mcp`): hub tools + DTBClaw edge relay
│   ├── dtbclaw_catalog.go   # Embedded `dtbclaw_edge_catalog.json` (tools/list + relay set)
│   ├── dtbclaw_edge_catalog.json  # Sync from DTBClaw: `cargo run --bin export_dtbclaw_edge_catalog`
│   ├── auth.go              # Credential store (bcrypt), pairing flow
│   ├── middleware.go         # Logging + CORS middleware
│   ├── hub_test.go          # Hub unit tests
│   └── auth_test.go         # Auth unit tests
├── Dockerfile               # Multi-stage build, non-root, healthcheck
├── .env.example
└── README.md
```
