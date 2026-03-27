package hub

import "testing"

func TestDtbClawCatalog_shellIsRelayTool(t *testing.T) {
	if !isEdgeRelayTool("shell") {
		t.Fatal("expected shell to be an edge relay tool")
	}
	if isEdgeRelayTool("not_a_real_tool_xyz") {
		t.Fatal("unexpected relay tool")
	}
}
