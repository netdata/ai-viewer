package parity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const codexLegacySourceFileMax = 16 * 1024 * 1024

type codexLegacyFlatSourceFile struct {
	Session json.RawMessage   `json:"session"`
	Items   []json.RawMessage `json:"items"`
}

type codexLegacyTrailingCorruption struct {
	offset int64
	bytes  []byte
	err    error
}

func codexSourceLegacyFlatJSON(root string, path string) bool {
	if filepath.Dir(filepath.Clean(path)) != filepath.Clean(root) {
		return false
	}
	name := filepath.Base(path)
	return strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, ".json")
}

func writeCodexLegacySourceFile(ctx context.Context, path string, sourceID string, writer ArtifactWriter) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	body, info, err := readCodexLegacySourceFile(path)
	if err != nil {
		return err
	}
	legacy, document, trailing, err := decodeCodexLegacyFlatSourceFile(body)
	if err != nil {
		return fmt.Errorf("%s: decode legacy flat JSON: %w", path, err)
	}

	state := newCodexSourceState(sourceID, path, info.ModTime(), codexSourceRootIndex{})
	if err := updateCodexSourceSession(&state, legacy.Session, 0); err != nil {
		return err
	}
	var session struct {
		Timestamp string `json:"timestamp"`
	}
	if err := decodeJSONPayload(legacy.Session, &session); err != nil {
		return fmt.Errorf("%s: decode legacy session timestamp: %w", path, err)
	}
	tsUs, err := parseCodexSourceTimestamp(session.Timestamp)
	if err != nil {
		return err
	}
	artifacts, err := state.sessionBoundary(tsUs, 0)
	if err != nil {
		return err
	}
	if err := writeArtifacts(ctx, writer, artifacts); err != nil {
		return fmt.Errorf("%s: write session artifacts: %w", path, err)
	}
	for i, item := range legacy.Items {
		if err := ctx.Err(); err != nil {
			return err
		}
		itemArtifacts, err := extractCodexLegacyItemArtifacts(&state, document, item, i)
		if err != nil {
			return fmt.Errorf("%s: item %d: %w", path, i, err)
		}
		if err := writeArtifacts(ctx, writer, itemArtifacts); err != nil {
			return fmt.Errorf("%s: item %d: write artifact: %w", path, i, err)
		}
	}
	eofArtifacts, err := state.finalizeAtEOF()
	if err != nil {
		return err
	}
	if err := writeArtifacts(ctx, writer, eofArtifacts); err != nil {
		return fmt.Errorf("%s: write eof artifact: %w", path, err)
	}
	if trailing != nil {
		artifact := codexLegacySourceCorruptionArtifact(&state, trailing)
		if err := writer.WriteArtifact(ctx, artifact); err != nil {
			return fmt.Errorf("%s: write source corruption artifact: %w", path, err)
		}
		return fmt.Errorf("%s: legacy flat JSON: %w", path, trailing.err)
	}
	return nil
}

func decodeCodexLegacyFlatSourceFile(body []byte) (codexLegacyFlatSourceFile, []byte, *codexLegacyTrailingCorruption, error) {
	var legacy codexLegacyFlatSourceFile
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&legacy); err != nil {
		return codexLegacyFlatSourceFile{}, nil, nil, err
	}
	offset := decoder.InputOffset()
	if offset < 0 || offset > int64(len(body)) {
		return codexLegacyFlatSourceFile{}, nil, nil, fmt.Errorf("invalid legacy flat JSON decoder offset %d", offset)
	}
	document := append([]byte(nil), body[:offset]...)
	trailingBytes := append([]byte(nil), body[offset:]...)
	if trailing := bytes.TrimSpace(trailingBytes); len(trailing) > 0 {
		return legacy, document, &codexLegacyTrailingCorruption{
			offset: offset,
			bytes:  trailingBytes,
			err:    fmt.Errorf("trailing non-whitespace bytes after first object (%d bytes)", len(trailing)),
		}, nil
	}
	return legacy, document, nil, nil
}

