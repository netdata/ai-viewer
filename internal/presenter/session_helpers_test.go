package presenter

import (
	"context"
	"net/http"
	"net/url"
	"testing"
)

func TestSessionDetailResponseLoader_HappyPathAndMissing(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedGraph(t, db, seedBase())

	resp, op, err := p.loadSessionDetailResponse(context.Background(), "rootA")
	if err != nil {
		t.Fatalf("load detail response: op=%q err=%v", op, err)
	}
	if op != "" {
		t.Fatalf("op = %q, want empty on success", op)
	}
	if resp.Session.ID != "rootA" {
		t.Fatalf("session id = %q, want rootA", resp.Session.ID)
	}
	if len(resp.Turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(resp.Turns))
	}
	if len(resp.ChildSessions) != 2 {
		t.Fatalf("child sessions = %d, want 2", len(resp.ChildSessions))
	}

	_, _, err = p.loadSessionDetailResponse(context.Background(), "missing")
	if !isNoRows(err) {
		t.Fatalf("missing session err = %v, want sql.ErrNoRows", err)
	}
}

func TestParseLogRequest_DefaultsAndSeverity_Defaults(t *testing.T) {
	t.Parallel()

	lf, limit, cursor, err := parseLogRequest(logRequest(url.Values{}), "rootA")
	if err != nil {
		t.Fatalf("parse default log request: %v", err)
	}
	if lf.id != "rootA" || len(lf.severities) != 0 {
		t.Fatalf("filter = %+v, want id rootA with no severities", lf)
	}
	if limit != defaultLimit {
		t.Fatalf("limit = %d, want %d", limit, defaultLimit)
	}
	if cursor.present {
		t.Fatalf("cursor.present = true, want false")
	}
}

func TestParseLogRequest_DefaultsAndSeverity_SeverityFilter(t *testing.T) {
	t.Parallel()

	v := url.Values{"limit": {"25"}, "severity": {"WRN,ERR"}}
	lf, limit, cursor, err := parseLogRequest(logRequest(v), "rootA")
	if err != nil {
		t.Fatalf("parse filtered log request: %v", err)
	}
	if limit != 25 {
		t.Fatalf("limit = %d, want 25", limit)
	}
	if cursor.present {
		t.Fatalf("cursor.present = true, want false")
	}
	if got := lf.severities; len(got) != 2 || got[0] != "WRN" || got[1] != "ERR" {
		t.Fatalf("severities = %#v, want [WRN ERR]", got)
	}
}

func logRequest(v url.Values) *http.Request {
	return &http.Request{URL: &url.URL{RawQuery: v.Encode()}}
}

func TestParseLogLimitValidation(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		raw  string
		want int
	}{
		{name: "default", raw: "", want: defaultLimit},
		{name: "custom", raw: "7", want: 7},
		{name: "max", raw: "1000", want: maxLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseLogLimit(tc.raw)
			if err != nil {
				t.Fatalf("parseLogLimit(%q): %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("parseLogLimit(%q) = %d, want %d", tc.raw, got, tc.want)
			}
		})
	}

	for _, raw := range []string{"abc", "0", "-1", "1001"} {
		t.Run("bad/"+raw, func(t *testing.T) {
			if _, err := parseLogLimit(raw); err == nil {
				t.Fatalf("parseLogLimit(%q): want error, got nil", raw)
			}
		})
	}
}

func TestLogPagingCursorParamValidation_Valid(t *testing.T) {
	t.Parallel()

	lf := logFilter{id: "rootA", severities: []string{"WRN", "ERR"}}
	good := pageCursor{
		TS:    123,
		ID:    "456",
		Sort:  logsSort,
		Order: logsOrder,
		FP:    lf.fingerprint(),
	}.encode()
	cursor, err := parseLogCursorParam(good, lf)
	if err != nil {
		t.Fatalf("parse valid cursor: %v", err)
	}
	if !cursor.present || cursor.ts != 123 || cursor.id != 456 {
		t.Fatalf("cursor = %+v, want present ts=123 id=456", cursor)
	}
}

func TestLogPagingCursorParamValidation_Empty(t *testing.T) {
	t.Parallel()

	lf := logFilter{id: "rootA", severities: []string{"WRN", "ERR"}}
	if cursor, err := parseLogCursorParam("", lf); err != nil || cursor.present {
		t.Fatalf("empty cursor = %+v, %v; want absent cursor with nil error", cursor, err)
	}
}

func TestLogPagingCursorParamValidation_RejectsForeignOrder(t *testing.T) {
	t.Parallel()

	lf := logFilter{id: "rootA", severities: []string{"WRN", "ERR"}}
	foreign := pageCursor{
		TS:    123,
		ID:    "456",
		Sort:  sortStartTS,
		Order: "desc",
		FP:    lf.fingerprint(),
	}.encode()
	if _, err := parseLogCursorParam(foreign, lf); err == nil {
		t.Fatal("foreign-order cursor: want error, got nil")
	}
}

func TestLogPagingCursorParamValidation_RejectsNonNumericID(t *testing.T) {
	t.Parallel()

	lf := logFilter{id: "rootA", severities: []string{"WRN", "ERR"}}
	badID := pageCursor{
		TS:    123,
		ID:    "not-an-int",
		Sort:  logsSort,
		Order: logsOrder,
		FP:    lf.fingerprint(),
	}.encode()
	if _, err := parseLogCursorParam(badID, lf); err == nil {
		t.Fatal("non-numeric cursor id: want error, got nil")
	}
}

func TestSessionLogsResponseLoader_HappyPathAndMissing(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedGraph(t, db, seedBase())

	resp, op, err := p.loadSessionLogsResponse(context.Background(), logFilter{id: "rootA"}, 2, logsCursor{})
	if err != nil {
		t.Fatalf("load logs response: op=%q err=%v", op, err)
	}
	if op != "" {
		t.Fatalf("op = %q, want empty on success", op)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(resp.Items))
	}
	if resp.NextCursor == "" {
		t.Fatal("next cursor empty, want a cursor for the remaining row")
	}

	_, op, err = p.loadSessionLogsResponse(context.Background(), logFilter{id: "missing"}, 2, logsCursor{})
	if !isNoRows(err) {
		t.Fatalf("missing session err = %v, want sql.ErrNoRows", err)
	}
	if op != "" {
		t.Fatalf("missing session op = %q, want empty", op)
	}
}
