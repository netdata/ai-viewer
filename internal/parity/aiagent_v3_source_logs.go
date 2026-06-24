package parity

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
)

func (s *aiAgentV3SourceState) aiAgentV3TurnLogArtifacts(rec aiAgentV3SourceRecord) []Artifact {
	artifacts := make([]Artifact, 0, len(rec.Warnings)+len(rec.Errors))
	for i, message := range rec.Warnings {
		pointer := fmt.Sprintf("/warnings/%d", i)
		artifacts = append(artifacts, s.aiAgentV3LogArtifact(rec, rec.Turn, pointer, message))
	}
	for i, message := range rec.Errors {
		pointer := fmt.Sprintf("/errors/%d", i)
		artifacts = append(artifacts, s.aiAgentV3LogArtifact(rec, rec.Turn, pointer, message))
	}
	return artifacts
}

func (s *aiAgentV3SourceState) aiAgentV3SessionLogArtifact(rec aiAgentV3SourceRecord, pointer string, message string) Artifact {
	return s.aiAgentV3LogArtifact(rec, 0, pointer, message)
}

func (s *aiAgentV3SourceState) aiAgentV3LogArtifact(rec aiAgentV3SourceRecord, turnSeq int, pointer string, message string) Artifact {
	nativeTurnID := ""
	if turnSeq > 0 {
		nativeTurnID = fmt.Sprintf("turn:%d", turnSeq)
	}
	return semanticTextArtifact(semanticTextArtifactInput{
		sourceID:         s.sourceID,
		adapter:          "aiagent_v3",
		sourceFile:       s.sourceFile,
		nativeSessionID:  rec.SessionID,
		nativeTurnID:     nativeTurnID,
		nativeArtifactID: aiAgentV3LogNativeArtifactID(rec.Seq, pointer),
		class:            ClassLogEntry,
		selector: Selector{
			URI:         aiAgentV3LogSelectorURI(s.root, rec.SessionID, rec.Seq),
			JSONPointer: pointer,
		},
		text: message,
	})
}

func aiAgentV3LogNativeArtifactID(seq uint64, pointer string) string {
	return fmt.Sprintf("seq:%d:%s", seq, pointer)
}

func aiAgentV3LogSelectorURI(root string, sessionID string, seq uint64) string {
	return (&url.URL{
		Scheme:   "file",
		Path:     filepath.Join(root, "session", sessionID+".jsonl"),
		RawQuery: "seq=" + strconv.FormatUint(seq, 10),
	}).String()
}
