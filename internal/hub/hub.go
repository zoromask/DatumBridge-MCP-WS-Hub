package hub

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Conn represents a registered device WebSocket connection
type Conn struct {
	DeviceID    string
	Send        chan []byte
	ConnectedIP string
	ConnectedAt time.Time

	edgeMu      sync.RWMutex
	EdgeVersion string
	EdgeGitSHA  string
	EdgeProto   int
	DriftStatus string    // unknown | ok | warn
	HelloAt     time.Time // UTC when last edge hello applied
}

// DeviceInfo is the combined view of a device (persisted metadata + live connection state).
type DeviceInfo struct {
	DeviceID     string `json:"device_id"`
	DeviceName   string `json:"device_name,omitempty"`
	DeviceIP     string `json:"device_ip,omitempty"`
	DeviceMAC    string `json:"device_mac,omitempty"`
	RegisteredAt string `json:"registered_at,omitempty"`
	Connected    bool   `json:"connected"`
	ConnectedIP  string `json:"connected_ip,omitempty"`
	ConnectedAt  string `json:"connected_at,omitempty"`
	// From edge WebSocket hello (when connected); drift vs HUB_EXPECTED_EDGE_VERSION.
	EdgeVersion      string `json:"edge_version,omitempty"`
	EdgeGitSHA       string `json:"edge_git_sha,omitempty"`
	EdgeProtocol     int    `json:"edge_protocol,omitempty"`
	DriftStatus      string `json:"drift_status,omitempty"`
	EdgeHelloAt      string `json:"edge_hello_at,omitempty"`
	HubExpectedEdgeV string `json:"hub_expected_edge_version,omitempty"`
}

// pendingReq holds a waiting HTTP request for a JSON-RPC response
type pendingReq struct {
	ch    chan []byte
	timer *time.Timer
}

// Hub maintains device_id -> connection mapping and request/response correlation
type Hub struct {
	mu           sync.RWMutex
	conns        map[string]*Conn
	pending      map[string]*pendingReq // key: "deviceID|rpcID"
	pendingMu    sync.Mutex
	creds        *credentialStore
	pairingStore *pairingStore
	mcpSessions  *mcpSessionStore // Streamable HTTP /mcp (initialize + tools/list)
}

// credentialsFilePath returns the path for persisting device credentials.
// Uses HUB_CREDENTIALS_FILE env, default ./data/devices.json
func credentialsFilePath() string {
	if p := os.Getenv("HUB_CREDENTIALS_FILE"); p != "" {
		return p
	}
	return filepath.Join("data", "devices.json")
}

// New creates a new Hub. Device credentials are persisted to file (HUB_CREDENTIALS_FILE or ./data/devices.json)
// so registered devices can reconnect after server restart.
func New() *Hub {
	credsPath := credentialsFilePath()
	creds := newCredentialStore(credsPath)
	if n := creds.Count(); n > 0 {
		log.Info().Str("file", credsPath).Int("devices", n).Msg("loaded persisted device credentials")
	}
	return &Hub{
		conns:        make(map[string]*Conn),
		pending:      make(map[string]*pendingReq),
		creds:        creds,
		pairingStore: newPairingStore(),
		mcpSessions:  newMCPSessionStore(),
	}
}

// Register adds a device connection. Replaces existing connection for same device_id.
func (h *Hub) Register(deviceID string, send chan []byte, connectedIP string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.conns[deviceID]; ok {
		close(old.Send)
	}
	h.conns[deviceID] = &Conn{
		DeviceID:    deviceID,
		Send:        send,
		ConnectedIP: connectedIP,
		ConnectedAt: time.Now().UTC(),
	}
}

// RevokeDevice disconnects the device (if connected) and removes its credential.
// The device cannot reconnect until it registers again.
func (h *Hub) RevokeDevice(deviceID string) {
	h.Unregister(deviceID)
	h.creds.RevokeDevice(deviceID)
}

