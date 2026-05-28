package presenter

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// subIDPattern is the contract: "sub-" + 32 lowercase hex chars.
var subIDPattern = regexp.MustCompile(`^sub-[0-9a-f]{32}$`)

// postSubBody is the decoded POST /api/subscriptions response.
type postSubBody struct {
	ID       string           `json:"id"`
	Filter   normalizedFilter `json:"filter_normalized"`
	rawError errorEnvelope
}

// doPostSub posts a subscription body and returns the status + decoded
// response.
func doPostSub(t *testing.T, p *Presenter, body string) (int, postSubBody) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/subscriptions", strings.NewReader(body))
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	var out postSubBody
	if rr.Code == http.StatusOK {
		if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
			t.Fatalf("decode POST response: %v", err)
		}
	} else if rr.Body.Len() > 0 {
		_ = json.NewDecoder(rr.Body).Decode(&out.rawError)
	}
	return rr.Code, out
}

// TestSubscriptionsCreate_OK asserts a valid POST returns 200, a
// well-formed id, the normalized filter, and registers the subscription
// in the hub.
func TestSubscriptionsCreate_OK(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()

	code, body := doPostSub(t, p, `{"filter":{"status":["failed"],"agents":["nedi","neda"]}}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if !subIDPattern.MatchString(body.ID) {
		t.Fatalf("id = %q, want sub-<32 hex>", body.ID)
	}
	if len(body.Filter.Status) != 1 || body.Filter.Status[0] != "failed" {
		t.Fatalf("filter_normalized.status = %v, want [failed]", body.Filter.Status)
	}
	// Normalized arrays are sorted.
	if len(body.Filter.Agents) != 2 || body.Filter.Agents[0] != "neda" || body.Filter.Agents[1] != "nedi" {
		t.Fatalf("filter_normalized.agents = %v, want [neda nedi]", body.Filter.Agents)
	}
	if !p.hub.Has(body.ID) {
		t.Fatalf("hub does not know subscription %s", body.ID)
	}
	if !p.subs.has(body.ID) {
		t.Fatalf("manager does not know subscription %s", body.ID)
	}
}

// TestSubscriptionsCreate_UniqueIDs asserts two POSTs mint distinct ids.
func TestSubscriptionsCreate_UniqueIDs(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	_, a := doPostSub(t, p, `{"filter":{}}`)
	_, b := doPostSub(t, p, `{"filter":{}}`)
	if a.ID == b.ID {
		t.Fatalf("ids collided: %s", a.ID)
	}
}

// TestSubscriptionsCreate_BadFilter asserts the filter validation rules
// surface as 400 BAD_REQUEST.
func TestSubscriptionsCreate_BadFilter(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	cases := []string{
		`{"filter":{"agents":[]}}`,
		`{"filter":{"models":[""]}}`,
		`{"filter":{"time_range":{"from":9,"to":1}}}`,
		`{"filter":{"unknown":1}}`,
		`{"bogus":1}`,
		`not json`,
	}
	for _, body := range cases {
		code, out := doPostSub(t, p, body)
		if code != http.StatusBadRequest {
			t.Fatalf("POST %s: status = %d, want 400", body, code)
		}
		if out.rawError.Error.Code != CodeBadRequest {
			t.Fatalf("POST %s: code = %q, want BAD_REQUEST", body, out.rawError.Error.Code)
		}
	}
}

// TestSubscriptionsCreate_MethodGate asserts non-POST methods are 405.
func TestSubscriptionsCreate_MethodGate(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodPatch} {
		req := httptest.NewRequest(m, "/api/subscriptions", nil)
		rr := httptest.NewRecorder()
		p.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s /api/subscriptions: status = %d, want 405", m, rr.Code)
		}
	}
}

// TestSubscriptionsDelete_Idempotent asserts DELETE returns 204 for both a
// known and an unknown id, and that a known id is removed from the hub +
// manager.
func TestSubscriptionsDelete_Idempotent(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	_, created := doPostSub(t, p, `{"filter":{}}`)

	del := func(id string) int {
		req := httptest.NewRequest(http.MethodDelete, "/api/subscriptions/"+id, nil)
		rr := httptest.NewRecorder()
		p.Handler().ServeHTTP(rr, req)
		return rr.Code
	}

	if code := del(created.ID); code != http.StatusNoContent {
		t.Fatalf("DELETE known id: status = %d, want 204", code)
	}
	if p.hub.Has(created.ID) {
		t.Fatalf("hub still knows deleted subscription %s", created.ID)
	}
	if p.subs.has(created.ID) {
		t.Fatalf("manager still knows deleted subscription %s", created.ID)
	}
	// Idempotent: deleting again is still 204.
	if code := del(created.ID); code != http.StatusNoContent {
		t.Fatalf("DELETE again: status = %d, want 204", code)
	}
	// Unknown id is also 204.
	if code := del("sub-deadbeefdeadbeefdeadbeefdeadbeef"); code != http.StatusNoContent {
		t.Fatalf("DELETE unknown: status = %d, want 204", code)
	}
}

// TestSubscriptionsDelete_BadID asserts a control char in the path id is a
// 400 (mirrors the Chunk-12 path-id rule), and the method gate rejects
// non-DELETE.
func TestSubscriptionsDelete_Guards(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()

	// %01 is a control char in the path id.
	req := httptest.NewRequest(http.MethodDelete, "/api/subscriptions/a%01b", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("DELETE control-char id: status = %d, want 400 (body=%q)", rr.Code, rr.Body.String())
	}

	// GET on the {id} route is not allowed (only DELETE).
	req2 := httptest.NewRequest(http.MethodGet, "/api/subscriptions/sub-deadbeefdeadbeefdeadbeefdeadbeef", nil)
	rr2 := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/subscriptions/{id}: status = %d, want 405", rr2.Code)
	}
}
