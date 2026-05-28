package presenter

import (
	"encoding/base64"
	"testing"
)

// TestCursor_RoundTrip asserts encode → decode preserves the full keyset
// cursor (tuple + sort + order) exactly.
func TestCursor_RoundTrip(t *testing.T) {
	t.Parallel()
	in := pageCursor{TS: 1_700_000_000_000_000, ID: "abc-123", Sort: sortStartTS, Order: "desc"}
	enc := in.encode()
	if enc == "" {
		t.Fatal("encode produced empty string")
	}
	out, err := decodeCursor(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out != in {
		t.Fatalf("round-trip = %+v, want %+v", out, in)
	}
}

// TestCursor_DecodeRejectsMalformed asserts every structurally-invalid
// cursor errors out so the handler can return BAD_REQUEST. The empty
// string is NOT in this set: callers never decode it (an absent cursor
// means "first page"); that contract is pinned by
// TestCursor_EmptyMeansBeginning.
func TestCursor_DecodeRejectsMalformed(t *testing.T) {
	t.Parallel()
	b64 := func(s string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(s))
	}
	cases := []struct {
		name, in string
	}{
		{"not base64", "not-base64!!!"},
		{"valid base64 not json", b64("abc")},
		{"empty object missing all fields", b64(`{}`)},
		{"missing id", b64(`{"ts":123,"sort":"start_ts","order":"desc"}`)},
		{"zero ts", b64(`{"ts":0,"id":"x","sort":"start_ts","order":"desc"}`)},
		{"missing sort", b64(`{"ts":123,"id":"x","order":"desc"}`)},
		{"missing order", b64(`{"ts":123,"id":"x","sort":"start_ts"}`)},
		{"unknown field", b64(`{"ts":123,"id":"x","sort":"start_ts","order":"desc","evil":1}`)},
		{"trailing bytes", b64(`{"ts":123,"id":"x","sort":"start_ts","order":"desc"} junk`)},
		{"two objects", b64(`{"ts":1,"id":"a","sort":"start_ts","order":"desc"}{"ts":2,"id":"b","sort":"start_ts","order":"desc"}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := decodeCursor(tc.in); err == nil {
				t.Fatalf("decodeCursor(%q): want error, got nil", tc.in)
			}
		})
	}
}

// TestCursor_DecodeAcceptsComplete asserts a fully-populated cursor
// round-trips through decode without error.
func TestCursor_DecodeAcceptsComplete(t *testing.T) {
	t.Parallel()
	enc := base64.RawURLEncoding.EncodeToString(
		[]byte(`{"ts":123,"id":"x","sort":"start_ts","order":"asc"}`))
	out, err := decodeCursor(enc)
	if err != nil {
		t.Fatalf("decode complete cursor: %v", err)
	}
	if out.TS != 123 || out.ID != "x" || out.Sort != sortStartTS || out.Order != "asc" {
		t.Fatalf("decoded = %+v", out)
	}
}

// TestCursor_EmptyMeansBeginning asserts the zero-value cursor is the
// "from the beginning" sentinel: callers never decode the empty string;
// they only decode a non-empty supplied cursor.
func TestCursor_EmptyMeansBeginning(t *testing.T) {
	t.Parallel()
	var zero pageCursor
	if !zero.isZero() {
		t.Fatal("zero-value cursor must report isZero()")
	}
	nonZero := pageCursor{TS: 1, ID: "x", Sort: sortStartTS, Order: "desc"}
	if nonZero.isZero() {
		t.Fatal("non-zero cursor must not report isZero()")
	}
}
