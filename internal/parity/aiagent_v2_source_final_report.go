package parity

import "bytes"

func (s *aiAgentV2SourceState) recordFinalReport(node aiAgentV2OpTree) error {
	if len(bytes.TrimSpace(node.FinalReport)) == 0 {
		return nil
	}
	nativeSessionArtifactID := "session:" + node.TraceID
	artifact, err := canonicalJSONArtifact(canonicalJSONArtifactInput{
		sourceID:         s.sourceID,
		adapter:          aiAgentV2Format,
		sourceFile:       s.sourceFile,
		nativeSessionID:  node.TraceID,
		nativeArtifactID: nativeSessionArtifactID + ":final_report",
		class:            ClassAssistantMessage,
		selector: Selector{
			URI:       aiAgentV2SelectorURI("sessions", node.TraceID, nativeSessionArtifactID),
			FieldPath: "finalReport",
		},
		raw:   node.FinalReport,
		label: "aiagent_v2 finalReport",
	})
	if err != nil {
		return err
	}
	s.artifacts = append(s.artifacts, artifact)
	return nil
}
