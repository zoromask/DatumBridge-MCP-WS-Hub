package hub

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

// Hijack delegates to the underlying ResponseWriter so WebSocket upgrade works.
func (sr *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := sr.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
}

// Flush delegates to the underlying ResponseWriter for streaming/SSE support.
func (sr *statusRecorder) Flush() {
	if f, ok := sr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// studioProxyPathPrefix is the path DatumBridge Studio uses when proxying to this hub
// (see datumbridge-studio nginx.conf / vite proxy). Some ingress setups forward the full
// client path without stripping the prefix; without this middleware those requests hit the
// embedded static FileServer, which returns 405 for POST (e.g. device register).
const studioProxyPathPrefix = "/api/ws-hub"

// OptionalStudioProxyStripMiddleware removes leading /api/ws-hub from the request path when
// present so routing matches handlers registered at /api/v1/..., /ws, /mcp, etc.
// Disable by setting HUB_HTTP_STRIP_PREFIX=0 (or "false").
func OptionalStudioProxyStripMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v := strings.TrimSpace(strings.ToLower(os.Getenv("HUB_HTTP_STRIP_PREFIX"))); v == "0" || v == "false" || v == "no" {
			next.ServeHTTP(w, r)
			return
		}
		p := r.URL.Path
		if p != studioProxyPathPrefix && !strings.HasPrefix(p, studioProxyPathPrefix+"/") {
			next.ServeHTTP(w, r)
			return
		}
		newPath := strings.TrimPrefix(p, studioProxyPathPrefix)
		if newPath == "" {
			newPath = "/"
		}
		r2 := r.Clone(r.Context())
		u := *r.URL
		u.Path = newPath
		u.RawPath = ""
		r2.URL = &u
		next.ServeHTTP(w, r2)
	})
}

// LoggingMiddleware logs method, path, status, and duration for every request.
func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Debug().
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Int("status", rec.status).
			Dur("duration", time.Since(start)).
			Str("remote", r.RemoteAddr).
			Msg("request")
	})
}

// CORSMiddleware adds CORS headers based on HUB_ALLOWED_ORIGINS.
// If not set, allows all origins (development mode).
func CORSMiddleware(next http.Handler) http.Handler {
	origins := os.Getenv("HUB_ALLOWED_ORIGINS")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			if origins == "" || origins == "*" {
				w.Header().Set("Access-Control-Allow-Origin", origin)
			} else {
				for _, o := range strings.Split(origins, ",") {
					if strings.TrimSpace(o) == origin {
						w.Header().Set("Access-Control-Allow-Origin", origin)
						break
					}
				}
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, X-API-Key, Authorization, Mcp-Session-Id")
			w.Header().Set("Access-Control-Max-Age", "86400")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
