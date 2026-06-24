package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/netdata/ai-viewer/internal/canonical"
)

const legacyFlatJSONMax = 16 * 1024 * 1024

type legacyFlatRollout struct {
	Session json.RawMessage   `json:"session"`
	Items   []json.RawMessage `json:"items"`
}

func scanLegacyRollouts(ctx context.Context, resolvedRoot, sourceID string, legacy []string, cur Cursor, out chan<- canonical.Event, onError func(error)) (Cursor, error) {
	for _, base := range legacy {
		if err := ctx.Err(); err != nil {
			return cur, err
		}
		if cur.legacyIngested(base) {
			continue
		}
		n, err := readLegacyRollout(ctx, resolvedRoot, sourceID, base, out)
		if err != nil {
			if isContextStop(err) {
				return cur, err
			}
			onError(err)
			cur = cur.withLegacyIngested(base)
			continue
		}
		_ = n
		cur = cur.withLegacyIngested(base)
	}
	return cur, nil
}

func readLegacyRollout(ctx context.Context, resolvedRoot, sourceID, base string, out chan<- canonical.Event) (int, error) {
	abs := filepath.Join(resolvedRoot, base)
	resolvedAbs, ok, err := withinResolvedRoot(resolvedRoot, abs)
	if err != nil {
		return 0, fmt.Errorf("codex: cannot resolve legacy flat JSON %s for containment: %w", base, err)
	}
	if !ok {
		return 0, fmt.Errorf("codex: legacy flat JSON %s resolves outside the sessions root; skipping (symlink escape)", base)
	}
	body, info, err := readLegacyFlatJSON(resolvedAbs)
	if err != nil {
		return 0, err
	}
	legacy, trailingErr, err := decodeLegacyFlatRollout(body)
	if err != nil {
		return 0, fmt.Errorf("codex: decode legacy flat JSON %s: %w", base, err)
	}

	mapper := newFileMapper(mapperConfig{
		sourceID: sourceID,
		absPath:  resolvedAbs,
		root:     resolvedRoot,
		nativeID: legacyNativeID(base),
	})
	emitted := 0
	sessionRecord, skip, err := legacySessionRecord(legacy.Session)
	if err != nil {
		return emitted, fmt.Errorf("codex: decode legacy session %s: %w", base, err)
	}
	if !skip {
		n, err := mapAndEmitLegacyRecord(ctx, mapper, sessionRecord, out)
		emitted += n
		if err != nil {
			return emitted, err
		}
	}
	for i, item := range legacy.Items {
		rec, itemSkip, err := legacyItemRecord(item, i)
		if err != nil {
			return emitted, fmt.Errorf("codex: decode legacy item %s[%d]: %w", base, i, err)
		}
		if itemSkip {
			continue
		}
		n, err := mapAndEmitLegacyRecord(ctx, mapper, rec, out)
		emitted += n
		if err != nil {
			return emitted, err
		}
	}
	for _, ev := range mapper.finalizeAtEOF(false, info.ModTime().UnixMicro()) {
		select {
		case <-ctx.Done():
			return emitted, ctx.Err()
		case out <- ev:
			emitted++
		}
	}
	if trailingErr != nil {
		return emitted, fmt.Errorf("codex: legacy flat JSON %s: %w", base, trailingErr)
	}
	return emitted, nil
}

func readLegacyFlatJSON(path string) ([]byte, os.FileInfo, error) {
	file, err := os.Open(path) // #nosec G304 -- path is containment-checked under the configured read-only sessions root.
	if err != nil {
		return nil, nil, fmt.Errorf("open legacy flat JSON %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, fmt.Errorf("stat legacy flat JSON %s: %w", path, err)
	}
	limited := io.LimitReader(file, legacyFlatJSONMax+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, nil, fmt.Errorf("read legacy flat JSON %s: %w", path, err)
	}
	if len(body) > legacyFlatJSONMax {
		return nil, nil, fmt.Errorf("legacy flat JSON %s exceeds %d bytes", path, legacyFlatJSONMax)
	}
	return body, info, nil
}

func decodeLegacyFlatRollout(body []byte) (legacyFlatRollout, error, error) {
	var legacy legacyFlatRollout
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&legacy); err != nil {
		return legacyFlatRollout{}, nil, err
	}
	offset := decoder.InputOffset()
	if offset < 0 || offset > int64(len(body)) {
		return legacyFlatRollout{}, nil, fmt.Errorf("invalid legacy flat JSON decoder offset %d", offset)
	}
	if trailing := bytes.TrimSpace(body[offset:]); len(trailing) > 0 {
		return legacy, fmt.Errorf("trailing non-whitespace bytes after first object (%d bytes)", len(trailing)), nil
	}
	return legacy, nil, nil
}

func legacySessionRecord(raw json.RawMessage) (record, bool, error) {
	body := bytes.TrimSpace(raw)
	if len(body) == 0 || bytes.Equal(body, []byte("null")) {
		return record{}, true, nil
	}
	var session sessionMetaPayload
	if err := json.Unmarshal(body, &session); err != nil {
		return record{}, false, err
	}
	return record{
		Env: envelope{
			TS:      session.Timestamp,
			Type:    recSessionMeta,
			Payload: append(json.RawMessage(nil), body...),
		},
		SessionMeta:          &session,
		Raw:                  append([]byte(nil), body...),
		PayloadPointerPrefix: "/session",
	}, false, nil
}

func legacyItemRecord(raw json.RawMessage, index int) (record, bool, error) {
	body := bytes.TrimSpace(raw)
	if len(body) == 0 || bytes.Equal(body, []byte("null")) {
		return record{}, true, nil
	}
	rec := record{
		Env: envelope{
			Type:    recResponseItem,
			Payload: append(json.RawMessage(nil), body...),
		},
		Raw:                  append([]byte(nil), body...),
		PayloadPointerPrefix: fmt.Sprintf("/items/%d", index),
	}
	return decodeResponseItem(rec)
}

func mapAndEmitLegacyRecord(ctx context.Context, mapper *fileMapper, rec record, out chan<- canonical.Event) (int, error) {
	mapper.setLineNo(0)
	events, err := mapper.mapRecord(rec)
	if err != nil {
		return 0, err
	}
	emitted := 0
	for _, ev := range events {
		select {
		case <-ctx.Done():
			return emitted, ctx.Err()
		case out <- ev:
			emitted++
		}
	}
	return emitted, nil
}

func legacyNativeID(base string) string {
	stem := strings.TrimSuffix(base, ".json")
	stem = strings.TrimPrefix(stem, rolloutPrefix)
	if id := uuidTail(stem); id != "" {
		return id
	}
	return stem
}
