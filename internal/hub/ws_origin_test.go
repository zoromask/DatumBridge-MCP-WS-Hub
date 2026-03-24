package hub

import (
	"net/http"
	"testing"
)

func TestValidateWebSocketOrigin_UnsetEnv_SameHost(t *testing.T) {
	t.Setenv("HUB_ALLOWED_ORIGINS", "")
	r := &http.Request{
		Header: http.Header{},
		Host:   "localhost:8082",
	}
	r.Header.Set("Origin", "http://localhost:8082")
	if !ValidateWebSocketOrigin(r) {
		t.Fatal("expected same-origin Origin to be allowed")
	}
}

func TestValidateWebSocketOrigin_UnsetEnv_NoOrigin(t *testing.T) {
	t.Setenv("HUB_ALLOWED_ORIGINS", "")
	r := &http.Request{Header: http.Header{}, Host: "localhost:8082"}
	if !ValidateWebSocketOrigin(r) {
		t.Fatal("expected missing Origin to be allowed (native clients)")
	}
}

func TestValidateWebSocketOrigin_UnsetEnv_CrossOriginRejected(t *testing.T) {
	t.Setenv("HUB_ALLOWED_ORIGINS", "")
	r := &http.Request{
		Header: http.Header{},
		Host:   "hub.example.com",
	}
	r.Header.Set("Origin", "https://evil.example")
	if ValidateWebSocketOrigin(r) {
		t.Fatal("expected cross-origin to be rejected when allowlist unset")
	}
}

func TestValidateWebSocketOrigin_ExplicitList(t *testing.T) {
	t.Setenv("HUB_ALLOWED_ORIGINS", "https://app.example.com, https://admin.example.com")

	r := &http.Request{Header: http.Header{}, Host: "ws.example.com"}
	r.Header.Set("Origin", "https://app.example.com")
	if !ValidateWebSocketOrigin(r) {
		t.Fatal("expected listed origin to be allowed")
	}

	r2 := &http.Request{Header: http.Header{}, Host: "ws.example.com"}
	r2.Header.Set("Origin", "https://evil.example")
	if ValidateWebSocketOrigin(r2) {
		t.Fatal("expected non-listed origin to be rejected")
	}
}
