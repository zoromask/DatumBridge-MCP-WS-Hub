package hub

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMCPStreamableHTTP_InitializeAndToolsList(t *testing.T) {
	h := New()
	srv := httptest.NewServer(http.HandlerFunc(h.HandleMCPStreamableHTTP))
	t.Cleanup(srv.Close)

	// initialize
	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader([]byte(initBody)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("init status %d: %s", resp.StatusCode, b)
	}
	sid := resp.Header.Get(mcpSessionHeader)
	if sid == "" {
		t.Fatal("missing Mcp-Session-Id")
	}
	var initResp struct {
		JSONRPC string `json:"jsonrpc"`
		Result  struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&initResp); err != nil {
		t.Fatal(err)
	}
	if initResp.Result.ProtocolVersion != mcpProtocolVersion {
		t.Fatalf("protocol: got %q", initResp.Result.ProtocolVersion)
	}

	// tools/list
	listBody := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	req2, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader([]byte(listBody)))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Accept", "application/json, text/event-stream")
	req2.Header.Set(mcpSessionHeader, sid)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp2.Body)
		t.Fatalf("list status %d: %s", resp2.StatusCode, b)
	}
	var listResp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&listResp); err != nil {
		t.Fatal(err)
	}
	if listResp.Error != nil {
		t.Fatalf("rpc error: %s", listResp.Error.Message)
	}
	if len(listResp.Result.Tools) < 2 {
		t.Fatalf("expected at least 2 tools, got %d", len(listResp.Result.Tools))
	}
}

func TestMCPStreamableHTTP_ToolsListRejectsMissingSession(t *testing.T) {
	h := New()
	srv := httptest.NewServer(http.HandlerFunc(h.HandleMCPStreamableHTTP))
	t.Cleanup(srv.Close)

	listBody := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader([]byte(listBody)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var listResp struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&listResp)
	if listResp.Error == nil || listResp.Error.Message == "" {
		t.Fatal("expected JSON-RPC error for missing session")
	}
}
