package aiagent_v2

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// payloadRef mirrors the v3 EvidencePayloadRef shape inside a v2
// `op.request.payload` / `op.response.payload`. Real v2 snapshots wrap
// the ref under `payload.ref` or `payload.sdk.ref`; legacy tests also
// cover the older flat helper shape.
type payloadRef struct {
	Ref           string
	Path          string
	Format        string
	Compression   string
	OriginalBytes int64
	StoredBytes   int64
	SHA256        string
	Captured      *bool
	SDK           bool
}

type payloadRefEnvelope struct {
	Ref             json.RawMessage `json:"ref,omitempty"`
	SDK             *sdkPayloadRef  `json:"sdk,omitempty"`
	Path            string          `json:"path,omitempty"`
	Format          string          `json:"format,omitempty"`
	Compression     string          `json:"compression,omitempty"`
	OriginalBytes   int64           `json:"originalBytes,omitempty"`
	StoredBytes     int64           `json:"storedBytes,omitempty"`
	CompressedBytes int64           `json:"compressedBytes,omitempty"`
	SHA256          string          `json:"sha256,omitempty"`
	Captured        *bool           `json:"captured,omitempty"`
}

type sdkPayloadRef struct {
	Ref json.RawMessage `json:"ref,omitempty"`
}

// extractPayloadRef decodes the first ref-form payload descriptor.
// Kept for legacy unit coverage; mapper emission uses extractPayloadRefs
// so regular and SDK refs on one side are both surfaced.
func extractPayloadRef(raw json.RawMessage) (payloadRef, bool) {
	refs := extractPayloadRefs(raw)
	if len(refs) == 0 {
		return payloadRef{}, false
	}
	return refs[0], true
}

// extractPayloadRefs decodes all known ref wrappers from an op's
// request/response `payload` raw JSON. Inline payloads and malformed
// JSON are treated as "no refs" because raw payload extraction is
// deliberately skipped for v2.
func extractPayloadRefs(raw json.RawMessage) []payloadRef {
	if !payloadRefCandidate(raw) {
		return nil
	}
	var env payloadRefEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil
	}
	refs := make([]payloadRef, 0, 2)
	if ref, ok := regularPayloadRef(env); ok {
		refs = append(refs, ref)
	}
	if env.SDK != nil {
		if ref, ok := decodeWrappedPayloadRef(env.SDK.Ref, true); ok {
			refs = append(refs, ref)
		}
	}
	return refs
}

func regularPayloadRef(env payloadRefEnvelope) (payloadRef, bool) {
	if legacyPayloadRefCandidate(env) && !rawPayloadRefIsObject(env.Ref) {
		return legacyPayloadRef(env)
	}
	return decodeWrappedPayloadRef(env.Ref, false)
}

func legacyPayloadRefCandidate(env payloadRefEnvelope) bool {
	if stringPayloadRef(env.Ref) != "" {
		return true
	}
	return !rawPayloadRefPresent(env.Ref) && env.Path != "" && hasLegacyPayloadEvidence(env)
}

func hasLegacyPayloadEvidence(env payloadRefEnvelope) bool {
	return env.Captured != nil || hasLegacyPayloadMetadata(env)
}

func hasLegacyPayloadMetadata(env payloadRefEnvelope) bool {
	return env.Format != "" || env.Compression != "" || env.OriginalBytes != 0 ||
		env.StoredBytes != 0 || env.CompressedBytes != 0 || env.SHA256 != ""
}

func rawPayloadRefPresent(raw json.RawMessage) bool {
	_, ok := firstNonSpaceByte(raw)
	return ok
}

func rawPayloadRefIsObject(raw json.RawMessage) bool {
	first, ok := firstNonSpaceByte(raw)
	return ok && first == '{'
}

func payloadRefCandidate(raw json.RawMessage) bool {
	first, ok := firstNonSpaceByte(raw)
	return ok && first == '{'
}

func firstNonSpaceByte(raw json.RawMessage) (byte, bool) {
	for _, b := range raw {
		if isJSONSpace(b) {
			continue
		}
		return b, true
	}
	return 0, false
}

func isJSONSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

func decodeWrappedPayloadRef(raw json.RawMessage, sdk bool) (payloadRef, bool) {
	first, ok := firstNonSpaceByte(raw)
	if !ok {
		return payloadRef{}, false
	}
	if first == '"' {
		return decodeStringPayloadRef(raw, sdk)
	}
	if first == '{' {
		return decodeEvidencePayloadRef(raw, sdk)
	}
	return payloadRef{}, false
}

func decodeStringPayloadRef(raw json.RawMessage, sdk bool) (payloadRef, bool) {
	var path string
	if err := json.Unmarshal(raw, &path); err != nil || path == "" {
		return payloadRef{}, false
	}
	return payloadRef{Ref: path, Path: path, SDK: sdk}, true
}

func decodeEvidencePayloadRef(raw json.RawMessage, sdk bool) (payloadRef, bool) {
	var env payloadRefEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return payloadRef{}, false
	}
	ref := payloadRefFromEnvelope(env)
	ref.SDK = sdk
	if ref.Path == "" && ref.Captured == nil {
		return payloadRef{}, false
	}
	return ref, true
}

func legacyPayloadRef(env payloadRefEnvelope) (payloadRef, bool) {
	ref := payloadRefFromEnvelope(env)
	ref.Ref = stringPayloadRef(env.Ref)
	if ref.Path == "" {
		ref.Path = ref.Ref
	}
	if ref.Path == "" {
		return payloadRef{}, false
	}
	return ref, true
}

func payloadRefFromEnvelope(env payloadRefEnvelope) payloadRef {
	return payloadRef{
		Path:          env.Path,
		Format:        env.Format,
		Compression:   env.Compression,
		OriginalBytes: env.OriginalBytes,
		StoredBytes:   storedPayloadBytes(env),
		SHA256:        env.SHA256,
		Captured:      env.Captured,
	}
}

func storedPayloadBytes(env payloadRefEnvelope) int64 {
	if env.CompressedBytes != 0 {
		return env.CompressedBytes
	}
	return env.StoredBytes
}

func stringPayloadRef(raw json.RawMessage) string {
	var path string
	if err := json.Unmarshal(raw, &path); err != nil {
		return ""
	}
	return path
}

