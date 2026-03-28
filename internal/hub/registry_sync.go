package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// DefaultRegistryMcpServerID is the default Tool Registry mcpServer field for edge tools.
// Override with WS_HUB_REGISTRY_MCPSERVER_NAME when syncing.
const DefaultRegistryMcpServerID = "datumbridge_mcp_ws_hub"

// EdgeCatalogRegistryToolBodies builds payloads for datumbridge-mcp POST /api/v1/tools from the embedded catalog.
func EdgeCatalogRegistryToolBodies(mcpServerID string) ([]map[string]interface{}, error) {
	m, err := dtbClawCatalog()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(mcpServerID) == "" {
		mcpServerID = DefaultRegistryMcpServerID
	}
	profile := m.Profile
	if profile == "" {
		profile = "dtbclaw-default"
	}

	out := make([]map[string]interface{}, 0, len(m.Tools))
	for _, et := range m.Tools {
		if et.RegistryID == "" || et.McpToolName == "" {
			continue
		}
		inSchema := cloneMap(et.InputSchema)
		if len(inSchema) == 0 {
			inSchema = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
		}
		displayName := et.Name
		if displayName == "" {
			displayName = et.McpToolName
		}
		desc := et.Description
		if desc == "" {
			desc = "Edge device MCP tool (catalog). Set device on the workflow node."
		}
		desc = desc + " Profile: " + profile + "."

		out = append(out, map[string]interface{}{
			"id":          et.RegistryID,
			"name":        displayName,
			"description": desc,
			"version":     "1.0.0",
			"mcpServer":   mcpServerID,
			"status":      "active",
			"schema": map[string]interface{}{
				"inputSchema":  inSchema,
				"outputSchema": map[string]interface{}{"type": "object"},
			},
			"capabilities": []string{"edge_device", "mcp_ws_hub", profile},
			"tags":         []string{"edge-device", "manifest:" + profile, "ws-hub"},
			"docs":         m.Description,
			"edgeDevice": map[string]interface{}{
				"profile":     profile,
				"mcpToolName": et.McpToolName,
			},
			"permissions": map[string]interface{}{
				"allowedRoles":   []string{"user", "admin", "workflow_engine"},
				"requiredScopes": []string{},
				"minAuthLevel":   "none",
			},
			"cost": map[string]interface{}{
				"costPerCall":  0,
				"costCurrency": "USD",
			},
			"rateLimit": map[string]interface{}{
				"requestsPerMinute": 120,
				"requestsPerHour":   5000,
			},
			"createdBy": "system:datumbridge-mcp-ws-hub",
		})
	}
	return out, nil
}

// SyncEdgeCatalogToRegistry POSTs each catalog tool to the Tool Registry (upserts id+version).
// baseURL must include the /api/v1 prefix, e.g. http://datumbridge-mcp:8081/api/v1 .
// apiKey is optional; when set, sends Bearer and X-API-Key (same as MCP_SERVICE_API_KEY / MCP_API_KEY).
func SyncEdgeCatalogToRegistry(ctx context.Context, baseURL, apiKey, mcpServerID string) (synced int, err error) {
	bodies, err := EdgeCatalogRegistryToolBodies(mcpServerID)
	if err != nil {
		return 0, err
	}
	client := &http.Client{Timeout: 45 * time.Second}
	endpoint := strings.TrimSuffix(strings.TrimSpace(baseURL), "/") + "/tools"

	for _, body := range bodies {
		raw, mErr := json.Marshal(body)
		if mErr != nil {
			return synced, fmt.Errorf("marshal tool %v: %w", body["id"], mErr)
		}
		var lastErr error
		for attempt := 0; attempt < 5; attempt++ {
			if attempt > 0 {
				select {
				case <-ctx.Done():
					return synced, ctx.Err()
				case <-time.After(time.Duration(attempt) * 2 * time.Second):
				}
			}
			req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
			if reqErr != nil {
				return synced, reqErr
			}
			req.Header.Set("Content-Type", "application/json")
			if apiKey != "" {
				req.Header.Set("Authorization", "Bearer "+apiKey)
				req.Header.Set("X-API-Key", apiKey)
			}
			resp, doErr := client.Do(req)
			if doErr != nil {
				lastErr = doErr
				log.Warn().Err(doErr).Int("attempt", attempt+1).Interface("tool", body["id"]).Msg("registry sync request failed")
				continue
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK {
				synced++
				lastErr = nil
				break
			}
			lastErr = fmt.Errorf("POST %s: status %d", endpoint, resp.StatusCode)
			log.Warn().Int("status", resp.StatusCode).Int("attempt", attempt+1).Interface("tool", body["id"]).Msg("registry sync rejected")
		}
		if lastErr != nil {
			return synced, fmt.Errorf("tool %v: %w", body["id"], lastErr)
		}
	}
	return synced, nil
}
