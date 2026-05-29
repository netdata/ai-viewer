package pricing

import (
	"encoding/json"
	"fmt"
	"strings"
)

// rejectNullsInOptionals walks the raw JSON tree and rejects `null`
// at every schema position the JSON Schema declares as a typed value
// (not a nullable union). The schema declares `aliases` as an array,
// `ctx_max` as an integer, and every optional price field as a
// number — none of them list `null` as a valid value. json.Unmarshal
// without this gate would silently turn `aliases: null` into a `nil`
// slice, `ctx_max: null` into `0`, and `optional_price: null` into
// `0` — none of which match the schema. This function is the Go-side
// equivalent of the jq validator's `has(X) ⇒ type == "array"` /
// `nn_number` shape (scripts/lib/pricing-validate.jq:114-115).
func rejectNullsInOptionals(jsonBytes []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(jsonBytes, &raw); err != nil {
		// A non-object root is caught by the strict decode in parseDoc;
		// returning nil here lets the strict error message win.
		return nil
	}
	provRaw, ok := raw["providers"]
	if !ok || isJSONNull(provRaw) {
		return nil // Strict decode catches missing/null providers.
	}
	var providers []map[string]json.RawMessage
	if err := json.Unmarshal(provRaw, &providers); err != nil {
		return nil // Strict decode catches non-array providers.
	}
	for pi, p := range providers {
		pName := stringOrIndex(p, "name", pi)
		if err := rejectNullField(p, "aliases", fmt.Sprintf("provider %s", pName)); err != nil {
			return err
		}
		if err := walkModels(p["models"], pName); err != nil {
			return err
		}
	}
	return nil
}

// walkModels iterates the provider's models[] and applies the
// null-rejection rules to each model's typed fields plus its
// nested tier.prices object.
func walkModels(modelsRaw json.RawMessage, pName string) error {
	if len(modelsRaw) == 0 || isJSONNull(modelsRaw) {
		return nil
	}
	var models []map[string]json.RawMessage
	if err := json.Unmarshal(modelsRaw, &models); err != nil {
		return nil
	}
	for mi, m := range models {
		mName := stringOrIndex(m, "name", mi)
		ctx := fmt.Sprintf("provider %s model %s", pName, mName)
		for _, field := range nullableRejectedFields {
			if err := rejectNullField(m, field, ctx); err != nil {
				return err
			}
		}
		if err := rejectNullsInTiers(m["tiers"], ctx); err != nil {
			return err
		}
	}
	return nil
}

// nullableRejectedFields lists the model-level fields the schema
// declares as typed values (not nullable). `tiers` is handled via
// rejectNullsInTiers because we recurse into it.
var nullableRejectedFields = []string{"aliases", "ctx_max"}

// optionalPriceFields enumerates the schema's optional numeric price
// fields. Each one is declared `type: number`; null is not allowed.
// `input_per_million` and `output_per_million` are required and
// already enforced by validateDoc's pointer-nil check; they are not
// listed here because absence is rejected by a different code path.
var optionalPriceFields = []string{
	"cache_read_per_million",
	"cache_write_per_million",
	"cache_write_5m_per_million",
	"cache_write_1h_per_million",
	"reasoning_per_million",
}

func rejectNullsInTiers(tiersRaw json.RawMessage, modelCtx string) error {
	if len(tiersRaw) == 0 || isJSONNull(tiersRaw) {
		return nil
	}
	var tiers []map[string]json.RawMessage
	if err := json.Unmarshal(tiersRaw, &tiers); err != nil {
		return nil
	}
	for ti, t := range tiers {
		ctx := fmt.Sprintf("%s tiers[%d]", modelCtx, ti)
		pricesRaw, ok := t["prices"]
		if !ok || isJSONNull(pricesRaw) {
			continue
		}
		var prices map[string]json.RawMessage
		if err := json.Unmarshal(pricesRaw, &prices); err != nil {
			continue
		}
		for _, field := range optionalPriceFields {
			if err := rejectNullField(prices, field, ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

// rejectNullField returns an error when the named key in obj is the
// literal JSON `null`. Missing key is accepted (the field is optional
// in the schema); a typed value is accepted (validateDoc verifies the
// type later); only `null` is rejected.
func rejectNullField(obj map[string]json.RawMessage, key, contextStr string) error {
	v, ok := obj[key]
	if !ok {
		return nil
	}
	if isJSONNull(v) {
		return fmt.Errorf("pricing: %s field %q is null; schema declares a typed value, not nullable", contextStr, key)
	}
	return nil
}

// isJSONNull returns true when the raw message decodes to the JSON
// literal `null` (after whitespace trimming).
func isJSONNull(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s == "null"
}

// stringOrIndex extracts a string field for context-building. Falls
// back to a numeric index when the field is missing or non-string so
// error messages still identify the offending entry.
func stringOrIndex(obj map[string]json.RawMessage, key string, idx int) string {
	if raw, ok := obj[key]; ok {
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" {
			return fmt.Sprintf("%q", s)
		}
	}
	return fmt.Sprintf("[%d]", idx)
}
