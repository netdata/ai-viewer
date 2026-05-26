package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// canonicalSessionID computes the stable canonical sessions.id from the
// source identity and the source's native session id. Stability across
// runs is the goal — re-ingesting the same source produces the same
// canonical id so UPSERT semantics flow naturally.
//
// The hash is sha256(sourceID + "|" + nativeID) truncated to 32 hex
// chars (128 bits). That's enough entropy for collision-freedom across
// a multi-million-row corpus and short enough to be index-friendly.
func canonicalSessionID(sourceID, nativeID string) string {
	return hashID(sourceID, "|", nativeID)
}

// canonicalTurnID derives the stable canonical turns.id from the
// canonical session id and the turn seq.
func canonicalTurnID(sessionID string, seq int) string {
	return hashID(sessionID, "|t|", strconv.Itoa(seq))
}

// canonicalOpID derives the stable canonical ops.id from the turn id
// and the op seq within the turn.
func canonicalOpID(turnID string, seq int) string {
	return hashID(turnID, "|o|", strconv.Itoa(seq))
}

// hashID returns sha256(parts...) truncated to 32 hex chars. The
// truncation is to 16 bytes (128 bits), which is plenty for a workload
// where every row already has a UNIQUE constraint on a natural key
// (source_id, native_id) catching the rare collision deterministically.
func hashID(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		// Writing to sha256.Hash never returns an error per
		// hash.Hash's documented contract.
		_, _ = h.Write([]byte(p))
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:16])
}
