package hub

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOptionalStudioProxyStripMiddleware(t *testing.T) {
	cases := []struct {
		name     string
		inPath   string
		wantPath string
	}{
		{"strip register", "/api/ws-hub/api/v1/devices/register", "/api/v1/devices/register"},
		{"strip ws", "/api/ws-hub/ws", "/ws"},
		{"strip mcp", "/api/ws-hub/mcp", "/mcp"},
		{"strip base to root", "/api/ws-hub", "/"},
		{"no strip bare api", "/api/v1/devices/register", "/api/v1/devices/register"},
		{"no strip unrelated", "/health", "/health"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			h := OptionalStudioProxyStripMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				got = r.URL.Path
			}))
			req := httptest.NewRequest(http.MethodPost, tc.inPath, nil)
			h.ServeHTTP(httptest.NewRecorder(), req)
			if got != tc.wantPath {
				t.Fatalf("path: got %q want %q", got, tc.wantPath)
			}
		})
	}

	t.Run("disabled by env", func(t *testing.T) {
		t.Setenv("HUB_HTTP_STRIP_PREFIX", "0")
		var got string
		h := OptionalStudioProxyStripMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			got = r.URL.Path
		}))
		req := httptest.NewRequest(http.MethodPost, "/api/ws-hub/api/v1/foo", nil)
		h.ServeHTTP(httptest.NewRecorder(), req)
		if got != "/api/ws-hub/api/v1/foo" {
			t.Fatalf("expected no strip, got %q", got)
		}
	})
}
