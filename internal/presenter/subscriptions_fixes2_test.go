package presenter

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// This file holds the Chunk-13 iteration-2 review fixes for the
// subscription-create surface: RNG-failure → 500 INTERNAL_ERROR (no weak
// fallback id), and POST → 503 once SSE shutdown has begun. The
// loggingResponseWriter Unwrap/FlushError fix lives in
// middleware_fixes2_test.go (a different production file). Shared helpers
// (newTestPresenter) live in presenter_test.go and are visible here as
// same-package symbols.

// failingReader is an io.Reader that always errors, standing in for a
// crypto/rand source whose ReadFull fails (effectively never on Linux, but
// the contract must hold: rest-api.md §POST /api/subscriptions says an RNG
// failure returns 500 INTERNAL_ERROR, never a weak/predictable id).
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("simulated RNG failure") }

// withRandReader swaps the package-level randReader for the duration of a
// test and restores it afterwards. Mirrors how other tests pin injectable
// package state.
func withRandReader(t *testing.T, r io.Reader) {
	t.Helper()
	prev := randReader
	randReader = r
	t.Cleanup(func() { randReader = prev })
}

// TestNewSubscriptionID_RNGFailureReturnsError pins FIX 1 at the unit level:
// when the RNG source fails, newSubscriptionID returns ("", err) — NO
// timestamp/weak fallback id.
func TestNewSubscriptionID_RNGFailureReturnsError(t *testing.T) {
	withRandReader(t, failingReader{})
	id, err := newSubscriptionID()
	if err == nil {
		t.Fatal("newSubscriptionID: want error on RNG failure, got nil")
	}
	if id != "" {
		t.Fatalf("newSubscriptionID: id = %q, want empty on RNG failure (no weak fallback)", id)
	}
}

// TestNewSubscriptionID_HappyPath pins the happy path: a real RNG source
// yields a spec-shaped id and no error.
func TestNewSubscriptionID_HappyPath(t *testing.T) {
	id, err := newSubscriptionID()
	if err != nil {
		t.Fatalf("newSubscriptionID: unexpected error: %v", err)
	}
	if !subIDPattern.MatchString(id) {
		t.Fatalf("newSubscriptionID: id = %q, want sub-<32 hex>", id)
	}
}

// TestSubscriptionsCreate_RNGFailureReturns500 pins FIX 1 at the HTTP layer:
// a POST whose id generation fails returns 500 INTERNAL_ERROR (rest-api.md
// §POST /api/subscriptions), and leaves no orphan hub/registry entry.
func TestSubscriptionsCreate_RNGFailureReturns500(t *testing.T) {
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	withRandReader(t, failingReader{})

	req := httptest.NewRequest(http.MethodPost, "/api/subscriptions", strings.NewReader(`{"filter":{}}`))
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body=%q)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), CodeInternalError) {
		t.Fatalf("body = %q, want code %s", rr.Body.String(), CodeInternalError)
	}
	// No orphan registry entry left behind by the failed create.
	if got := p.subs.count(); got != 0 {
		t.Fatalf("registry count = %d after failed create, want 0", got)
	}
}

// TestSubscriptionsCreate_ShuttingDownReturns503 pins FIX 3: once
// ShutdownSSE has begun (hub closed), a racing POST returns 503 rather than
// minting a subscription the closed hub would silently drop (rest-api.md
// §POST /api/subscriptions: "While the server is shutting down (SSE hub
// closed) new subscription creation returns 503 SERVICE_UNAVAILABLE"). The
// envelope code is SERVICE_UNAVAILABLE — NOT DB_UNAVAILABLE: the database is
// fine; the server is shutting down (observability.md §Self-Documenting
// Errors).
func TestSubscriptionsCreate_ShuttingDownReturns503(t *testing.T) {
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()

	p.ShutdownSSE()

	req := httptest.NewRequest(http.MethodPost, "/api/subscriptions", strings.NewReader(`{"filter":{}}`))
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body=%q)", rr.Code, rr.Body.String())
	}
	var env errorEnvelope
	if err := json.NewDecoder(rr.Body).Decode(&env); err != nil {
		t.Fatalf("decode envelope: %v (body=%q)", err, rr.Body.String())
	}
	if env.Error.Code != CodeUnavailable {
		t.Fatalf("code = %q, want %q (shutdown is not a DB failure)", env.Error.Code, CodeUnavailable)
	}
	// No orphan registry entry: the 503 short-circuits before create().
	if got := p.subs.count(); got != 0 {
		t.Fatalf("registry count = %d after 503, want 0 (no orphan sub)", got)
	}
}
