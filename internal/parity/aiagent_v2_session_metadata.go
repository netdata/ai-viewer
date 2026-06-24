package parity

import (
	"encoding/json"
	"fmt"
	"net/url"
)

type aiAgentV2SessionMetadataIdentity struct {
	NativeSessionID   string `json:"native_session_id"`
	OriginID          string `json:"origin_id"`
	Version           int    `json:"version,omitempty"`
	NodeID            string `json:"node_id,omitempty"`
	AgentID           string `json:"agent_id,omitempty"`
	CallPath          string `json:"call_path,omitempty"`
	SessionTitle      string `json:"session_title,omitempty"`
	LatestStatus      string `json:"latest_status,omitempty"`
	AttributesSHA256  string `json:"attributes_sha256,omitempty"`
	TotalsSHA256      string `json:"totals_sha256,omitempty"`
	PluginMetasSHA256 string `json:"plugin_metas_sha256,omitempty"`
}

func (s *aiAgentV2SourceState) recordSessionMetadata(node aiAgentV2OpTree, visit aiAgentV2SessionVisit) error {
	identity, ok, err := s.aiAgentV2SessionMetadataIdentity(node, visit)
	if err != nil || !ok {
		return err
	}
	nativeID := node.TraceID
	artifact, err := identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          aiAgentV2Format,
		sourceFile:       s.sourceFile,
		nativeSessionID:  nativeID,
		nativeArtifactID: "session:" + nativeID + ":metadata",
		class:            ClassSessionMetadata,
		selectorURI:      aiAgentV2SelectorURI("sessions", nativeID, "session:"+nativeID) + "#metadata",
		identity:         identity,
	})
	if err != nil {
		return err
	}
	s.artifacts = append(s.artifacts, artifact)
	return nil
}

func (s *aiAgentV2SourceState) aiAgentV2SessionMetadataIdentity(node aiAgentV2OpTree, visit aiAgentV2SessionVisit) (aiAgentV2SessionMetadataIdentity, bool, error) {
	attributesHash, err := aiAgentV2JSONMapHash(node.Attributes)
	if err != nil {
		return aiAgentV2SessionMetadataIdentity{}, false, fmt.Errorf("hash aiagent_v2 session attributes: %w", err)
	}
	totalsHash, err := aiAgentV2JSONHash(node.Totals)
	if err != nil {
		return aiAgentV2SessionMetadataIdentity{}, false, fmt.Errorf("hash aiagent_v2 session totals: %w", err)
	}
	pluginMetasHash, err := aiAgentV2JSONHash(node.PluginMetas)
	if err != nil {
		return aiAgentV2SessionMetadataIdentity{}, false, fmt.Errorf("hash aiagent_v2 pluginMetas: %w", err)
	}
	version := 0
	if visit.parentNativeID == "" {
		version = s.version
	}
	identity := aiAgentV2SessionMetadataIdentity{
		NativeSessionID:   node.TraceID,
		OriginID:          s.originID,
		Version:           version,
		NodeID:            node.ID,
		AgentID:           node.AgentID,
		CallPath:          node.CallPath,
		SessionTitle:      node.SessionTitle,
		LatestStatus:      node.LatestStatus,
		AttributesSHA256:  attributesHash,
		TotalsSHA256:      totalsHash,
		PluginMetasSHA256: pluginMetasHash,
	}
	if !identity.hasDescriptiveAIAgentV2Metadata() {
		return aiAgentV2SessionMetadataIdentity{}, false, nil
	}
	return identity, true, nil
}

