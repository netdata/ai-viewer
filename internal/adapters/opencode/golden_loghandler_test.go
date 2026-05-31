package opencode

import (
	"context"
	"log/slog"
	"sync"
)

// This file holds the record-capturing slog.Handler the AC#5 missing-column INF
// assertion (golden_invariants_test.go:TestGoldenInvariant_DSchemaDrift_MissingColumnsLoggedINF)
// uses to capture structured log records rather than parsing text logs.

// capturedRecord is one slog record captured by captureHandler: its level,
// message, and string-valued attributes (the only kind the adapter logs here),
// flattened across any WithAttrs presets and the per-call attrs.
type capturedRecord struct {
	level   slog.Level
	message string
	attrs   map[string]string
}

// captureHandler is a minimal structured slog.Handler that records every Handle
// call for assertion (level + message + string attrs). New() wraps the logger
// with .With("adapter", …, "db", …) before scanLoop/tailLoop log the per-
// (table,column) attrs, so WithAttrs returns a DERIVED handler that the adapter
// actually logs through. All derived handlers share one *captureStore (mutex +
// records slice), so the original handler the test holds sees every record
// regardless of which derived handler appended it (append-reallocation safe).
// Concurrency-safe so the Scan goroutine and the test reader never race.
type captureHandler struct {
	store   *captureStore
	presets map[string]string
}

// captureStore is the shared sink every derived captureHandler appends to.
type captureStore struct {
	mu   sync.Mutex
	recs []capturedRecord
}

func (h *captureHandler) ensure() *captureStore {
	if h.store == nil {
		h.store = &captureStore{}
	}
	return h.store
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	st := h.ensure()
	attrs := map[string]string{}
	for k, v := range h.presets {
		attrs[k] = v
	}
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.String()
		return true
	})
	st.mu.Lock()
	st.recs = append(st.recs, capturedRecord{level: r.Level, message: r.Message, attrs: attrs})
	st.mu.Unlock()
	return nil
}

func (h *captureHandler) WithAttrs(as []slog.Attr) slog.Handler {
	merged := map[string]string{}
	for k, v := range h.presets {
		merged[k] = v
	}
	for _, a := range as {
		merged[a.Key] = a.Value.String()
	}
	return &captureHandler{store: h.ensure(), presets: merged}
}

func (h *captureHandler) WithGroup(string) slog.Handler { return h }

// records returns a snapshot of every captured record, across all derived
// handlers (they share h's store).
func (h *captureHandler) records() []capturedRecord {
	st := h.ensure()
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make([]capturedRecord, len(st.recs))
	copy(out, st.recs)
	return out
}
