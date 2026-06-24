package parity

import "unicode/utf8"

type semanticTextArtifactInput struct {
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
	text               string
}

func semanticTextArtifact(in semanticTextArtifactInput) Artifact {
	availability := AvailabilityAvailable
	if in.text == "" {
		availability = AvailabilitySourceEmpty
	}
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
		Availability:       availability,
		HashDomain:         HashSemanticText,
		Selector:           in.selector,
		Bytes:              int64(len(in.text)),
		Chars:              int64(utf8.RuneCountInString(in.text)),
		ComputedSHA256:     stringSHA256(in.text),
		Synthetic:          false,
		SyntheticReason:    "",
	}
}
