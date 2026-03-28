package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestEdgeCatalogRegistryToolBodies_nonEmpty(t *testing.T) {
	bodies, err := EdgeCatalogRegistryToolBodies("my_mcp_server")
	if err != nil {
		t.Fatalf("EdgeCatalogRegistryToolBodies: %v", err)
	}
	if len(bodies) < 10 {
		t.Fatalf("expected many catalog tools, got %d", len(bodies))
	}
	var saw bool
	for _, b := range bodies {
		if b["mcpServer"] != "my_mcp_server" {
			t.Fatalf("mcpServer not overridden: %v", b["mcpServer"])
		}
		ed, _ := b["edgeDevice"].(map[string]interface{})
		if ed["mcpToolName"] == "apply_patch" {
			saw = true
		}
	}
	if !saw {
		t.Fatal("expected apply_patch edge tool in catalog bodies")
	}
}

func TestSyncEdgeCatalogToRegistry(t *testing.T) {
	var posts int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/tools" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt32(&posts, 1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer ts.Close()

	base := strings.TrimSuffix(ts.URL, "/") + "/api/v1"
	n, err := SyncEdgeCatalogToRegistry(context.Background(), base, "", "test_server")
	if err != nil {
		t.Fatalf("SyncEdgeCatalogToRegistry: %v", err)
	}
	if int(n) != int(atomic.LoadInt32(&posts)) || n == 0 {
		t.Fatalf("synced=%d posts=%d", n, posts)
	}
}