// Unregister removes a device connection
func (h *Hub) Unregister(deviceID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c, ok := h.conns[deviceID]; ok {
		close(c.Send)
		delete(h.conns, deviceID)
	}
	// Cancel any pending requests for this device
	h.pendingMu.Lock()
	for k, pr := range h.pending {
		if len(k) > len(deviceID) && k[:len(deviceID)] == deviceID && k[len(deviceID)] == '|' {
			pr.timer.Stop()
			close(pr.ch)
			delete(h.pending, k)
		}
	}
	h.pendingMu.Unlock()
}

// Get returns the connection for a device, or nil if not registered
func (h *Hub) Get(deviceID string) *Conn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.conns[deviceID]
}

// ListDeviceIDs returns all connected device IDs
func (h *Hub) ListDeviceIDs() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	ids := make([]string, 0, len(h.conns))
	for id := range h.conns {
		ids = append(ids, id)
	}
	return ids
}

// ListDeviceInfos returns detailed info for all registered devices,
// merging persisted metadata with live connection state.
func (h *Hub) ListDeviceInfos() []DeviceInfo {
	allRecords := h.creds.ListAllDeviceRecords()

	h.mu.RLock()
	connsCopy := make(map[string]*Conn, len(h.conns))
	for k, v := range h.conns {
		connsCopy[k] = v
	}
	h.mu.RUnlock()

	// Merge: start from all registered devices, then overlay connection status
	seen := make(map[string]bool)
	var out []DeviceInfo
	for id, rec := range allRecords {
		seen[id] = true
		info := DeviceInfo{
			DeviceID:     id,
			DeviceName:   rec.DeviceName,
			DeviceIP:     rec.DeviceIP,
			DeviceMAC:    rec.DeviceMAC,
			RegisteredAt: rec.RegisteredAt,
		}
		if conn, ok := connsCopy[id]; ok {
			info.Connected = true
			info.ConnectedIP = conn.ConnectedIP
			info.ConnectedAt = conn.ConnectedAt.Format(time.RFC3339)
			ver, sha, proto, drift, hello := conn.edgeSnapshot()
			info.EdgeVersion = ver
			info.EdgeGitSHA = sha
			info.EdgeProtocol = proto
			info.DriftStatus = drift
			info.HubExpectedEdgeV = expectedEdgeVersion()
			if !hello.IsZero() {
				info.EdgeHelloAt = hello.Format(time.RFC3339)
			}
		}
		out = append(out, info)
	}
	// Include any connected devices that somehow don't have a persisted record
	for id, conn := range connsCopy {
		if !seen[id] {
			di := DeviceInfo{
				DeviceID:    id,
				Connected:   true,
				ConnectedIP: conn.ConnectedIP,
				ConnectedAt: conn.ConnectedAt.Format(time.RFC3339),
			}
			ver, sha, proto, drift, hello := conn.edgeSnapshot()
			di.EdgeVersion = ver
			di.EdgeGitSHA = sha
			di.EdgeProtocol = proto
			di.DriftStatus = drift
			di.HubExpectedEdgeV = expectedEdgeVersion()
			if !hello.IsZero() {
				di.EdgeHelloAt = hello.Format(time.RFC3339)
			}
			out = append(out, di)
		}
	}
	return out
}

// ForwardRequestOpts carries optional forward metadata (correlation id for logs).
type ForwardRequestOpts struct {
	CorrelationID string
}

func forwardWarn(opts ForwardRequestOpts, deviceID string) *zerolog.Event {
	ev := log.Warn()
	if opts.CorrelationID != "" {
		ev = ev.Str("correlation_id", opts.CorrelationID)
	}
	if deviceID != "" {
		ev = ev.Str("device_id", deviceID)
	}
	return ev
}

// ForwardRequest sends a JSON-RPC request to the device and waits for the response.
// Returns the response body or error. Timeout after timeoutDur.
func (h *Hub) ForwardRequest(deviceID string, payload []byte, timeoutDur time.Duration) ([]byte, bool) {
	return h.ForwardRequestWithOpts(deviceID, payload, timeoutDur, ForwardRequestOpts{})
}

