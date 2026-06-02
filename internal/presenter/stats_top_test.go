package presenter

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// topBody mirrors GET /api/stats/top.
type topBody struct {
	Dimension string `json:"dimension"`
	Metric    string `json:"metric"`
	Items     []struct {
		Key   string  `json:"key"`
		Value float64 `json:"value"`
	} `json:"items"`
}

func getTop(t *testing.T, p *Presenter, query string) (int, topBody, errorEnvelope) {
	t.Helper()
	return doStatsGet[topBody](t, p, "/api/stats/top", query)
}

// TestStatsTop_Ordering asserts items are ordered by value desc over the whole
// window (closed rollups + live open bucket). dimension=model, metric=calls:
// claude-opus has 3 ops (day25 + closed-hour-today + open-hour), gpt-5 has 1.
func TestStatsTop_Ordering(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedRollupFixture(t, db)
	materializeRollups(t, db)

	code, body, env := getTop(t, p, "dimension=model&metric=calls")
	if code != http.StatusOK {
		t.Fatalf("status=%d env=%+v", code, env)
	}
	if body.Dimension != "model" || body.Metric != "calls" {
		t.Fatalf("echo: dimension=%q metric=%q", body.Dimension, body.Metric)
	}
	if len(body.Items) != 2 {
		t.Fatalf("items=%+v, want 2 (claude-opus, gpt-5)", body.Items)
	}
	if body.Items[0].Key != "claude-opus" || body.Items[0].Value != 3 {
		t.Errorf("top item = %+v, want claude-opus=3", body.Items[0])
	}
	if body.Items[1].Key != "gpt-5" || body.Items[1].Value != 1 {
		t.Errorf("second item = %+v, want gpt-5=1", body.Items[1])
	}
}

// TestStatsTop_FastPathMatchesLiveFold asserts the top ranking is identical
// whether it takes the rollup fast path or the live fold over the same data
// (same parity contract as the aggregate, collapsed across buckets).
func TestStatsTop_FastPathMatchesLiveFold(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedRollupFixture(t, db)
	materializeRollups(t, db)

	dims := []string{"model", "provider", "tool", "agent", "cwd"}
	metrics := []string{"cost", "tokens_in", "tokens_out", "calls", "failures", "duration_us", "sessions"}
	for _, d := range dims {
		for _, m := range metrics {
			t.Run(d+"/"+m, func(t *testing.T) {
				base := "dimension=" + d + "&metric=" + m + "&n=200"
				_, fast, _ := getTop(t, p, base)
				_, live, _ := getTop(t, p, base+"&sources=aiagent_v3:/p,codex:/p")
				fm := topToMap(fast)
				lm := topToMap(live)
				if len(fm) != len(lm) {
					t.Fatalf("key count: fast=%d live=%d (fast=%+v live=%+v)", len(fm), len(lm), fast.Items, live.Items)
				}
				for k, fv := range fm {
					if lv := lm[k]; !floatEq(fv, lv) {
						t.Fatalf("key %q: fast=%v live=%v", k, fv, lv)
					}
				}
			})
		}
	}
}

func topToMap(b topBody) map[string]float64 {
	out := make(map[string]float64, len(b.Items))
	for _, it := range b.Items {
		out[it.Key] = it.Value
	}
	return out
}

// TestStatsTop_NClamp asserts n is clamped to [1,200] (n=0→1, n=999→200) and
// that the cap actually truncates the item list.
func TestStatsTop_NClamp(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedRollupFixture(t, db)
	materializeRollups(t, db)

	// n=0 clamps to 1: only the single highest model.
	code, body, _ := getTop(t, p, "dimension=model&metric=calls&n=0")
	if code != http.StatusOK {
		t.Fatalf("n=0 status=%d", code)
	}
	if len(body.Items) != 1 || body.Items[0].Key != "claude-opus" {
		t.Fatalf("n=0 items=%+v, want [claude-opus]", body.Items)
	}

	// n=999 clamps to 200: all (2) models returned, no error.
	code, body, _ = getTop(t, p, "dimension=model&metric=calls&n=999")
	if code != http.StatusOK {
		t.Fatalf("n=999 status=%d", code)
	}
	if len(body.Items) != 2 {
		t.Fatalf("n=999 items=%+v, want 2", body.Items)
	}
}

// TestStatsTop_DimensionEnum asserts top rejects total/source_format (the two
// group_by-only dimensions) and unknown dimensions with BAD_REQUEST, while the
// five op/session dimensions are accepted.
func TestStatsTop_DimensionEnum(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()

	for _, bad := range []string{"total", "source_format", "bogus"} {
		code, _, env := getTop(t, p, "dimension="+bad)
		if code != http.StatusBadRequest || env.Error.Code != CodeBadRequest {
			t.Errorf("dimension=%q: status=%d code=%q, want 400/BAD_REQUEST", bad, code, env.Error.Code)
		}
	}
	for _, ok := range []string{"model", "provider", "tool", "agent", "cwd"} {
		code, _, _ := getTop(t, p, "dimension="+ok)
		if code != http.StatusOK {
			t.Errorf("dimension=%q: status=%d, want 200", ok, code)
		}
	}
}

// TestStatsTop_BadParams covers the metric/n guards, 405, and HEAD.
func TestStatsTop_BadParams(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()

	code, _, env := getTop(t, p, "metric=bogus")
	if code != http.StatusBadRequest || env.Error.Code != CodeBadRequest {
		t.Errorf("metric=bogus: status=%d code=%q", code, env.Error.Code)
	}
	code, _, env = getTop(t, p, "n=abc")
	if code != http.StatusBadRequest || env.Error.Code != CodeBadRequest {
		t.Errorf("n=abc: status=%d code=%q", code, env.Error.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/stats/top", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status=%d, want 405", rr.Code)
	}

	headReq := httptest.NewRequest(http.MethodHead, "/api/stats/top", nil)
	headRR := httptest.NewRecorder()
	p.Handler().ServeHTTP(headRR, headReq)
	if headRR.Code != http.StatusOK || headRR.Body.Len() != 0 {
		t.Fatalf("HEAD status=%d bodyLen=%d, want 200/0", headRR.Code, headRR.Body.Len())
	}
}

// TestStatsTop_Empty asserts an empty DB returns 200 with an empty items array.
func TestStatsTop_Empty(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	code, body, _ := getTop(t, p, "")
	if code != http.StatusOK {
		t.Fatalf("status=%d", code)
	}
	if body.Items == nil {
		t.Fatal("items must serialize as [] not null")
	}
	if len(body.Items) != 0 {
		t.Fatalf("items=%+v, want empty", body.Items)
	}
	if body.Dimension != "model" || body.Metric != "cost" {
		t.Fatalf("defaults: dimension=%q metric=%q", body.Dimension, body.Metric)
	}
}
