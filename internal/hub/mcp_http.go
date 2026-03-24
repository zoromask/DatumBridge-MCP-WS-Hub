package hub

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"
)

const (
	mcpProtocolVersion = "2024-11-05"
	mcpSessionHeader   = "Mcp-Session-Id"
)

// rpcEnvelope is a minimal JSON-RPC 2.0 request (datumbridge-mcp client uses POST /mcp).
type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	ID      json.RawMessage `json:"id"`
}

// HandleMCPStreamableHTTP implements Streamable HTTP MCP for DatumBridge publish/approve (initialize, tools/list, tools/call).
// Matches github.com/datumbridge/mcp/internal/publisher/client.MCPClient expectations.
func (h *Hub) HandleMCPStreamableHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	_ = r.Body.Close()

	var env rpcEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		writeMCPRPCError(w, nil, -32700, "parse error")
		return
	}
	if env.JSONRPC != "2.0" {
		writeMCPRPCError(w, env.ID, -32600, "invalid request")
		return
	}

	switch env.Method {
	case "initialize":
		h.handleMCPInitialize(w, env.ID)
	case "notifications/initialized":
		// Optional client notification; no JSON-RPC response body required.
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		h.handleMCPToolsList(w, r, env.ID)
	case "tools/call":
		h.handleMCPToolsCall(w, r, env)
	default:
		writeMCPRPCError(w, env.ID, -32601, "method not found: "+env.Method)
	}
}

func (h *Hub) handleMCPInitialize(w http.ResponseWriter, id json.RawMessage) {
	sid := h.mcpSessions.Create()
	w.Header().Set(mcpSessionHeader, sid)
	w.Header().Set("Content-Type", "application/json")

	result := map[string]interface{}{
		"protocolVersion": mcpProtocolVersion,
		"capabilities": map[string]interface{}{
			"tools": map[string]interface{}{},
		},
		"serverInfo": map[string]string{
			"name":    "datumbridge-mcp-ws-hub",
			"version": "1.0.0",
		},
	}
	writeMCPRPCResult(w, id, result)
	preview := sid
	if len(preview) > 8 {
		preview = preview[:8] + "…"
	}
	log.Debug().Str("mcp_session", preview).Msg("mcp initialize")
}

func (h *Hub) handleMCPToolsList(w http.ResponseWriter, r *http.Request, id json.RawMessage) {
	sess := r.Header.Get(mcpSessionHeader)
	if !h.mcpSessions.Valid(sess) {
		writeMCPRPCError(w, id, -32000, "invalid or missing Mcp-Session-Id (call initialize first)")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	writeMCPRPCResult(w, id, map[string]interface{}{"tools": mcpToolDescriptors()})
}

func mcpToolDescriptors() []map[string]interface{} {
	return []map[string]interface{}{
		{
			"name":        "hub_info",
			"description": "Return hub service metadata and a summary of registered / connected edge devices. Does not invoke device MCP.",
			"inputSchema": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			"name":        "forward_jsonrpc_to_device",
			"description": "Forward a JSON-RPC 2.0 payload to a connected edge device MCP server (same path as POST /api/v1/devices/{device_id}/mcp).",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"device_id": map[string]interface{}{
						"type":        "string",
						"description": "Registered device_id with an active WebSocket to the hub",
					},
					"jsonrpc_request": map[string]interface{}{
						"type":        "object",
						"description": "Full JSON-RPC object to send (e.g. {\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\",\"params\":{}})",
					},
				},
				"required": []string{"device_id", "jsonrpc_request"},
			},
		},
	}
}

type mcpCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (h *Hub) handleMCPToolsCall(w http.ResponseWriter, r *http.Request, env rpcEnvelope) {
	sess := r.Header.Get(mcpSessionHeader)
	if !h.mcpSessions.Valid(sess) {
		writeMCPRPCError(w, env.ID, -32000, "invalid or missing Mcp-Session-Id (call initialize first)")
		return
	}

	var params mcpCallParams
	if len(env.Params) > 0 && string(env.Params) != "null" {
		if err := json.Unmarshal(env.Params, &params); err != nil {
			writeMCPRPCError(w, env.ID, -32602, "invalid params")
			return
		}
	}
	if params.Arguments == nil {
		params.Arguments = []byte("{}")
	}

	w.Header().Set("Content-Type", "application/json")

	switch params.Name {
	case "hub_info":
		infos := h.ListDeviceInfos()
		summary, _ := json.Marshal(map[string]interface{}{
			"service":           "datumbridge-mcp-ws-hub",
			"protocol":          "MCP Streamable HTTP subset for DatumBridge publish/approve",
			"devices":           infos,
			"device_count":      len(infos),
			"connected_count":   countConnected(infos),
		})
		writeMCPRPCResult(w, env.ID, mcpToolResultText(string(summary)))

	case "forward_jsonrpc_to_device":
		var args struct {
			DeviceID       string          `json:"device_id"`
			JSONRPCRequest json.RawMessage `json:"jsonrpc_request"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			writeMCPRPCError(w, env.ID, -32602, "invalid arguments")
			return
		}
		args.DeviceID = strings.TrimSpace(args.DeviceID)
		if args.DeviceID == "" || len(args.JSONRPCRequest) == 0 {
			writeMCPRPCError(w, env.ID, -32602, "device_id and jsonrpc_request required")
			return
		}
		payload := bytesTrimSpace(args.JSONRPCRequest)
		if !json.Valid(payload) {
			writeMCPRPCError(w, env.ID, -32602, "jsonrpc_request must be valid JSON")
			return
		}
		resp, ok := h.ForwardRequest(args.DeviceID, payload, requestTimeout)
		if !ok {
			writeMCPRPCResult(w, env.ID, mcpToolResultError("device not connected or request timeout"))
			return
		}
		writeMCPRPCResult(w, env.ID, mcpToolResultText(string(resp)))

	default:
		writeMCPRPCError(w, env.ID, -32601, "unknown tool: "+params.Name)
	}
}

func countConnected(infos []DeviceInfo) int {
	n := 0
	for _, d := range infos {
		if d.Connected {
			n++
		}
	}
	return n
}

func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

func mcpToolResultText(text string) map[string]interface{} {
	return map[string]interface{}{
		"content": []map[string]string{
			{"type": "text", "text": text},
		},
		"isError": false,
	}
}

func mcpToolResultError(text string) map[string]interface{} {
	return map[string]interface{}{
		"content": []map[string]string{
			{"type": "text", "text": text},
		},
		"isError": true,
	}
}

func writeMCPRPCResult(w http.ResponseWriter, id json.RawMessage, result interface{}) {
	out := map[string]interface{}{"jsonrpc": "2.0", "result": result}
	if len(id) > 0 && string(id) != "null" {
		var idVal interface{}
		if err := json.Unmarshal(id, &idVal); err == nil {
			out["id"] = idVal
		}
	}
	_ = json.NewEncoder(w).Encode(out)
}

func writeMCPRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	out := map[string]interface{}{
		"jsonrpc": "2.0",
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
		},
	}
	if len(id) > 0 && string(id) != "null" {
		var idVal interface{}
		if err := json.Unmarshal(id, &idVal); err == nil {
			out["id"] = idVal
		}
	}
	_ = json.NewEncoder(w).Encode(out)
}
