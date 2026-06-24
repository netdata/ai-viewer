package claude_code

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// payloadLocationURI builds a containment-checked "file://<abs>" location for
// a payload artifact (spec §5.4, §6.1). The path is resolved with
// filepath.EvalSymlinks and verified to stay inside the configured projects
// root before it is surfaced (security.md §6 "No symlink traversal escape").
// Returns ("", err) when the path escapes the root so the caller can refuse
// the ref and surface a SourceError. When root is empty (mapper-only unit
// tests) the check is skipped and the cleaned absolute path is returned.
func payloadLocationURI(root, abs string) (string, error) {
	cleaned := filepath.Clean(abs)
	if root == "" {
		return "file://" + filepath.ToSlash(cleaned), nil
	}
	resolved, ok, err := resolveWithinRoot(root, cleaned)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("payload path escapes root: %q", abs)
	}
	return "file://" + filepath.ToSlash(resolved), nil
}

// resolveWithinRoot resolves both root AND abs through filepath.EvalSymlinks and
// reports whether the fully-resolved path stays inside the fully-resolved root
// (security.md §6 "No symlink traversal escape", spec §6.1, P2e). It resolves
// the root every call, so per-file walk callers that share one root should
// resolve it once and use withinResolvedRoot instead. A legitimately symlinked
// projects root (e.g. ~/.claude → an external volume) still works: containment
// is judged against the RESOLVED root. A symlink inside the tree that points
// outside the resolved root is refused (ok=false). Returns:
//   - (resolvedAbs, true, nil)  — abs resolves to a path under the root.
//   - ("", false, nil)          — abs resolves outside the root (escape).
//   - ("", false, err)          — the path or root could not be resolved.
//
// When abs does not yet exist on disk, the deepest existing ancestor is
// resolved and the non-existent tail re-appended, so a not-yet-created file is
// judged by where it WOULD live (a non-existent path cannot itself be a
// symlink to elsewhere).
func resolveWithinRoot(root, abs string) (string, bool, error) {
	resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return "", false, fmt.Errorf("resolve root %q: %w", root, err)
	}
	return withinResolvedRoot(resolvedRoot, abs)
}

