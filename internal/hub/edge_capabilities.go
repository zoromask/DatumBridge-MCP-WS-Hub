package hub

import "strings"

// EdgeCapabilityFields is advertised by the edge in datumbridge edge_hello (Approach 2 / 3 foundations).
// Omitted fields in JSON imply false / empty — fail closed for autonomous edge missions.
type EdgeCapabilityFields struct {
	LocalLLMAvailable   bool
	SupportsEdgeMission bool
	MissionToolName     string
	EdgeMissionProtocol int
}

type edgeCapsBlock struct {
	LocalLLMAvailable   *bool  `json:"local_llm_available,omitempty"`
	SupportsEdgeMission *bool  `json:"supports_edge_mission,omitempty"`
	MissionToolName     string `json:"mission_tool_name,omitempty"`
	EdgeMissionProtocol int    `json:"edge_mission_protocol,omitempty"`
}

// mergeEdgeCaps applies nested capabilities first, then flat fields on the parent edge object (flat wins when set).
func mergeEdgeCaps(block *edgeCapsBlock, flatLocal, flatMission *bool, flatTool string, flatProto int) EdgeCapabilityFields {
	var out EdgeCapabilityFields
	if block != nil {
		if block.LocalLLMAvailable != nil {
			out.LocalLLMAvailable = *block.LocalLLMAvailable
		}
		if block.SupportsEdgeMission != nil {
			out.SupportsEdgeMission = *block.SupportsEdgeMission
		}
		if t := strings.TrimSpace(block.MissionToolName); t != "" {
			out.MissionToolName = t
		}
		if block.EdgeMissionProtocol != 0 {
			out.EdgeMissionProtocol = block.EdgeMissionProtocol
		}
	}
	if flatLocal != nil {
		out.LocalLLMAvailable = *flatLocal
	}
	if flatMission != nil {
		out.SupportsEdgeMission = *flatMission
	}
	if t := strings.TrimSpace(flatTool); t != "" {
		out.MissionToolName = t
	}
	if flatProto != 0 {
		out.EdgeMissionProtocol = flatProto
	}
	return out
}
