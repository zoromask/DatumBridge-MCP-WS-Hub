# Design: DatumBridge MCP WebSocket Hub

## Purpose

The MCP WebSocket Hub acts as a relay between the DatumBridge cloud platform (which speaks HTTP) and DTBClaw edge devices (which run local MCP servers over WebSocket). It is **not** an MCP server itself — it is a transparent proxy that forwards JSON-RPC messages and correlates request/response pairs.

## Architecture

```
┌─────────────────┐         ┌─────────────────────┐         ┌─────────────────┐
│   DatumBridge   │  HTTP   │   MCP WebSocket Hub │   WS    │   DTBClaw       │
│   Platform      │────────▶│                     │◀═══════▶│   Device        │
│   (Cloud)       │◀────────│   - Auth (bcrypt)   │         │   (Edge MCP)    │
│                 │         │   - Correlation     │         │                 │
│                 │         │   - Timeout (60s)   │         │                 │
└─────────────────┘         └─────────────────────┘         └─────────────────┘
```

## Key Design Decisions

### 1. WebSocket Proxy (not MCP Server)

The hub does not implement MCP tools. It acts purely as an HTTP-to-WebSocket bridge:
- Cloud sends `POST /api/v1/devices/{id}/mcp` with JSON-RPC body
- Hub forwards the body to the device over WebSocket
- Device processes the MCP request and sends JSON-RPC response
- Hub correlates the response using `deviceID|rpcID` and returns it to the HTTP caller

This keeps the hub stateless regarding MCP semantics.

### 2. Device Authentication

Devices authenticate with a server-generated token:
1. Registration creates a random 256-bit token
2. Plain-text token is returned once to the registrant
3. Token is stored as a bcrypt hash on disk
4. Device presents the plain token when connecting via WebSocket
5. Hub validates using `bcrypt.CompareHashAndPassword`

Two registration flows are supported:
- **Direct**: `POST /register` → immediate `{device_id, token}` response
- **Pairing**: `POST /register {pairing:true}` → 6-digit code displayed on hub → device enters code → `POST /register/confirm {code}` → `{device_id, token}`

### 3. Request/Response Correlation

Pending HTTP requests are tracked in `map[string]*pendingReq` keyed by `"deviceID|rpcID"`:
- JSON-RPC `id` field is extracted from the outgoing request
- A buffered channel is created and stored in the pending map
- When a response arrives on the WebSocket, its `id` is matched to the pending entry
- The response is delivered via the channel, and the HTTP handler returns it
- A timer fires after 60s to prevent leaks if the device never responds

### 4. Connection Health

WebSocket connections use gorilla/websocket ping/pong:
- Hub sends pings every 54 seconds
- If pong is not received within 60 seconds, the connection is considered dead
- Dead connections are automatically unregistered, and pending requests are canceled

### 5. Security Layers

| Layer | Mechanism |
|-------|-----------|
| Token storage | bcrypt (cost 10) |
| Registration | Protected by `HUB_REGISTER_API_KEY` (optional) |
| WebSocket | Token validated before upgrade |
| CORS | `HUB_ALLOWED_ORIGINS` (configurable) |
| Body size | 1 MB limit via `http.MaxBytesReader` |
| Container | Non-root user in Docker |

### 6. Error Response Format

REST endpoints use a standardized error structure matching other DatumBridge MCP servers:
```json
{
  "error_code": "VALIDATION_ERROR",
  "error_message": "device_id required",
  "retryable": false
}
```

The MCP proxy endpoint (`/mcp`) returns JSON-RPC error format:
```json
{
  "jsonrpc": "2.0",
  "id": null,
  "error": {"code": -32000, "message": "device not connected or request timeout"}
}
```

## Alignment with DatumBridge Platform

This hub follows the same conventions as other MCP services (`google-drive-mcp`, `social-listening`):
- Health endpoint at `/health`
- Docker with multi-stage build, non-root user, HEALTHCHECK
- Standardized error responses
- Environment-based configuration with `.env.example`
- Structured logging (zerolog)

The key difference is that this service is a **transport layer** rather than a tool provider. It enables the DatumBridge Execution Engine to reach MCP servers running on edge devices without requiring direct network access.
