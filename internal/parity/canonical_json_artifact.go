package parity

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
)

type canonicalJSONArtifactInput struct {
	sourceID           string
	adapter            string
	sourceFile         string
	canonicalSessionID string
	canonicalTurnID    string
	canonicalOpID      string
	nativeSessionID    string
	nativeTurnID       string
	nativeArtifactID   string
	class              ArtifactClass
	selector           Selector
	raw                []byte
	label              string
}

func canonicalJSONArtifact(in canonicalJSONArtifactInput) (Artifact, error) {
	canonical, err := canonicalJSONBytes(in.raw, in.label)
	if err != nil {
		return Artifact{}, err
	}
	sum := sha256.Sum256(canonical)
	return Artifact{
		SchemaVersion:      SchemaVersion,
		Adapter:            in.adapter,
		SourceID:           in.sourceID,
		SourceFile:         in.sourceFile,
		CanonicalSessionID: in.canonicalSessionID,
		CanonicalTurnID:    in.canonicalTurnID,
		CanonicalOpID:      in.canonicalOpID,
		NativeSessionID:    in.nativeSessionID,
		NativeTurnID:       in.nativeTurnID,
		NativeArtifactID:   in.nativeArtifactID,
		Class:              in.class,
		Availability:       AvailabilityAvailable,
		HashDomain:         HashCanonicalJSON,
		Selector:           in.selector,
		Bytes:              int64(len(canonical)),
		Chars:              -1,
		ComputedSHA256:     fmt.Sprintf("%x", sum),
		Synthetic:          false,
		SyntheticReason:    "",
	}, nil
}

func canonicalJSONBytes(raw []byte, label string) ([]byte, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("%s is empty", label)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var doc interface{}
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode %s: %w", label, err)
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("%s contains multiple JSON values", label)
		}
		return nil, fmt.Errorf("decode trailing %s: %w", label, err)
	}
	canonical, err := canonicalIdentityBytes(doc)
	if err != nil {
		return nil, fmt.Errorf("canonicalize %s: %w", label, err)
	}
	return canonical, nil
}
