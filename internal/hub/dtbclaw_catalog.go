package hub

import (
	_ "embed"
	"encoding/json"
	"sort"
	"sync"
)

//go:embed dtbclaw_edge_catalog.json
var dtbclawEdgeCatalogRaw []byte

type dtbClawEdgeManifest struct {
	Profile     string            `json:"profile"`
	Description string            `json:"description"`
	Tools       []dtbClawEdgeTool `json:"tools"`
}

type dtbClawEdgeTool struct {
	RegistryID  string                 `json:"registryId"`
	Name        string                 `json:"name"`
	McpToolName string                 `json:"mcpToolName"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

var (
	catalogOnce    sync.Once
	catalogLoaded  *dtbClawEdgeManifest
	catalogLoadErr error
	relayNameSet   map[string]struct{}
)

func dtbClawCatalog() (*dtbClawEdgeManifest, error) {
	catalogOnce.Do(func() {
		var m dtbClawEdgeManifest
		if err := json.Unmarshal(dtbclawEdgeCatalogRaw, &m); err != nil {
			catalogLoadErr = err
			return
		}
		catalogLoaded = &m
		relayNameSet = make(map[string]struct{}, len(m.Tools))
		for _, t := range m.Tools {
			if t.McpToolName != "" {
				relayNameSet[t.McpToolName] = struct{}{}
			}
		}
	})
	return catalogLoaded, catalogLoadErr
}

// isEdgeRelayTool reports whether name is a native DTBClaw tool forwarded to connected devices.
func isEdgeRelayTool(name string) bool {
	_, err := dtbClawCatalog()
	if err != nil || relayNameSet == nil {
		return false
	}
	_, ok := relayNameSet[name]
	return ok
}

func cloneMap(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return map[string]interface{}{}
	}
	b, err := json.Marshal(m)
	if err != nil {
		out := make(map[string]interface{}, len(m))
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	var out map[string]interface{}
	if err := json.Unmarshal(b, &out); err != nil {
		return map[string]interface{}{"type": "object"}
	}
	return out
}

// edgeRelayToolDescriptors returns MCP tools/list entries for DTBClaw edge tools.
// Each schema includes a required device_id for calls through the hub MCP endpoint.
func edgeRelayToolDescriptors() ([]map[string]interface{}, error) {
	m, err := dtbClawCatalog()
	if err != nil {
		return nil, err
	}
	out := make([]map[string]interface{}, 0, len(m.Tools))
	for _, t := range m.Tools {
		if t.McpToolName == "" {
			continue
		}
		schema := cloneMap(t.InputSchema)
		props, _ := schema["properties"].(map[string]interface{})
		if props == nil {
			props = map[string]interface{}{}
			schema["properties"] = props
		}
		props["device_id"] = map[string]interface{}{
			"type":        "string",
			"description": "Hub-registered device with an active WebSocket (same as workflow execution device_id).",
		}
		reqSet := map[string]struct{}{"device_id": {}}
		if r, ok := schema["required"].([]interface{}); ok {
			for _, x := range r {
				if s, ok := x.(string); ok && s != "device_id" && s != "deviceId" {
					reqSet[s] = struct{}{}
				}
			}
		}
		extra := make([]string, 0, len(reqSet))
		for s := range reqSet {
			if s != "device_id" {
				extra = append(extra, s)
			}
		}
		sort.Strings(extra)
		schema["required"] = append(append([]string{}, "device_id"), extra...)

		desc := t.Description
		if m.Profile != "" {
			desc += " (profile: " + m.Profile + ")"
		}

		entry := map[string]interface{}{
			"name":        t.McpToolName,
			"description": desc,
			"inputSchema": schema,
		}
		if len(t.Metadata) > 0 {
			entry["metadata"] = cloneMap(t.Metadata)
		}
		out = append(out, entry)
	}
	return out, nil
}
