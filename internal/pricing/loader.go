package pricing

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
)

// namePattern bounds provider and model names. The pattern matches
// the JSON Schema for provider names and models so the schema and Go
// loader agree. Both lower- and upper-case are allowed because lookup
// is case-folded; the canonical name in pricing.json preserves the
// case the seed declares.
var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]*$`)

// schemaVersion is the only schema version this package accepts. Older
// versions are not auto-migrated; the refresh script regenerates from
// scratch when the operator opts in.
const schemaVersion = 2

// dateLayout is the YYYY-MM-DD layout used by tier.effective_date. The
// loader parses dates once at startup and stores them as time.Time so
// the hot path is an integer compare.
const dateLayout = "2006-01-02"

// rawDoc mirrors the on-disk JSON shape. Fields use json tags only;
// no validation lives here — that is loader.validate's job so errors
// carry full context (which provider, which model, which tier).
type rawDoc struct {
	Version     int           `json:"version"`
	SchemaURL   string        `json:"schema_url"`
	GeneratedAt string        `json:"generated_at"`
	GeneratedBy string        `json:"generated_by"`
	Currency    string        `json:"currency"`
	Providers   []rawProvider `json:"providers"`
}

type rawProvider struct {
	Name    string     `json:"name"`
	Aliases []string   `json:"aliases,omitempty"`
	Models  []rawModel `json:"models"`
}

type rawModel struct {
	Name    string    `json:"name"`
	Aliases []string  `json:"aliases,omitempty"`
	CtxMax  int64     `json:"ctx_max,omitempty"`
	Tiers   []rawTier `json:"tiers"`
}

type rawTier struct {
	EffectiveDate string    `json:"effective_date"`
	CitationURL   string    `json:"citation_url"`
	Source        string    `json:"source"`
	Prices        rawPrices `json:"prices"`
}

// rawPrices uses pointer fields for the required prices so absent and
// zero are distinguishable. The schema requires input_per_million and
// output_per_million; accepting them as Go-zero would silently price a
// malformed tier at zero. Optional fields stay as plain float64
// because absent and zero are semantically equivalent there (the
// provider does not differentiate that token class).
type rawPrices struct {
	InputPerMillion        *float64 `json:"input_per_million"`
	OutputPerMillion       *float64 `json:"output_per_million"`
	CacheReadPerMillion    float64  `json:"cache_read_per_million,omitempty"`
	CacheWritePerMillion   float64  `json:"cache_write_per_million,omitempty"`
	CacheWrite5mPerMillion float64  `json:"cache_write_5m_per_million,omitempty"`
	CacheWrite1hPerMillion float64  `json:"cache_write_1h_per_million,omitempty"`
	ReasoningPerMillion    float64  `json:"reasoning_per_million,omitempty"`
}

// resolvedPrices is the in-memory representation after validation
// (required fields proven present + non-negative). It carries plain
// floats so the hot path never dereferences a pointer.
type resolvedPrices struct {
	InputPerMillion        float64
	OutputPerMillion       float64
	CacheReadPerMillion    float64
	CacheWritePerMillion   float64
	CacheWrite5mPerMillion float64
	CacheWrite1hPerMillion float64
	ReasoningPerMillion    float64
}

// tier is the in-memory representation. effectiveAt is parsed once at
// load so resolveTier never re-parses on the hot path.
type tier struct {
	effectiveDate string
	effectiveAt   time.Time
	citationURL   string
	source        string
	prices        resolvedPrices
}

// modelEntry holds the tiers for a single canonical model after
// resolution + sorting.
type modelEntry struct {
	provider string
	model    string
	ctxMax   int64
	tiers    []tier
}

// parseDoc parses raw JSON, validates it, and returns the lookup map
// keyed by (lower-case provider, lower-case model) including alias
// expansion. The returned map shares modelEntry pointers across alias
// keys so updates remain consistent (though entries are immutable
// post-load). Unknown top-level fields are rejected via
// DisallowUnknownFields so a typo in the refresh script's output is
// caught at load time instead of being silently ignored.
//
// Iter-8 fix iter8-3: a pre-decode raw scan (rejectNullsInOptionals,
// loader_null_check.go) rejects JSON `null` for schema fields
// declared as non-nullable. Go's `encoding/json` decodes `null` into
// the zero value of the target type — `[]string` becomes nil,
// `int64` becomes 0, plain `float64` becomes 0 — and validateDoc
// cannot tell that from "field absent". The schema and the jq
// validator both reject `aliases: null`, `ctx_max: null`, and any
// optional price set to `null`; the Go loader must match per
// pricing.md §"Validation" (the loader is the runtime safety net).
func parseDoc(jsonBytes []byte) (map[modelKey]*modelEntry, *rawDoc, error) {
	if err := rejectNullsInOptionals(jsonBytes); err != nil {
		return nil, nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(jsonBytes))
	dec.DisallowUnknownFields()
	var doc rawDoc
	if err := dec.Decode(&doc); err != nil {
		return nil, nil, fmt.Errorf("pricing: parse json: %w", err)
	}
	// json.Decoder.Decode consumes exactly one top-level JSON value and
	// silently leaves any trailing content unread. The schema declares a single
	// document; concatenated JSON values (e.g. `{...}\n{}`) are a
	// corrupted seed and must be rejected by the runtime safety net,
	// not just by the jq validator. A second Decode call should
	// return io.EOF; anything else (including a second successful
	// decode) is a hard failure.
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, nil, fmt.Errorf("pricing: trailing JSON content after document")
		}
		return nil, nil, fmt.Errorf("pricing: failed to verify EOF: %w", err)
	}
	if err := validateDoc(&doc); err != nil {
		return nil, nil, err
	}
	lookup, err := buildLookup(&doc)
	if err != nil {
		return nil, nil, err
	}
	return lookup, &doc, nil
}

// validateDoc walks the parsed doc and rejects any structural problem
// the JSON parser missed (version mismatch, empty tiers, malformed
// dates, duplicate effective_date within a model, negative prices,
// negative ctx_max, empty schema_url). Errors carry full
// "provider X model Y tier Z" context.
func validateDoc(doc *rawDoc) error {
	if err := validateDocHeader(doc); err != nil {
		return err
	}
	seenProviders := make(map[string]bool, len(doc.Providers))
	for pi := range doc.Providers {
		if err := validateProvider(&doc.Providers[pi], pi, seenProviders); err != nil {
			return err
		}
	}
	return nil
}

// validateDocHeader checks the document-level fields (schema version, the
// required string fields, currency, and the non-empty providers invariant)
// before any per-provider walk. Error messages and check order are identical
// to the original inline block.
func validateDocHeader(doc *rawDoc) error {
	if doc.Version != schemaVersion {
		return fmt.Errorf("pricing: unsupported schema version %d (want %d)", doc.Version, schemaVersion)
	}
	if doc.SchemaURL == "" {
		return fmt.Errorf("pricing: schema_url is required")
	}
	if doc.Currency == "" {
		return fmt.Errorf("pricing: currency is required")
	}
	if doc.Currency != "USD" {
		return fmt.Errorf("pricing: only USD is supported in v2 (got %q)", doc.Currency)
	}
	if len(doc.Providers) == 0 {
		return fmt.Errorf("pricing: providers[] is empty")
	}
	if doc.GeneratedAt == "" {
		return fmt.Errorf("pricing: generated_at is required")
	}
	if _, err := time.Parse(time.RFC3339, doc.GeneratedAt); err != nil {
		return fmt.Errorf("pricing: generated_at %q is not RFC3339: %w", doc.GeneratedAt, err)
	}
	if doc.GeneratedBy == "" {
		return fmt.Errorf("pricing: generated_by is required")
	}
	return nil
}

// validateProvider validates one provider (name presence/pattern, alias
// patterns, case-folded uniqueness via seenProviders, non-empty models) and
// walks its models. pi is the provider index for the empty-name message.
func validateProvider(p *rawProvider, pi int, seenProviders map[string]bool) error {
	if p.Name == "" {
		return fmt.Errorf("pricing: providers[%d].name is empty", pi)
	}
	if !namePattern.MatchString(p.Name) {
		return fmt.Errorf("pricing: invalid provider name %q (must match %s)", p.Name, namePattern.String())
	}
	for ai, a := range p.Aliases {
		if !namePattern.MatchString(a) {
			return fmt.Errorf("pricing: provider %q aliases[%d] %q is invalid (must match %s)", p.Name, ai, a, namePattern.String())
		}
	}
	pn := strings.ToLower(p.Name)
	if seenProviders[pn] {
		return fmt.Errorf("pricing: duplicate provider %q", p.Name)
	}
	seenProviders[pn] = true
	if len(p.Models) == 0 {
		return fmt.Errorf("pricing: provider %q has no models[]", p.Name)
	}
	seenModels := make(map[string]bool, len(p.Models))
	for mi := range p.Models {
		if err := validateModel(p.Name, &p.Models[mi], mi, seenModels); err != nil {
			return err
		}
	}
	return nil
}

// validateModel validates one model under provider provName (name
// presence/pattern, alias patterns, non-negative ctx_max, case-folded
// uniqueness via seenModels, non-empty tiers) and walks its tiers
// (effective_date presence/format/uniqueness, citation_url, source, prices).
// mi is the model index for the empty-name message.
func validateModel(provName string, m *rawModel, mi int, seenModels map[string]bool) error {
	if m.Name == "" {
		return fmt.Errorf("pricing: provider %q models[%d].name is empty", provName, mi)
	}
	if !namePattern.MatchString(m.Name) {
		return fmt.Errorf("pricing: provider %q invalid model name %q (must match %s)", provName, m.Name, namePattern.String())
	}
	for ai, a := range m.Aliases {
		if !namePattern.MatchString(a) {
			return fmt.Errorf("pricing: provider %q model %q aliases[%d] %q is invalid (must match %s)", provName, m.Name, ai, a, namePattern.String())
		}
	}
	if m.CtxMax < 0 {
		return fmt.Errorf("pricing: provider %q model %q ctx_max is negative (%d)", provName, m.Name, m.CtxMax)
	}
	mn := strings.ToLower(m.Name)
	if seenModels[mn] {
		return fmt.Errorf("pricing: provider %q has duplicate model %q", provName, m.Name)
	}
	seenModels[mn] = true
	if len(m.Tiers) == 0 {
		return fmt.Errorf("pricing: provider %q model %q has no tiers[]", provName, m.Name)
	}
	seenDates := make(map[string]bool, len(m.Tiers))
	for ti := range m.Tiers {
		t := &m.Tiers[ti]
		if t.EffectiveDate == "" {
			return fmt.Errorf("pricing: provider %q model %q tiers[%d].effective_date is empty", provName, m.Name, ti)
		}
		if _, err := time.Parse(dateLayout, t.EffectiveDate); err != nil {
			return fmt.Errorf("pricing: provider %q model %q tiers[%d].effective_date %q is not YYYY-MM-DD: %w", provName, m.Name, ti, t.EffectiveDate, err)
		}
		if seenDates[t.EffectiveDate] {
			return fmt.Errorf("pricing: provider %q model %q has duplicate effective_date %q", provName, m.Name, t.EffectiveDate)
		}
		seenDates[t.EffectiveDate] = true
		if t.CitationURL == "" {
			return fmt.Errorf("pricing: provider %q model %q tier %q is missing citation_url", provName, m.Name, t.EffectiveDate)
		}
		if t.Source == "" {
			return fmt.Errorf("pricing: provider %q model %q tier %q is missing source", provName, m.Name, t.EffectiveDate)
		}
		if err := validatePrices(provName, m.Name, t.EffectiveDate, &t.Prices); err != nil {
			return err
		}
	}
	return nil
}

// validatePrices enforces (1) required-field presence for
// input_per_million and output_per_million — a tier without these is
// rejected, never silently priced at zero, and (2) non-negativity on
// every numeric price field. The schema declares both invariants;
// validatePrices is the Go-side enforcement so a corrupted seed never
// loads.
func validatePrices(provider, model, date string, p *rawPrices) error {
	if p.InputPerMillion == nil {
		return fmt.Errorf("pricing: provider %q model %q tier %q is missing required input_per_million", provider, model, date)
	}
	if p.OutputPerMillion == nil {
		return fmt.Errorf("pricing: provider %q model %q tier %q is missing required output_per_million", provider, model, date)
	}
	checks := []struct {
		name string
		v    float64
	}{
		{"input_per_million", *p.InputPerMillion},
		{"output_per_million", *p.OutputPerMillion},
		{"cache_read_per_million", p.CacheReadPerMillion},
		{"cache_write_per_million", p.CacheWritePerMillion},
		{"cache_write_5m_per_million", p.CacheWrite5mPerMillion},
		{"cache_write_1h_per_million", p.CacheWrite1hPerMillion},
		{"reasoning_per_million", p.ReasoningPerMillion},
	}
	for _, c := range checks {
		if c.v < 0 {
			return fmt.Errorf("pricing: provider %q model %q tier %q has negative %s (%v)", provider, model, date, c.name, c.v)
		}
	}
	return nil
}

// resolveRawPrices materialises rawPrices (with pointer required
// fields) into the in-memory resolvedPrices the hot path reads. The
// caller has already invoked validatePrices, so the pointer fields
// are guaranteed non-nil; the nil check is defence-in-depth.
func resolveRawPrices(p *rawPrices) resolvedPrices {
	out := resolvedPrices{
		CacheReadPerMillion:    p.CacheReadPerMillion,
		CacheWritePerMillion:   p.CacheWritePerMillion,
		CacheWrite5mPerMillion: p.CacheWrite5mPerMillion,
		CacheWrite1hPerMillion: p.CacheWrite1hPerMillion,
		ReasoningPerMillion:    p.ReasoningPerMillion,
	}
	if p.InputPerMillion != nil {
		out.InputPerMillion = *p.InputPerMillion
	}
	if p.OutputPerMillion != nil {
		out.OutputPerMillion = *p.OutputPerMillion
	}
	return out
}

// modelKey is the case-folded lookup key. Both fields are lower-case.
type modelKey struct {
	provider string
	model    string
}

// buildLookup constructs the in-memory lookup map. Each (provider,
// model) pair gets one *modelEntry, registered under the canonical
// name and every alias (both provider-level and model-level).
//
// Tiers are sorted by effective_date DESC so resolveTier's linear scan
// terminates on the first hit.
//
// buildLookup detects alias collisions: if two aliases (across or
// within providers and models) would register the same (provider,
// model) key for different *modelEntry instances, an error is
// returned. Silent overwrite would route Cost() to the wrong tier.
func buildLookup(doc *rawDoc) (map[modelKey]*modelEntry, error) {
	out := make(map[modelKey]*modelEntry)
	// owner tracks (canonical-provider, canonical-model) per key so we
	// can produce a descriptive error when a different entry tries to
	// claim the same lookup key.
	type ownerID struct{ provider, model string }
	owner := make(map[modelKey]ownerID)
	for pi := range doc.Providers {
		p := &doc.Providers[pi]
		providerNames := append([]string{p.Name}, p.Aliases...)
		for mi := range p.Models {
			m := &p.Models[mi]
			entry := &modelEntry{
				provider: p.Name,
				model:    m.Name,
				ctxMax:   m.CtxMax,
				tiers:    make([]tier, 0, len(m.Tiers)),
			}
			for ti := range m.Tiers {
				rt := &m.Tiers[ti]
				// EffectiveDate validated in validateDoc, so Parse can
				// only fail on a programming error in validateDoc; the
				// _ pattern here is intentional and covered by tests.
				at, _ := time.Parse(dateLayout, rt.EffectiveDate)
				entry.tiers = append(entry.tiers, tier{
					effectiveDate: rt.EffectiveDate,
					effectiveAt:   at,
					citationURL:   rt.CitationURL,
					source:        rt.Source,
					prices:        resolveRawPrices(&rt.Prices),
				})
			}
			sort.Slice(entry.tiers, func(i, j int) bool {
				return entry.tiers[i].effectiveAt.After(entry.tiers[j].effectiveAt)
			})

			modelNames := append([]string{m.Name}, m.Aliases...)
			thisOwner := ownerID{provider: strings.ToLower(p.Name), model: strings.ToLower(m.Name)}
			for _, pn := range providerNames {
				for _, mn := range modelNames {
					k := modelKey{
						provider: strings.ToLower(pn),
						model:    strings.ToLower(mn),
					}
					if existing, ok := owner[k]; ok && existing != thisOwner {
						return nil, fmt.Errorf("pricing: alias collision: provider %q model %q alias key (%s,%s) is already claimed by provider %q model %q",
							p.Name, m.Name, k.provider, k.model, existing.provider, existing.model)
					}
					out[k] = entry
					owner[k] = thisOwner
				}
			}
		}
	}
	return out, nil
}
