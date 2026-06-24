package parity

import (
	"fmt"
	"net/url"
	"path/filepath"
)

func (s *aiAgentV2SourceState) recordOpLogs(op aiAgentV2Operation, scope aiAgentV2OpScope, opPointer string) {
	for i := range op.Logs {
		pointer := fmt.Sprintf("%s/logs/%d/message", opPointer, i)
		s.artifacts = append(s.artifacts, s.aiAgentV2LogArtifact(scope.sessionTrace, scope.turnSeq, pointer, op.Logs[i].Message))
	}
}

func (s *aiAgentV2SourceState) recordFailedOpLog(op aiAgentV2Operation, scope aiAgentV2OpScope, opPointer string) {
	if op.Status != "failed" {
		return
	}
	message := aiAgentV2AttrString(op.Attributes, "error")
	if message == "" {
		return
	}
	pointer := opPointer + "/attributes/error"
	s.artifacts = append(s.artifacts, s.aiAgentV2LogArtifact(scope.sessionTrace, scope.turnSeq, pointer, message))
}

func (s *aiAgentV2SourceState) recordSessionErrorLog(node aiAgentV2OpTree, sessionPointer string) {
	if node.Success == nil || *node.Success || node.Error == "" {
		return
	}
	s.artifacts = append(s.artifacts, s.aiAgentV2LogArtifact(node.TraceID, -1, sessionPointer+"/error", node.Error))
}

func (s *aiAgentV2SourceState) aiAgentV2LogArtifact(sessionTrace string, turnSeq int64, pointer string, message string) Artifact {
	nativeTurnID := ""
	if turnSeq >= 0 {
		nativeTurnID = aiAgentV2NativeTurnID(turnSeq)
	}
	return semanticTextArtifact(semanticTextArtifactInput{
		sourceID:         s.sourceID,
		adapter:          aiAgentV2Format,
		sourceFile:       s.sourceFile,
		nativeSessionID:  sessionTrace,
		nativeTurnID:     nativeTurnID,
		nativeArtifactID: aiAgentV2SnapshotFieldNativeID(s.sourceFile, pointer),
		class:            ClassLogEntry,
		selector: Selector{
			URI:         aiAgentV2SnapshotSelectorURI(s.sourceFile),
			JSONPointer: pointer,
		},
		text: message,
	})
}

func aiAgentV2SnapshotFieldNativeID(sourceFile string, pointer string) string {
	return fmt.Sprintf("file:%s:%s", filepath.Base(sourceFile), pointer)
}

func aiAgentV2SnapshotSelectorURI(sourceFile string) string {
	return (&url.URL{Scheme: "file", Path: sourceFile}).String()
}
