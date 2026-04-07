package hub

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// HubDriftProtocolVersion is the datumbridge WebSocket control-plane version (hello / hub_handshake).
const HubDriftProtocolVersion = 1

// expectedEdgeVersion returns HUB_EXPECTED_EDGE_VERSION when the hub should flag version mismatch (optional).
func expectedEdgeVersion() string {
	return strings.TrimSpace(os.Getenv("HUB_EXPECTED_EDGE_VERSION"))
}

func computeDriftStatus(edgeVersion string) string {
	want := expectedEdgeVersion()
	ev := strings.TrimSpace(edgeVersion)
	if want == "" {
		if ev == "" {
			return "unknown"
		}
		return "unknown"
	}
	if ev == "" {
		return "warn"
	}
	if ev == want {
		return "ok"
	}
	return "warn"
}

func (c *Conn) applyEdgeHello(version, gitSHA string, protocol int, drift string, caps EdgeCapabilityFields) {
	if c == nil {
		return
	}
	c.edgeMu.Lock()
	defer c.edgeMu.Unlock()
	c.EdgeVersion = version
	c.EdgeGitSHA = gitSHA
	c.EdgeProto = protocol
	c.DriftStatus = drift
	c.LocalLLMAvailable = caps.LocalLLMAvailable
	c.SupportsEdgeMission = caps.SupportsEdgeMission
	c.MissionToolName = caps.MissionToolName
	c.EdgeMissionProtocol = caps.EdgeMissionProtocol
	c.HelloAt = time.Now().UTC()
}

func (c *Conn) edgeSnapshot() (version, gitSHA string, protocol int, drift string, helloAt time.Time, caps EdgeCapabilityFields) {
	if c == nil {
		return "", "", 0, "unknown", time.Time{}, EdgeCapabilityFields{}
	}
	c.edgeMu.RLock()
	defer c.edgeMu.RUnlock()
	d := c.DriftStatus
	if d == "" {
		d = "unknown"
	}
	caps = EdgeCapabilityFields{
		LocalLLMAvailable:   c.LocalLLMAvailable,
		SupportsEdgeMission: c.SupportsEdgeMission,
		MissionToolName:     c.MissionToolName,
		EdgeMissionProtocol: c.EdgeMissionProtocol,
	}
	return c.EdgeVersion, c.EdgeGitSHA, c.EdgeProto, d, c.HelloAt, caps
}

// isMCPJSONRPCResponse is true for JSON-RPC 2.0 responses (result or error, no method).
func isMCPJSONRPCResponse(msg []byte) bool {
	var v struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(msg, &v); err != nil {
		return false
	}
	if v.Method != "" {
		return false
	}
	if v.JSONRPC != "" && v.JSONRPC != "2.0" {
		return false
	}
	hasResult := len(v.Result) > 0 && string(v.Result) != "null"
	hasError := len(v.Error) > 0 && string(v.Error) != "null"
	return hasResult || hasError
}

// HandleDeviceWSInbound routes inbound WebSocket text: MCP JSON-RPC responses vs edge hello / control.
func (h *Hub) HandleDeviceWSInbound(deviceID string, msg []byte) {
	if isMCPJSONRPCResponse(msg) {
		h.DeliverResponse(deviceID, msg)
		return
	}
	if h.tryConsumeEdgeHello(deviceID, msg) {
		return
	}
	var legacy struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(msg, &legacy) == nil && legacy.Type == "register" {
		return
	}
	log.Debug().Str("device_id", deviceID).Msg("ws inbound ignored: not MCP JSON-RPC response or edge hello")
}

