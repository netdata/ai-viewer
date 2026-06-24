package parity

import (
	"encoding/json"
	"fmt"
	"net/url"
)

func aiAgentV2SessionStatus(node aiAgentV2OpTree) string {
	if node.Success != nil {
		if *node.Success {
			return "completed"
		}
		return "failed"
	}
	if node.EndedAt != nil {
		return "interrupted"
	}
	if len(node.Turns) == 0 && len(node.Steps) == 0 {
		return "abandoned"
	}
	return "running"
}

func turnStatusFromAIAgentV2Ops(ops []aiAgentV2Operation) string {
	if len(ops) == 0 {
		return "completed"
	}
	allCompleted := true
	for i := range ops {
		switch ops[i].Status {
		case "failed":
			return "failed"
		case "ok":
			continue
		default:
			allCompleted = false
		}
	}
	if allCompleted {
		return "completed"
	}
	return "running"
}

func aiAgentV2OpStatus(status string) string {
	switch status {
	case "ok":
		return "completed"
	case "failed":
		return "failed"
	case "":
		return "running"
	default:
		return status
	}
}

func aiAgentV2OpTimes(op aiAgentV2Operation, fallbackUs int64) (int64, int64) {
	startUs := aiAgentV2TimestampOrRoot(op.StartedAt, fallbackUs)
	endUs := startUs
	if op.EndedAt != nil {
		endUs = aiAgentV2TimestampUS(*op.EndedAt)
	}
	return startUs, endUs
}

func aiAgentV2HasReasoningOp(op aiAgentV2Operation) bool {
	return op.Kind == "llm" && op.Reasoning != nil && op.Reasoning.Final != ""
}

func aiAgentV2AttrString(attrs map[string]json.RawMessage, key string) string {
	raw, ok := attrs[key]
	if !ok {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}

func aiAgentV2TimestampUS(ms int64) int64 {
	if ms <= 0 {
		return 0
	}
	return ms * 1000
}

func aiAgentV2TimestampOrRoot(ms int64, rootUs int64) int64 {
	if got := aiAgentV2TimestampUS(ms); got != 0 {
		return got
	}
	return rootUs
}

func aiAgentV2OptionalTimestamp(ms *int64) *int64 {
	if ms == nil {
		return nil
	}
	return ptrInt64(aiAgentV2TimestampUS(*ms))
}

func aiAgentV2NativeTurnID(turnSeq int64) string {
	return fmt.Sprintf("turn:%d", turnSeq)
}

func aiAgentV2NativeOpID(turnSeq int64, opSeq int64) string {
	return fmt.Sprintf("op:%d:%d", turnSeq, opSeq)
}

func aiAgentV2SelectorURI(kind string, sessionTrace string, nativeID string) string {
	return "aiagent-v2-source://" + kind + "/" + url.PathEscape(sessionTrace) + "/" + url.PathEscape(nativeID)
}
