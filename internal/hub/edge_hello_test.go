package hub

import (
	"encoding/json"
	"os"
	"testing"
)

func TestIsMCPJSONRPCResponse(t *testing.T) {
	yes := []string{
		`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`,
		`{"jsonrpc":"2.0","id":2,"error":{"code":-1,"message":"x"}}`,
	}
	for _, s := range yes {
		if !isMCPJSONRPCResponse([]byte(s)) {
			t.Fatalf("expected response: %s", s)
		}
	}
	no := []string{
		`{"jsonrpc":"2.0","method":"tools/list","id":1,"params":{}}`,
		`{"type":"register","device_id":"x"}`,
		`{"datumbridge":{"v":1,"edge":{"version":"1.0.0","protocol":1}}}`,
	}
	for _, s := range no {
		if isMCPJSONRPCResponse([]byte(s)) {
			t.Fatalf("expected non-response: %s", s)
		}
	}
}

func TestComputeDriftStatus(t *testing.T) {
	t.Setenv("HUB_EXPECTED_EDGE_VERSION", "")
	if got := computeDriftStatus("1.0.0"); got != "unknown" {
		t.Fatalf("got %q", got)
	}
	t.Setenv("HUB_EXPECTED_EDGE_VERSION", "1.2.0")
	if got := computeDriftStatus(""); got != "warn" {
		t.Fatalf("empty edge version: got %q", got)
	}
	if got := computeDriftStatus("1.2.0"); got != "ok" {
		t.Fatalf("match: got %q", got)
	}
	if got := computeDriftStatus("1.1.9"); got != "warn" {
		t.Fatalf("mismatch: got %q", got)
	}
}

func TestListDeviceInfosIncludesEdgeMeta(t *testing.T) {
	_ = os.Setenv("HUB_EXPECTED_EDGE_VERSION", "2.0.0")
	defer os.Unsetenv("HUB_EXPECTED_EDGE_VERSION")

	h := newTestHub()
	send := make(chan []byte, 4)
	h.Register("dev-x", send, "127.0.0.1")

	hello := map[string]interface{}{
		"datumbridge": map[string]interface{}{
			"v": 1,
			"edge": map[string]interface{}{
				"protocol":    1,
				"version":     "2.0.0",
				"git_sha":     "abc123",
				"server_name": "dtbclaw",
			},
		},
	}
	b, _ := json.Marshal(hello)
	h.tryConsumeEdgeHello("dev-x", b)

	infos := h.ListDeviceInfos()
	var found *DeviceInfo
	for i := range infos {
		if infos[i].DeviceID == "dev-x" {
			found = &infos[i]
			break
		}
	}
	if found == nil {
		t.Fatal("device dev-x not in list")
	}
	if found.EdgeVersion != "2.0.0" || found.EdgeGitSHA != "abc123" {
		t.Fatalf("edge meta: %+v", found)
	}
	if found.DriftStatus != "ok" {
		t.Fatalf("drift: %q", found.DriftStatus)
	}
	if found.HubExpectedEdgeV != "2.0.0" {
		t.Fatalf("expected hub expected: %q", found.HubExpectedEdgeV)
	}
}

func TestListDeviceInfosIncludesMissionCaps(t *testing.T) {
	h := newTestHub()
	send := make(chan []byte, 4)
	h.Register("dev-m", send, "127.0.0.1")

	hello := map[string]interface{}{
		"datumbridge": map[string]interface{}{
			"v": 1,
			"edge": map[string]interface{}{
				"protocol":              1,
				"version":               "1.0.0",
				"git_sha":               "deadbeef",
				"local_llm_available":   true,
				"supports_edge_mission": true,
				"mission_tool_name":     "edge_agent_run",
				"edge_mission_protocol": 1,
			},
		},
	}
	b, _ := json.Marshal(hello)
	h.tryConsumeEdgeHello("dev-m", b)

	infos := h.ListDeviceInfos()
	var found *DeviceInfo
	for i := range infos {
		if infos[i].DeviceID == "dev-m" {
			found = &infos[i]
			break
		}
	}
	if found == nil {
		t.Fatal("device dev-m not in list")
	}
	if !found.LocalLLMAvailable || !found.SupportsEdgeMission {
		t.Fatalf("caps: local_llm=%v mission=%v", found.LocalLLMAvailable, found.SupportsEdgeMission)
	}
	if found.MissionToolName != "edge_agent_run" || found.EdgeMissionProtocol != 1 {
		t.Fatalf("mission meta: tool=%q proto=%d", found.MissionToolName, found.EdgeMissionProtocol)
	}
}
