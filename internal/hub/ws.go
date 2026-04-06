package hub

import (
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
)

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10 // 54s — must be less than pongWait
)

// checkSameOrigin validates Origin against Host to prevent CSRF.
// Mirrors gorilla/websocket default: allow if Origin is empty or matches request Host.
func checkSameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // No Origin (e.g. native clients, curl) — allow
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Host == r.Host
}

// ValidateWebSocketOrigin enforces trusted origins for the WebSocket handshake (CSRF mitigation).
// When HUB_ALLOWED_ORIGINS is unset: same-origin or missing Origin only.
// When set: request Origin must exactly match one listed value (no "*" wildcard).
func ValidateWebSocketOrigin(r *http.Request) bool {
	origins := parseAllowedOrigins()
	if len(origins) == 0 {
		return checkSameOrigin(r)
	}
	origin := r.Header.Get("Origin")
	for _, o := range origins {
		if o == origin {
			return true
		}
	}
	return false
}

func newUpgrader() websocket.Upgrader {
	return websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     ValidateWebSocketOrigin,
	}
}

// parseAllowedOrigins reads HUB_ALLOWED_ORIGINS (comma-separated).
// Empty or unset means WebSocket uses same-origin / missing-Origin only (see ValidateWebSocketOrigin).
func parseAllowedOrigins() []string {
	raw := os.Getenv("HUB_ALLOWED_ORIGINS")
	if raw == "" {
		return nil
	}
	var out []string
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// HandleWS handles WebSocket connections from DTBClaw devices.
// Requires device_id and token (server-generated credential) in query params.
func (h *Hub) HandleWS(w http.ResponseWriter, r *http.Request) {
	deviceID := r.URL.Query().Get("device_id")
	token := r.URL.Query().Get("token")
	if deviceID == "" {
		http.Error(w, "device_id query parameter required", http.StatusBadRequest)
		return
	}
	if token == "" {
		http.Error(w, "token query parameter required (get credentials from POST /api/v1/devices/register)", http.StatusUnauthorized)
		return
	}
	if !h.creds.ValidateToken(deviceID, token) {
		http.Error(w, "invalid or revoked device credential", http.StatusUnauthorized)
		return
	}

	if !ValidateWebSocketOrigin(r) {
		http.Error(w, "WebSocket origin not allowed", http.StatusForbidden)
		return
	}

	upgrader := newUpgrader()
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error().Err(err).Msg("websocket upgrade failed")
		return
	}
	defer conn.Close()

	remoteIP := r.RemoteAddr
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		remoteIP = strings.SplitN(fwd, ",", 2)[0]
	}

	send := make(chan []byte, 16)
	h.Register(deviceID, send, remoteIP)
	defer h.Unregister(deviceID)

	log.Info().Str("device_id", deviceID).Str("remote_ip", remoteIP).Msg("device connected")

	// Writer goroutine: forward messages from send channel + periodic pings
	go func() {
		ticker := time.NewTicker(pingPeriod)
		defer ticker.Stop()
		for {
			select {
			case msg, ok := <-send:
				if !ok {
					_ = conn.WriteMessage(websocket.CloseMessage, []byte{})
					return
				}
				_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
				if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
					log.Debug().Err(err).Str("device_id", deviceID).Msg("write to device failed")
					return
				}
			case <-ticker.C:
				_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					log.Debug().Err(err).Str("device_id", deviceID).Msg("ping to device failed")
					return
				}
			}
		}
	}()

	// Configure read deadline and pong handler for connection health
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	// Reader loop: all incoming messages are treated as JSON-RPC responses
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			log.Debug().Err(err).Str("device_id", deviceID).Msg("read from device closed")
			return
		}
		h.HandleDeviceWSInbound(deviceID, msg)
	}
}
