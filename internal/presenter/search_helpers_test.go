package presenter

import (
	"database/sql"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSearchHelperParseRequestOwnsSearchParams_ClampsAndTrims(t *testing.T) {
	t.Parallel()

	req := mustParseSearchRequest(t, searchOwnedValues())
	if req.match != "needle" {
		t.Fatalf("match = %q, want needle", req.match)
	}
	if req.limit != maxSearchLimit {
		t.Fatalf("limit = %d, want clamp to %d", req.limit, maxSearchLimit)
	}
	if req.offset != 0 {
		t.Fatalf("offset = %d, want first-page offset 0", req.offset)
	}
}

func TestSearchHelperParseRequestOwnsSearchParams_ForcesAllScope(t *testing.T) {
	t.Parallel()

	req := mustParseSearchRequest(t, searchOwnedValues())
	if req.filter.group != groupAll {
		t.Fatalf("group = %q, want forced all-session search scope", req.filter.group)
	}
	if len(req.filter.agents) != 1 || req.filter.agents[0] != "worker" {
		t.Fatalf("agents = %v, want [worker]", req.filter.agents)
	}
	if req.filter.q != "" {
		t.Fatalf("session filter q = %q, want search-owned q stripped", req.filter.q)
	}
	if req.filter.limit != defaultLimit {
		t.Fatalf("session filter limit = %d, want shared parser default after search-owned limit stripped", req.filter.limit)
	}
}

func searchOwnedValues() url.Values {
	return url.Values{
		"q":      {"  needle  "},
		"limit":  {"9999"},
		"cursor": {""},
		"agents": {"worker"},
		"group":  {groupRoot},
	}
}

func mustParseSearchRequest(t *testing.T, values url.Values) searchRequest {
	t.Helper()

	req, err := parseSearchRequest(values, time.Unix(100, 0).UTC())
	if err != nil {
		t.Fatalf("parseSearchRequest: %v", err)
	}
	return req
}

func TestSearchHelperParseRequestCursorBinding(t *testing.T) {
	t.Parallel()
	now := time.Unix(100, 0).UTC()
	first, err := parseSearchRequest(url.Values{
		"q":      {"needle"},
		"limit":  {"2"},
		"agents": {"worker"},
	}, now)
	if err != nil {
		t.Fatalf("first parseSearchRequest: %v", err)
	}
	cursor := searchNextCursor(true, first.limit, first.offset, first.match, first.filter)
	if cursor == "" {
		t.Fatal("cursor empty, want minted cursor")
	}

	replay, err := parseSearchRequest(url.Values{
		"q":      {"needle"},
		"limit":  {"7"},
		"agents": {"worker"},
		"cursor": {cursor},
	}, now)
	if err != nil {
		t.Fatalf("replay parseSearchRequest: %v", err)
	}
	if replay.offset != int64(first.limit) {
		t.Fatalf("replay offset = %d, want %d", replay.offset, first.limit)
	}
	if replay.limit != 7 {
		t.Fatalf("replay limit = %d, want changed page size accepted", replay.limit)
	}

	if _, err := parseSearchRequest(url.Values{
		"q":      {"changed"},
		"agents": {"worker"},
		"cursor": {cursor},
	}, now); err == nil {
		t.Fatal("changed q with replayed cursor: got nil error, want BAD_REQUEST")
	}
}

func TestSearchLogHelperQueryBindsFilterAndPagination(t *testing.T) {
	t.Parallel()
	filter := sessionFilter{
		group:  groupAll,
		source: []string{"src1"},
	}
	query, args := buildSearchLogsQuery(filter, "needle", 3, 6)

	for _, want := range []string{
		"fts_logs MATCH ?",
		"src.fts5_index_logs = 1",
		"ORDER BY rank, fts_logs.log_id",
		"LIMIT ? OFFSET ?",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("query missing %q:\n%s", want, query)
		}
	}
	if strings.Contains(query, "needle") || strings.Contains(query, "src1") {
		t.Fatalf("query interpolated user values instead of placeholders:\n%s", query)
	}
	wantArgs := []any{"needle", "src1", 4, int64(6), "needle"}
	if len(args) != len(wantArgs) {
		t.Fatalf("args = %#v, want %#v", args, wantArgs)
	}
	for i := range wantArgs {
		if args[i] != wantArgs[i] {
			t.Fatalf("args[%d] = %#v, want %#v (all user values and paging are bound)", i, args[i], wantArgs[i])
		}
	}
}

func TestSearchLogHelperOpIDPointer(t *testing.T) {
	t.Parallel()
	ptr := searchLogOpID(sql.NullString{String: "op1", Valid: true})
	if ptr == nil || *ptr != "op1" {
		t.Fatalf("valid op id pointer = %v, want op1", ptr)
	}
	if got := searchLogOpID(sql.NullString{String: "ignored", Valid: false}); got != nil {
		t.Fatalf("invalid op id pointer = %v, want nil", got)
	}
}

func TestSearchLogHelperTrimPage(t *testing.T) {
	t.Parallel()
	rows := []searchLogRow{{LogID: 1}, {LogID: 2}, {LogID: 3}}

	page, hasMore := trimSearchLogRows(rows, 2)
	if !hasMore {
		t.Fatal("hasMore = false, want true when limit+1 row is present")
	}
	if len(page) != 2 || page[0].LogID != 1 || page[1].LogID != 2 {
		t.Fatalf("page = %+v, want log ids [1,2]", page)
	}

	page, hasMore = trimSearchLogRows(rows[:2], 2)
	if hasMore {
		t.Fatal("hasMore = true, want false for exact page without peek row")
	}
	if len(page) != 2 {
		t.Fatalf("exact page len = %d, want 2", len(page))
	}
}