func readCodexLegacySourceFile(path string) ([]byte, os.FileInfo, error) {
	file, err := os.Open(path) // #nosec G304 -- path comes from Codex parity discovery after source-root containment checks.
	if err != nil {
		return nil, nil, fmt.Errorf("open codex legacy source file read-only: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, fmt.Errorf("stat codex legacy source file: %w", err)
	}
	body, err := io.ReadAll(io.LimitReader(file, codexLegacySourceFileMax+1))
	if err != nil {
		return nil, nil, fmt.Errorf("read codex legacy source file: %w", err)
	}
	if len(body) > codexLegacySourceFileMax {
		return nil, nil, fmt.Errorf("codex legacy source file exceeds %d bytes", codexLegacySourceFileMax)
	}
	return body, info, nil
}

func extractCodexLegacyItemArtifacts(state *codexSourceState, document []byte, payload json.RawMessage, index int) ([]Artifact, error) {
	var body struct {
		Type   string `json:"type"`
		Role   string `json:"role"`
		Name   string `json:"name"`
		CallID string `json:"call_id"`
	}
	if err := decodeJSONPayload(payload, &body); err != nil {
		return nil, fmt.Errorf("decode legacy item payload: %w", err)
	}
	prefix := fmt.Sprintf("/items/%d", index)
	switch body.Type {
	case "message":
		class := ClassAssistantMessage
		var structural []Artifact
		var err error
		if body.Role == "user" {
			class = ClassUserPrompt
			structural, err = state.recordUserInput(0)
		} else {
			structural, err = state.recordCompletedOp(0, "llm", "message")
		}
		if err != nil {
			return nil, err
		}
		payloadArtifacts, err := codexDocumentPointerArtifacts(state, document, class, textPointers(payload, "content", prefix))
		return append(structural, payloadArtifacts...), err
	case "reasoning":
		structural, err := state.recordCompletedOp(0, "reasoning", "reasoning")
		if err != nil {
			return nil, err
		}
		pointers := append(textPointers(payload, "summary", prefix), textPointers(payload, "content", prefix)...)
		payloadArtifacts, err := codexDocumentPointerArtifacts(state, document, ClassReasoningText, pointers)
		return append(structural, payloadArtifacts...), err
	case "local_shell_call":
		state.recordToolStart(0, "shell", "shell", body.CallID)
		return codexDocumentPointerArtifacts(state, document, ClassToolRequest, scalarPointers(payload, "action", prefix))
	case "local_shell_call_output", "function_call_output":
		structural, err := state.recordToolOutput(0, body.CallID)
		if err != nil {
			return nil, err
		}
		payloadArtifacts, err := codexDocumentPointerArtifacts(state, document, ClassToolResponse, scalarPointers(payload, "output", prefix))
		return append(structural, payloadArtifacts...), err
	default:
		return nil, fmt.Errorf("unknown legacy item type %q", body.Type)
	}
}

func codexDocumentPointerArtifacts(state *codexSourceState, document []byte, class ArtifactClass, pointers []string) ([]Artifact, error) {
	artifacts := make([]Artifact, 0, len(pointers))
	for _, pointer := range pointers {
		artifacts = append(artifacts, codexDocumentPointerArtifact(state, document, class, pointer))
	}
	return artifacts, nil
}

func codexLegacySourceCorruptionArtifact(state *codexSourceState, corruption *codexLegacyTrailingCorruption) Artifact {
	nativeSessionID := state.nativeSessionID
	if nativeSessionID == "" {
		nativeSessionID = "source:" + state.sourceID
	}
	selectorURI := (&url.URL{Scheme: "file", Path: state.sourceFile}).String()
	end := corruption.offset + int64(len(corruption.bytes))
	return Artifact{
		SchemaVersion:    SchemaVersion,
		Adapter:          "codex",
		SourceID:         state.sourceID,
		SourceFile:       state.sourceFile,
		NativeSessionID:  nativeSessionID,
		NativeArtifactID: fmt.Sprintf("source_corruption:file:%s:trailing", filepath.Base(state.sourceFile)),
		Class:            ClassSourceCorruption,
		Availability:     AvailabilitySourceCorrupt,
		HashDomain:       HashRawBytes,
		Selector: Selector{
			URI:       selectorURI,
			ByteRange: &ByteRange{Start: corruption.offset, End: end},
		},
		Bytes:          int64(len(corruption.bytes)),
		Chars:          -1,
		ComputedSHA256: stringSHA256(string(corruption.bytes)),
		IntegrityFailures: []IntegrityFailure{
			int64IntegrityFailure("trailing_bytes", 0, int64(len(corruption.bytes))),
		},
	}
}

func codexDocumentPointerArtifact(state *codexSourceState, document []byte, class ArtifactClass, pointer string) Artifact {
	resolved, err := resolveJSONPointerPayload(document, pointer)
	hashDomain := resolved.hashDomain
	if hashDomain == "" {
		hashDomain = HashSemanticText
	}
	availability := AvailabilityAvailable
	bytesLen := int64(len(resolved.bytes))
	hash := stringSHA256(string(resolved.bytes))
	chars := int64(-1)
	if err != nil {
		availability = AvailabilityUnverifiable
		bytesLen = -1
		hash = ""
	} else if bytesLen == 0 {
		availability = AvailabilitySourceEmpty
	}
	if err == nil && resolved.hashDomain == HashSemanticText && utf8.Valid(resolved.bytes) {
		chars = int64(utf8.RuneCount(resolved.bytes))
	}
	nativeSessionID := state.nativeSessionID
	if nativeSessionID == "" {
		nativeSessionID = "source:" + state.sourceID
	}
	selectorURI := (&url.URL{Scheme: "file", Path: state.sourceFile}).String()
	return Artifact{
		SchemaVersion:    SchemaVersion,
		Adapter:          "codex",
		SourceID:         state.sourceID,
		SourceFile:       state.sourceFile,
		NativeSessionID:  nativeSessionID,
		NativeArtifactID: fmt.Sprintf("file:%s:%s", filepath.Base(state.sourceFile), pointer),
		Class:            class,
		Availability:     availability,
		HashDomain:       hashDomain,
		Selector:         Selector{URI: selectorURI, JSONPointer: pointer},
		Bytes:            bytesLen,
		Chars:            chars,
		ComputedSHA256:   hash,
		Synthetic:        false,
		SyntheticReason:  "",
	}
}
