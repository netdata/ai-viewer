package opencode

import "net/url"

// This file is the SINGLE SOURCE OF TRUTH for the opencode PayloadRef
// LocationURI grammar (SOW-0005 chunk D). It mirrors codex/claude_code, which
// keep URI construction in their own payloads.go rather than scattering it
// across the mapper. The mapper (mapper_turn.go) is pure and DB-agnostic; its
// built-in default (defaultPayloadURI) delegates here so there is exactly one
// place that knows the grammar.
//
// There is intentionally NO resolver/parser here. Nothing in the project reads
// an opencode-sqlite:// URI yet: the future /api/payloads resolver is a separate
// Phase-2 SOW. Building a parser now would be dead code (AGENTS.md
// "Runtime artifact discipline" / no half-built features).

// payloadURIScheme is the scheme for an opencode payload reference. The body is
// not copied into ai-viewer's database; the reference records WHERE to read it
// (which part row, which JSON field) so the future resolver can fetch it
// read-only on demand. Hostless + pathless: the owning database is resolved from
// the payload_ref's source_id, not embedded in the URI.
const payloadURIScheme = "opencode-sqlite"

// buildPayloadURI renders the canonical PayloadRef LocationURI for a body that
// lives in an opencode `part` row's `data` JSON. The grammar is:
//
//   - scheme `opencode-sqlite` (no host, no path);
//   - query params `part_id=<id>` and `field=<field>`;
//   - both values URL-encoded via net/url so a part id or field path containing
//     a reserved character (`&`, `=`, `?`, `#`, space, …) cannot corrupt the
//     query or be misread by the resolver.
//
// Produces exactly:
//
//	opencode-sqlite://?part_id=<id>&field=<field>
//
// `field` is a dotted path into the part's decoded `data` (e.g. "text",
// "state.input", "state.output"); the resolver SELECTs the owning source's
// `part.data` for `part_id` read-only and projects `field`. For the values
// opencode actually emits (Sonyflake part ids like `prt_...` and the fixed field
// names above) every character is URL-unreserved, so the encoded form is
// byte-identical to the pre-chunk-D literal concatenation — existing mapper
// goldens are unchanged.
func buildPayloadURI(partID, field string) string {
	return payloadURIScheme + "://?part_id=" + url.QueryEscape(partID) +
		"&field=" + url.QueryEscape(field)
}

func buildInputPayloadURI(inputID, field string) string {
	return payloadURIScheme + "://?input_id=" + url.QueryEscape(inputID) +
		"&field=" + url.QueryEscape(field)
}

func buildTableSelectorURI(table, id string) string {
	values := url.Values{}
	values.Set("table", table)
	values.Set("id", id)
	return payloadURIScheme + "://?" + values.Encode()
}