func artifactFromAIAgentV2SessionMetadata(row canonicalSessionRow) (Artifact, bool, error) {
	identity, ok, err := aiAgentV2SessionMetadataFromCanonical(row)
	if err != nil || !ok {
		return Artifact{}, ok, err
	}
	artifact, err := identityArtifact(identityArtifactInput{
		sourceID:           row.sourceID,
		adapter:            row.adapter,
		sourceFile:         row.sourceLocation,
		canonicalSessionID: row.sessionID,
		nativeSessionID:    row.nativeSessionID,
		nativeArtifactID:   "session:" + row.nativeSessionID + ":metadata",
		class:              ClassSessionMetadata,
		selectorURI:        "canonical://sessions/" + url.PathEscape(row.sessionID) + "#metadata",
		identity:           identity,
	})
	return artifact, true, err
}

func aiAgentV2SessionMetadataFromCanonical(row canonicalSessionRow) (aiAgentV2SessionMetadataIdentity, bool, error) {
	fields, err := jsonObjectFromNullString(row.extrasJSON, "aiagent_v2 session extras")
	if err != nil {
		return aiAgentV2SessionMetadataIdentity{}, false, err
	}
	version64, err := int64JSONField(fields, "version")
	if err != nil {
		return aiAgentV2SessionMetadataIdentity{}, false, err
	}
	originID, err := stringJSONField(fields, "originId")
	if err != nil {
		return aiAgentV2SessionMetadataIdentity{}, false, err
	}
	nodeID, err := stringJSONField(fields, "nodeId")
	if err != nil {
		return aiAgentV2SessionMetadataIdentity{}, false, err
	}
	sessionTitle, err := stringJSONField(fields, "sessionTitle")
	if err != nil {
		return aiAgentV2SessionMetadataIdentity{}, false, err
	}
	latestStatus, err := stringJSONField(fields, "latestStatus")
	if err != nil {
		return aiAgentV2SessionMetadataIdentity{}, false, err
	}
	attributesHash, err := aiAgentV2JSONHash(fields["attributes"])
	if err != nil {
		return aiAgentV2SessionMetadataIdentity{}, false, fmt.Errorf("hash aiagent_v2 session attributes: %w", err)
	}
	totalsHash, err := aiAgentV2JSONHash(fields["totals"])
	if err != nil {
		return aiAgentV2SessionMetadataIdentity{}, false, fmt.Errorf("hash aiagent_v2 session totals: %w", err)
	}
	pluginMetasHash, err := aiAgentV2JSONHash(fields["plugin_metas"])
	if err != nil {
		return aiAgentV2SessionMetadataIdentity{}, false, fmt.Errorf("hash aiagent_v2 plugin_metas: %w", err)
	}
	identity := aiAgentV2SessionMetadataIdentity{
		NativeSessionID:   row.nativeSessionID,
		OriginID:          originID,
		Version:           int(version64),
		NodeID:            nodeID,
		AgentID:           nullString(row.agentName),
		CallPath:          nullString(row.callPath),
		SessionTitle:      sessionTitle,
		LatestStatus:      latestStatus,
		AttributesSHA256:  attributesHash,
		TotalsSHA256:      totalsHash,
		PluginMetasSHA256: pluginMetasHash,
	}
	if !identity.hasDescriptiveAIAgentV2Metadata() {
		return aiAgentV2SessionMetadataIdentity{}, false, nil
	}
	return identity, true, nil
}

func (i aiAgentV2SessionMetadataIdentity) hasDescriptiveAIAgentV2Metadata() bool {
	return i.OriginID != "" ||
		i.Version != 0 ||
		i.NodeID != "" ||
		i.AgentID != "" ||
		i.CallPath != "" ||
		i.SessionTitle != "" ||
		i.LatestStatus != "" ||
		i.AttributesSHA256 != "" ||
		i.TotalsSHA256 != "" ||
		i.PluginMetasSHA256 != ""
}

func aiAgentV2JSONMapHash(fields map[string]json.RawMessage) (string, error) {
	if len(fields) == 0 {
		return "", nil
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	return aiAgentV2JSONHash(raw)
}

func aiAgentV2JSONHash(raw json.RawMessage) (string, error) {
	if jsonRawEmptyObjectOrNull(raw) {
		return "", nil
	}
	return canonicalJSONHash(raw)
}
