package presenter

import "database/sql"

func payloadArtifactClass(kind string) string {
	switch kind {
	case "llm_request":
		return "llm_request"
	case "llm_response":
		return "llm_response"
	case "llm_sdk_request", "sdk_request":
		return "llm_sdk_request"
	case "llm_sdk_response", "sdk_response":
		return "llm_sdk_response"
	case "llm_reasoning", "reasoning_stream":
		return "reasoning_text"
	case "tool_request":
		return "tool_request"
	case "tool_response":
		return "tool_response"
	case "log":
		return "log"
	default:
		return kind
	}
}

func applyPayloadRefScalars(pr *payloadRef, compression sql.NullString, origBytes, storedBytes sql.NullInt64, locationURI string, sha256 sql.NullString, includeProof bool) {
	pr.ArtifactClass = payloadArtifactClass(pr.Kind)
	if compression.Valid {
		v := compression.String
		pr.Compression = &v
	}
	if origBytes.Valid {
		v := origBytes.Int64
		pr.OriginalBytes = &v
	}
	if storedBytes.Valid {
		v := storedBytes.Int64
		pr.StoredBytes = &v
	}
	if includeProof {
		pr.LocationURI = &locationURI
		if sha256.Valid {
			v := sha256.String
			pr.SHA256 = &v
		}
	}
}
