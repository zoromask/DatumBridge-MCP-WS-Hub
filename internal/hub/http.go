package hub

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/rs/zerolog/log"
)

const (
	requestTimeout = 5 * time.Minute // long enough for cursor agent --print
	maxBodySize    = 1 << 20         // 1 MB
)

// APIError is a standardized error response matching the DatumBridge MCP convention.
type APIError struct {
	ErrorCode    string `json:"error_code"`
	ErrorMessage string `json:"error_message"`
	Retryable    bool   `json:"retryable"`
}

func writeAPIError(w http.ResponseWriter, status int, code, message string, retryable bool) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(APIError{
		ErrorCode:    code,
		ErrorMessage: message,
		Retryable:    retryable,
	})
}

// jsonrpcError is used for proper JSON encoding to prevent XSS (avoids direct w.Write).
type jsonrpcError struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int   `json:"id"`
	Error   struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeJSONRPCError(w http.ResponseWriter, status int, rpcCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(jsonrpcError{
		JSONRPC: "2.0",
		ID:      nil,
		Error: struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}{Code: rpcCode, Message: message},
	})
}

// checkRegisterAPIKey validates HUB_REGISTER_API_KEY if configured.
// Returns true when access is allowed.
func checkRegisterAPIKey(r *http.Request) bool {
	apiKey := os.Getenv("HUB_REGISTER_API_KEY")
	if apiKey == "" {
		return true // not configured — open registration (dev mode)
	}
	if r.Header.Get("X-API-Key") == apiKey {
		return true
	}
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") && strings.TrimPrefix(auth, "Bearer ") == apiKey {
		return true
	}
	return false
}

// HandleDeviceMCP handles POST /api/v1/devices/{device_id}/mcp
// Forwards MCP JSON-RPC to the device over WebSocket and returns the response.
func (h *Hub) HandleDeviceMCP(w http.ResponseWriter, r *http.Request) {
	corr := CorrelationIDFromRequest(r)
	w.Header().Set(HeaderCorrelationID, corr)

	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", false)
		return
	}

	vars := mux.Vars(r)
	deviceID := vars["device_id"]
	if deviceID == "" {
		writeAPIError(w, http.StatusBadRequest, "VALIDATION_ERROR", "device_id required", false)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "PAYLOAD_TOO_LARGE", "request body too large (max 1 MB)", false)
		return
	}
	defer r.Body.Close()

	if len(body) == 0 {
		writeAPIError(w, http.StatusBadRequest, "VALIDATION_ERROR", "empty body", false)
		return
	}

	resp, ok := h.ForwardRequestWithOpts(deviceID, body, requestTimeout, ForwardRequestOpts{CorrelationID: corr})
	if !ok {
		writeJSONRPCError(w, http.StatusBadGateway, -32000, "device not connected or request timeout")
		return
	}

	// Validate JSON and re-emit via json.RawMessage (concrete type; avoids CWE-502 interface{} decode).
	var payload json.RawMessage
	if err := json.Unmarshal(resp, &payload); err != nil {
		writeJSONRPCError(w, http.StatusBadGateway, -32603, "invalid response from device")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(payload)
}

// HandleHealth returns health status
func HandleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok","service":"datumbridge-mcp-ws-hub"}`))
}

// HandleListDevices returns all registered devices with metadata and connection status.
func (h *Hub) HandleListDevices(w http.ResponseWriter, _ *http.Request) {
	infos := h.ListDeviceInfos()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"devices": infos})
}

// HandleRevokeDevice disconnects a device and removes its credential (DELETE /api/v1/devices/{device_id})
func (h *Hub) HandleRevokeDevice(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	deviceID := vars["device_id"]
	if deviceID == "" {
		writeAPIError(w, http.StatusBadRequest, "VALIDATION_ERROR", "device_id required", false)
		return
	}
	h.RevokeDevice(deviceID)
	log.Info().Str("device_id", deviceID).Msg("device revoked and disconnected")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "device_id": deviceID})
}