// ForwardRequestWithOpts is like ForwardRequest but attaches correlation_id to hub logs when set.
// When the device outbound buffer is briefly full, retries up to HUB_FORWARD_SEND_RETRIES with
// HUB_FORWARD_SEND_RETRY_INTERVAL_MS between attempts (not used for offline devices or JSON-RPC timeouts).
func (h *Hub) ForwardRequestWithOpts(deviceID string, payload []byte, timeoutDur time.Duration, opts ForwardRequestOpts) ([]byte, bool) {
	var req struct {
		ID interface{} `json:"id"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		forwardWarn(opts, deviceID).Err(err).Msg("forward: invalid JSON-RPC payload")
		return nil, false
	}
	rpcID := idToString(req.ID)
	if rpcID == "" {
		rpcID = "0"
	}

	key := deviceID + "|" + rpcID
	ch := make(chan []byte, 1)

	h.pendingMu.Lock()
	if _, exists := h.pending[key]; exists {
		h.pendingMu.Unlock()
		forwardWarn(opts, deviceID).Str("rpc_id", rpcID).Msg("forward: duplicate in-flight request id for device")
		return nil, false
	}
	pr := &pendingReq{
		ch: ch,
		timer: time.AfterFunc(timeoutDur, func() {
			h.pendingMu.Lock()
			if p, ok := h.pending[key]; ok {
				delete(h.pending, key)
				p.timer.Stop()
				close(p.ch)
			}
			h.pendingMu.Unlock()
		}),
	}
	h.pending[key] = pr
	h.pendingMu.Unlock()

	h.mu.RLock()
	c := h.conns[deviceID]
	h.mu.RUnlock()

	if c == nil {
		h.pendingMu.Lock()
		if p, ok := h.pending[key]; ok {
			delete(h.pending, key)
			p.timer.Stop()
			close(p.ch)
		}
		h.pendingMu.Unlock()
		forwardWarn(opts, deviceID).Msg("forward: device not connected")
		return nil, false
	}

	if !h.forwardTrySend(c, payload, opts) {
		h.pendingMu.Lock()
		if p, ok := h.pending[key]; ok {
			delete(h.pending, key)
			p.timer.Stop()
			close(p.ch)
		}
		h.pendingMu.Unlock()
		forwardWarn(opts, deviceID).Msg("forward: device send buffer full after retries")
		return nil, false
	}

	resp, ok := <-ch
	if !ok {
		forwardWarn(opts, deviceID).Msg("forward: response channel closed (timeout or disconnect)")
	}
	return resp, ok
}

func (h *Hub) forwardTrySend(c *Conn, payload []byte, opts ForwardRequestOpts) bool {
	maxAttempts := forwardSendMaxAttempts()
	delay := forwardSendRetryInterval()
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		select {
		case c.Send <- payload:
			if attempt > 1 && opts.CorrelationID != "" {
				log.Debug().Str("correlation_id", opts.CorrelationID).Str("device_id", c.DeviceID).Int("attempt", attempt).Msg("forward: send succeeded after retry")
			}
			return true
		default:
			if attempt == maxAttempts {
				return false
			}
			if delay > 0 {
				time.Sleep(delay)
			}
		}
	}
	return false
}

// DeliverResponse is called when a JSON-RPC response is received from a device
func (h *Hub) DeliverResponse(deviceID string, payload []byte) {
	var resp struct {
		ID interface{} `json:"id"`
	}
	if err := json.Unmarshal(payload, &resp); err != nil {
		return
	}
	rpcID := idToString(resp.ID)
	key := deviceID + "|" + rpcID

	h.pendingMu.Lock()
	pr, ok := h.pending[key]
	if ok {
		delete(h.pending, key)
		pr.timer.Stop()
		select {
		case pr.ch <- payload:
		default:
		}
		close(pr.ch)
	}
	h.pendingMu.Unlock()
}

func idToString(v interface{}) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