// resolvePayloadPath joins root + ref.Path with the same traversal
// guard the v3 adapter uses (mirrors aiagent_v3/payloads.go:25-52).
// Returns ("file://<abs>", nil) on success or ("", err) when the
// cleaned path escapes the root.
func resolvePayloadPath(root, refPath string) (string, error) {
	if root == "" || refPath == "" {
		return "", nil
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("aiagent_v2: resolve root %q: %w", root, err)
	}
	cleanedRoot := filepath.Clean(absRoot)
	// Producer paths use forward slashes (path.posix.join in
	// ai-agent.git/src/paths.ts). Normalise to the host separator
	// before joining.
	relParts := strings.Split(refPath, "/")
	joined := filepath.Join(append([]string{cleanedRoot}, relParts...)...)
	cleaned := filepath.Clean(joined)
	rel, err := filepath.Rel(cleanedRoot, cleaned)
	if err != nil {
		return "", fmt.Errorf("aiagent_v2: relative %q: %w", refPath, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("aiagent_v2: payload path escapes root: %q", refPath)
	}
	return "file://" + filepath.ToSlash(cleaned), nil
}

// payloadKindForSide returns the canonical PayloadKind string for regular
// request/response payload refs. SDK wrappers map through
// sdkPayloadKindForSide because they use canonical llm_sdk_* payload kinds.
func payloadKindForSide(kind canonical.OpKind, side string) string {
	if kind == canonical.OpTool {
		if side == "request" {
			return "tool_request"
		}
		return "tool_response"
	}
	if side == "request" {
		return "llm_request"
	}
	return "llm_response"
}

func sdkPayloadKindForSide(side string) string {
	if side == "request" {
		return "llm_sdk_request"
	}
	return "llm_sdk_response"
}

// emitPayloadRefs surfaces PayloadRefEvent for each side of the op
// (request and response) that carries a ref-form payload. When a side
// is inline (no ref), the event is skipped silently — inline-payload
// extraction is deferred per spec §Canonical Model Gaps item 10.
// A traversal-guard rejection produces a SourceErrorEvent via onError;
// the rest of the op emission continues.
func (m *mapEmitter) emitPayloadRefs(in payloadEmitContext) {
	m.emitPayloadSide(in, "request", in.visit.op.Request)
	m.emitPayloadSide(in, "response", in.visit.op.Response)
}

type payloadEmitContext struct {
	visit opVisit
	kind  canonical.OpKind
}

func (m *mapEmitter) emitPayloadSide(in payloadEmitContext, side string, payload *opPayload) {
	if payload == nil {
		return
	}
	for _, ref := range extractPayloadRefs(payload.Payload) {
		m.appendPayloadRef(payloadRefEmit{
			ref:           ref,
			visit:         in.visit,
			payloadKind:   payloadKindForRef(in.kind, side, ref),
			path:          payloadRefEventPath(in.visit.path, side, ref),
			fallbackBytes: payload.Size,
		})
	}
}

func payloadRefEventPath(opPath, side string, ref payloadRef) string {
	path := opPath + "::payload:" + side
	if ref.SDK {
		return path + ":sdk"
	}
	return path
}

func payloadKindForRef(kind canonical.OpKind, side string, ref payloadRef) string {
	if ref.SDK {
		return sdkPayloadKindForSide(side)
	}
	return payloadKindForSide(kind, side)
}

type payloadRefEmit struct {
	ref           payloadRef
	visit         opVisit
	payloadKind   string
	path          string
	fallbackBytes int64
}

// appendPayloadRef builds and appends a PayloadRefEvent for one
// resolved ref. Path-escape rejections become non-fatal onError calls
// so a single malformed ref cannot poison the rest of the file.
func (m *mapEmitter) appendPayloadRef(in payloadRefEmit) {
	location, err := resolvePayloadLocation(m.ctx.sessionsRoot, in.ref)
	if err != nil {
		m.report(err)
		return
	}
	origBytes := in.ref.OriginalBytes
	if origBytes == 0 {
		// Producer occasionally omits originalBytes on the ref but the
		// op-level request/response.size carries the same number. Fall
		// back so the event is still useful for size aggregation.
		origBytes = in.fallbackBytes
	}
	m.append(canonical.PayloadRefEvent{
		EventBase:       baseEvent(m.ctx, in.path, msToMicrosOrFallback(0, m.ctx.rootTs)),
		SessionNativeID: in.visit.scope.sessionTrace,
		TurnSeq:         in.visit.scope.turnSeq,
		OpSeq:           in.visit.seq,
		PayloadKind:     in.payloadKind,
		Format:          in.ref.Format,
		Compression:     payloadRefCompression(in.ref),
		LocationURI:     location,
		OriginalBytes:   origBytes,
		StoredBytes:     in.ref.StoredBytes,
		SHA256:          in.ref.SHA256,
	})
}

func resolvePayloadLocation(root string, ref payloadRef) (string, error) {
	if !payloadRefCaptured(ref) || ref.Path == "" {
		return "", nil
	}
	return resolvePayloadPath(root, ref.Path)
}

func payloadRefCompression(ref payloadRef) string {
	if !payloadRefCaptured(ref) || ref.Path == "" {
		return ""
	}
	return ref.Compression
}

func payloadRefCaptured(ref payloadRef) bool {
	return ref.Captured == nil || *ref.Captured
}

// msToMicrosOrFallback returns msToMicros(ms) when ms is positive,
// otherwise the provided fallback. Used for PayloadRefEvent timestamps
// where the producer does not record a per-payload time and the
// surrounding op's start/end is the best signal.
func msToMicrosOrFallback(ms, fallback int64) int64 {
	if ms <= 0 {
		return fallback
	}
	return ms * 1000
}