// withinResolvedRoot is resolveWithinRoot's core for callers that have ALREADY
// resolved the projects root once (the directory-walk hot path: every meta and
// every discovered transcript shares one resolved root, so re-running
// EvalSymlinks on the root per file is wasted work — P2-perf). resolvedRoot MUST
// be the output of filepath.EvalSymlinks on the configured root; only abs is
// resolved here. Containment semantics are identical to resolveWithinRoot.
func withinResolvedRoot(resolvedRoot, abs string) (string, bool, error) {
	resolvedAbs, err := evalSymlinksAllowingTail(filepath.Clean(abs))
	if err != nil {
		return "", false, fmt.Errorf("resolve path %q: %w", abs, err)
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedAbs)
	if err != nil {
		return "", false, fmt.Errorf("relative %q under %q: %w", resolvedAbs, resolvedRoot, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false, nil
	}
	return resolvedAbs, true, nil
}

// evalSymlinksAllowingTail resolves symlinks in abs, tolerating a not-yet-
// created leaf/tail: it walks up to the deepest existing ancestor, resolves
// that, and re-joins the non-existent remainder. A non-existent path cannot be
// a symlink itself, so judging it by its resolved parent is sound.
func evalSymlinksAllowingTail(abs string) (string, error) {
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(abs)
	if parent == abs {
		// Reached the filesystem root without an existing ancestor.
		return abs, nil
	}
	resolvedParent, perr := evalSymlinksAllowingTail(parent)
	if perr != nil {
		return "", perr
	}
	return filepath.Join(resolvedParent, filepath.Base(abs)), nil
}

func (m *fileMapper) payloadURI(lineNo int64) string {
	anchor := ""
	if lineNo > 0 {
		anchor = fmt.Sprintf("#L%d", lineNo)
	}
	if m.absPath == "" {
		return anchor
	}
	uri, err := payloadLocationURI(m.root, m.absPath)
	if err != nil {
		uri = "file://" + filepath.ToSlash(filepath.Clean(m.absPath))
	}
	return uri + anchor
}

func (m *fileMapper) payloadURIWithPointer(lineNo int64, pointer string) string {
	uri := m.payloadURI(lineNo)
	if pointer == "" {
		return uri
	}
	parsed, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	values := parsed.Query()
	values.Set("json_pointer", pointer)
	parsed.RawQuery = values.Encode()
	return parsed.String()
}

func (m *fileMapper) emitInlinePayload(base canonical.EventBase, turnSeq, opSeq int, kind, format string, rec record, pointer string) (canonical.PayloadRefEvent, error) {
	originalBytes, err := inlinePayloadOriginalBytes(rec, pointer)
	if err != nil {
		return canonical.PayloadRefEvent{}, err
	}
	return canonical.PayloadRefEvent{
		EventBase:       base,
		SessionNativeID: m.nativeID,
		TurnSeq:         turnSeq,
		OpSeq:           opSeq,
		PayloadKind:     kind,
		Format:          format,
		LocationURI:     m.payloadURIWithPointer(rec.LineNo, pointer),
		OriginalBytes:   originalBytes,
	}, nil
}

// emitSummaryPayload returns a PayloadRefEvent for the post-compaction summary
// user message (spec §5.4, §9.2). The summary text lives inline in the
// transcript; the ref lets the UI render it in a compaction lane. It is scoped
// to the compaction op (turnSeq/opSeq) so it references an op that EXISTS
// (P1.1a): payload_refs.op_id is NOT NULL REFERENCES ops(id), and the summary
// belongs to its compaction. The drop guard keys on opSeq == 0 ONLY — the real
// "no owning compaction op" sentinel (a compaction always sets opSeq>=1). A
// compaction can legitimately occur BEFORE any operator prompt (turn 0,
// P2.3b): keying the guard on turnSeq==0 too would wrongly drop that valid
// turn-0 summary. Returns (zero, false, nil) only when no compaction op has
// been seen on the file (opSeq==0) — without an owning op the ref would
// FK-roll-back the batch.
func (m *fileMapper) emitSummaryPayload(base canonical.EventBase, turnSeq, opSeq int, rec record) (canonical.PayloadRefEvent, bool, error) {
	if opSeq == 0 {
		return canonical.PayloadRefEvent{}, false, nil
	}
	ref, err := m.emitInlinePayload(base, turnSeq, opSeq, "log", "text", rec, "/message/content")
	if err != nil {
		return canonical.PayloadRefEvent{}, false, err
	}
	return ref, true, nil
}

func jsonPointerLogicalBytes(raw []byte, pointer string) ([]byte, error) {
	var doc interface{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode inline payload JSON for pointer %q: %w", pointer, err)
	}
	value, err := jsonPointerValue(doc, pointer)
	if err != nil {
		return nil, err
	}
	if text, ok := value.(string); ok {
		return []byte(text), nil
	}
	return canonicalJSONBytes(value)
}

func inlinePayloadOriginalBytes(rec record, pointer string) (int64, error) {
	if n, ok, err := inlinePayloadOriginalBytesFast(rec, pointer); ok || err != nil {
		return n, err
	}
	logicalBytes, err := jsonPointerLogicalBytes(rec.Raw, pointer)
	if err != nil {
		return 0, err
	}
	return int64(len(logicalBytes)), nil
}

func inlinePayloadOriginalBytesFast(rec record, pointer string) (int64, bool, error) {
	if pointer == "/message/content" {
		if rec.User == nil {
			return 0, false, nil
		}
		text, _, isString := classifyUserContent(rec.User)
		if !isString {
			return 0, false, nil
		}
		return int64(len(text)), true, nil
	}
	index, field, ok, err := parseMessageContentPointer(pointer)
	if err != nil || !ok {
		return 0, ok, err
	}
	if rec.Assistant != nil && index < len(rec.Assistant.Content) {
		return contentBlockFieldOriginalBytes(rec.Assistant.Content[index], field)
	}
	if rec.User != nil {
		_, blocks, isString := classifyUserContent(rec.User)
		if !isString && index < len(blocks) {
			return contentBlockFieldOriginalBytes(blocks[index], field)
		}
	}
	return 0, false, nil
}

func parseMessageContentPointer(pointer string) (index int, field string, ok bool, err error) {
	rest, ok := strings.CutPrefix(pointer, "/message/content/")
	if !ok {
		return 0, "", false, nil
	}
	indexToken, field, hasField := strings.Cut(rest, "/")
	index, err = parseJSONPointerIndex(indexToken)
	if err != nil {
		return 0, "", true, err
	}
	if !hasField {
		field = ""
	}
	return index, field, true, nil
}

func contentBlockFieldOriginalBytes(block contentBlock, field string) (int64, bool, error) {
	switch field {
	case "text":
		return int64(len(block.Text)), true, nil
	case "thinking":
		return int64(len(block.Thinking)), true, nil
	case "input":
		n, err := rawMessageOriginalBytes(block.Input)
		return n, true, err
	case "content":
		n, err := rawMessageOriginalBytes(block.Content)
		return n, true, err
	default:
		return 0, false, nil
	}
}

func rawMessageOriginalBytes(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 {
		return 0, nil
	}
	var value interface{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return 0, err
	}
	if text, ok := value.(string); ok {
		return int64(len(text)), nil
	}
	logicalBytes, err := canonicalJSONBytes(value)
	if err != nil {
		return 0, err
	}
	return int64(len(logicalBytes)), nil
}

func jsonPointerValue(doc interface{}, pointer string) (interface{}, error) {
	if pointer == "" {
		return doc, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("json_pointer %q must start with /", pointer)
	}
	current := doc
	for _, rawToken := range strings.Split(pointer[1:], "/") {
		token, err := unescapeJSONPointerToken(rawToken)
		if err != nil {
			return nil, err
		}
		next, err := jsonPointerStep(current, token)
		if err != nil {
			return nil, fmt.Errorf("resolve json_pointer %q token %q: %w", pointer, token, err)
		}
		current = next
	}
	return current, nil
}

func jsonPointerStep(current interface{}, token string) (interface{}, error) {
	switch typed := current.(type) {
	case map[string]interface{}:
		value, ok := typed[token]
		if !ok {
			return nil, fmt.Errorf("object key not found")
		}
		return value, nil
	case []interface{}:
		index, err := parseJSONPointerIndex(token)
		if err != nil {
			return nil, err
		}
		if index >= len(typed) {
			return nil, fmt.Errorf("array index out of range")
		}
		return typed[index], nil
	default:
		return nil, fmt.Errorf("cannot descend into %T", current)
	}
}

func parseJSONPointerIndex(token string) (int, error) {
	if token == "" {
		return 0, fmt.Errorf("array index is empty")
	}
	if len(token) > 1 && token[0] == '0' {
		return 0, fmt.Errorf("array index has leading zero")
	}
	for _, r := range token {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("array index is not decimal")
		}
	}
	index, err := strconv.Atoi(token)
	if err != nil {
		return 0, fmt.Errorf("parse array index: %w", err)
	}
	return index, nil
}

func unescapeJSONPointerToken(token string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(token); i++ {
		if token[i] != '~' {
			out.WriteByte(token[i])
			continue
		}
		if i+1 >= len(token) {
			return "", fmt.Errorf("json_pointer token %q has trailing escape", token)
		}
		switch token[i+1] {
		case '0':
			out.WriteByte('~')
		case '1':
			out.WriteByte('/')
		default:
			return "", fmt.Errorf("json_pointer token %q has invalid escape", token)
		}
		i++
	}
	return out.String(), nil
}

func canonicalJSONBytes(value interface{}) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, fmt.Errorf("encode canonical JSON: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