// HandleListPendingPairings returns pending pairing codes (for test UI)
func (h *Hub) HandleListPendingPairings(w http.ResponseWriter, _ *http.Request) {
	pending := h.pairingStore.List()
	ttl := PairingTTL()
	ttlSec := int(ttl / time.Second)
	if ttlSec < 1 {
		ttlSec = 1
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"pairings":            pending,
		"pairing_ttl_seconds": ttlSec,
	})
}

// RegisterDeviceRequest is the body for POST /api/v1/devices/register
type RegisterDeviceRequest struct {
	DeviceID   string `json:"device_id"`
	Pairing    bool   `json:"pairing"`
	DeviceName string `json:"device_name,omitempty"`
	DeviceIP   string `json:"device_ip,omitempty"`
	DeviceMAC  string `json:"device_mac,omitempty"`
}

// RegisterDeviceResponse is the response from POST /api/v1/devices/register
type RegisterDeviceResponse struct {
	DeviceID    string     `json:"device_id,omitempty"`
	Token       string     `json:"token,omitempty"`
	PairingCode string     `json:"pairing_code,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	DeviceName  string     `json:"device_name,omitempty"`
}

// ConfirmPairingRequest is the body for POST /api/v1/devices/register/confirm
type ConfirmPairingRequest struct {
	PairingCode string `json:"pairing_code"`
	DeviceName  string `json:"device_name,omitempty"`
	DeviceIP    string `json:"device_ip,omitempty"`
	DeviceMAC   string `json:"device_mac,omitempty"`
}

// HandleRegisterDevice creates a new device credential.
// If pairing=true, returns a 6-digit pairing code; client must call /register/confirm with it.
// Protected by HUB_REGISTER_API_KEY env (X-API-Key or Authorization: Bearer).
func (h *Hub) HandleRegisterDevice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", false)
		return
	}

	if !checkRegisterAPIKey(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid or missing API key", false)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	var req RegisterDeviceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		req = RegisterDeviceRequest{}
	}
	_ = r.Body.Close()

	if req.Pairing {
		code, cred, err := h.creds.StartPairing(req.DeviceID)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to generate pairing", true)
			return
		}
		expiresAt := time.Now().Add(PairingTTL())
		h.pairingStore.Add(code, cred, expiresAt)
		log.Info().Str("device_id", cred.DeviceID).Str("pairing_code", code).Time("expires_at", expiresAt).Msg("new pairing request — enter this code in octoclaw register")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		exp := expiresAt
		_ = json.NewEncoder(w).Encode(RegisterDeviceResponse{
			DeviceID:    cred.DeviceID,
			PairingCode: code,
			ExpiresAt:   &exp,
		})
		return
	}

	cred, err := h.creds.RegisterDevice(req.DeviceID, req.DeviceName, req.DeviceIP, req.DeviceMAC)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to generate credential", true)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(RegisterDeviceResponse{
		DeviceID:   cred.DeviceID,
		Token:      cred.Token,
		DeviceName: cred.DeviceName,
	})
}

// HandleConfirmPairing completes pairing by exchanging 6-digit code for device_id and token.
func (h *Hub) HandleConfirmPairing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", false)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	var req ConfirmPairingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.PairingCode == "" {
		writeAPIError(w, http.StatusBadRequest, "VALIDATION_ERROR", "pairing_code required", false)
		return
	}
	_ = r.Body.Close()

	cred, ok := h.pairingStore.Consume(req.PairingCode)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "INVALID_PAIRING", "invalid or expired pairing code", false)
		return
	}

	// Attach device metadata from the confirm request
	if req.DeviceName != "" {
		cred.DeviceName = req.DeviceName
	}
	if req.DeviceIP != "" {
		cred.DeviceIP = req.DeviceIP
	}
	if req.DeviceMAC != "" {
		cred.DeviceMAC = req.DeviceMAC
	}

	// Return plain token to device before hashing for storage
	resp := RegisterDeviceResponse{
		DeviceID:   cred.DeviceID,
		Token:      cred.Token,
		DeviceName: cred.DeviceName,
	}

	if err := h.creds.AddCredential(cred); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to persist credential", true)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