func (h *Hub) tryConsumeEdgeHello(deviceID string, msg []byte) bool {
	var wrap struct {
		Datumbridge json.RawMessage `json:"datumbridge"`
	}
	if json.Unmarshal(msg, &wrap) == nil && len(wrap.Datumbridge) > 0 && string(wrap.Datumbridge) != "null" {
		var inner struct {
			V    int `json:"v"`
			Edge struct {
				Protocol            int            `json:"protocol"`
				Version             string         `json:"version"`
				GitSHA              string         `json:"git_sha"`
				ServerName          string         `json:"server_name"`
				LocalLLMAvailable   *bool          `json:"local_llm_available,omitempty"`
				SupportsEdgeMission *bool          `json:"supports_edge_mission,omitempty"`
				MissionToolName     string         `json:"mission_tool_name,omitempty"`
				EdgeMissionProtocol int            `json:"edge_mission_protocol,omitempty"`
				Capabilities        *edgeCapsBlock `json:"capabilities,omitempty"`
			} `json:"edge"`
		}
		if err := json.Unmarshal(wrap.Datumbridge, &inner); err != nil || inner.V < 1 {
			return false
		}
		evVer := strings.TrimSpace(inner.Edge.Version)
		evSha := strings.TrimSpace(inner.Edge.GitSHA)
		proto := inner.Edge.Protocol
		if proto <= 0 {
			proto = HubDriftProtocolVersion
		}
		drift := computeDriftStatus(evVer)
		caps := mergeEdgeCaps(inner.Edge.Capabilities, inner.Edge.LocalLLMAvailable, inner.Edge.SupportsEdgeMission, inner.Edge.MissionToolName, inner.Edge.EdgeMissionProtocol)
		h.applyEdgeHelloToDevice(deviceID, evVer, evSha, proto, drift, caps)
		log.Info().Str("device_id", deviceID).Str("edge_version", evVer).Str("drift_status", drift).Msg("edge hello (datumbridge envelope)")
		h.queueHubHandshake(deviceID)
		return true
	}

	var rpc struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if json.Unmarshal(msg, &rpc) != nil || rpc.JSONRPC != "2.0" {
		return false
	}
	if rpc.Method != "datumbridge/edge_hello" {
		return false
	}
	var params struct {
		Version             string         `json:"version"`
		GitSHA              string         `json:"git_sha"`
		Protocol            int            `json:"protocol"`
		LocalLLMAvailable   *bool          `json:"local_llm_available,omitempty"`
		SupportsEdgeMission *bool          `json:"supports_edge_mission,omitempty"`
		MissionToolName     string         `json:"mission_tool_name,omitempty"`
		EdgeMissionProtocol int            `json:"edge_mission_protocol,omitempty"`
		Capabilities        *edgeCapsBlock `json:"capabilities,omitempty"`
	}
	if len(rpc.Params) > 0 && string(rpc.Params) != "null" {
		_ = json.Unmarshal(rpc.Params, &params)
	}
	evVer := strings.TrimSpace(params.Version)
	evSha := strings.TrimSpace(params.GitSHA)
	proto := params.Protocol
	if proto <= 0 {
		proto = HubDriftProtocolVersion
	}
	drift := computeDriftStatus(evVer)
	caps := mergeEdgeCaps(params.Capabilities, params.LocalLLMAvailable, params.SupportsEdgeMission, params.MissionToolName, params.EdgeMissionProtocol)
	h.applyEdgeHelloToDevice(deviceID, evVer, evSha, proto, drift, caps)
	log.Info().Str("device_id", deviceID).Str("edge_version", evVer).Str("drift_status", drift).Msg("edge hello (json-rpc)")
	h.queueHubHandshake(deviceID)
	return true
}

func (h *Hub) applyEdgeHelloToDevice(deviceID, version, gitSHA string, protocol int, drift string, caps EdgeCapabilityFields) {
	h.mu.RLock()
	c := h.conns[deviceID]
	h.mu.RUnlock()
	if c == nil {
		return
	}
	c.applyEdgeHello(version, gitSHA, protocol, drift, caps)
}

func (h *Hub) queueHubHandshake(deviceID string) {
	params := map[string]interface{}{
		"hub_protocol":          HubDriftProtocolVersion,
		"expected_edge_version": expectedEdgeVersion(),
	}
	h.mu.RLock()
	c := h.conns[deviceID]
	h.mu.RUnlock()
	if c != nil {
		ver, sha, proto, drift, _, _ := c.edgeSnapshot()
		params["edge_reported_version"] = ver
		params["edge_git_sha"] = sha
		params["edge_protocol"] = proto
		params["drift_status"] = drift
	} else {
		params["drift_status"] = "unknown"
	}
	payload := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "datumbridge/hub_handshake",
		"params":  params,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	c2 := h.Get(deviceID)
	if c2 == nil {
		return
	}
	select {
	case c2.Send <- b:
	default:
		log.Warn().Str("device_id", deviceID).Msg("hub handshake: device send buffer full")
	}
}
