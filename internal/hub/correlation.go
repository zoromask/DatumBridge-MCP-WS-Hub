package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
)

// HeaderCorrelationID is the canonical response header echoing the request trace id.
const HeaderCorrelationID = "X-Correlation-ID"

type correlationIDContextKey struct{}

// WithCorrelationID attaches a correlation id to the request context (use after the id is chosen once per inbound HTTP request).
func WithCorrelationID(r *http.Request, id string) *http.Request {
	if r == nil {
		return nil
	}
	return r.WithContext(context.WithValue(r.Context(), correlationIDContextKey{}, id))
}

// CorrelationIDFromContext returns the id from WithCorrelationID, or falls back to CorrelationIDFromRequest(r).
func CorrelationIDFromContext(r *http.Request) string {
	if r == nil {
		return newCorrelationID()
	}
	if v, ok := r.Context().Value(correlationIDContextKey{}).(string); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return CorrelationIDFromRequest(r)
}

// CorrelationIDFromRequest returns an incoming correlation id or generates one.
// Honours X-Correlation-ID, X-Request-ID, then W3C Traceparent (trace-id segment only).
func CorrelationIDFromRequest(r *http.Request) string {
	if r == nil {
		return newCorrelationID()
	}
	for _, key := range []string{HeaderCorrelationID, "X-Request-ID"} {
		v := strings.TrimSpace(r.Header.Get(key))
		if v != "" {
			return sanitizeCorrelationID(v)
		}
	}
	if tp := strings.TrimSpace(r.Header.Get("Traceparent")); tp != "" {
		if tid := traceIDFromTraceparent(tp); tid != "" {
			return tid
		}
	}
	return newCorrelationID()
}

func traceIDFromTraceparent(s string) string {
	// traceparent: 00-{trace-id}-{parent-id}-{flags}  (trace-id is 32 hex chars)
	parts := strings.Split(s, "-")
	if len(parts) < 2 {
		return ""
	}
	tid := strings.TrimSpace(parts[1])
	if len(tid) != 32 {
		return ""
	}
	for _, c := range tid {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return ""
		}
	}
	return strings.ToLower(tid)
}

func sanitizeCorrelationID(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 256 {
		s = s[:256]
	}
	return s
}

func newCorrelationID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(b[:])
}
