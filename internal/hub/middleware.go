package hub

import (
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
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key, Authorization")
			w.Header().Set("Access-Control-Max-Age", "86400")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
