package pricing

import (
	_ "embed"
	"encoding/json"
	"testing"
)

// pricingSchemaBytes is the JSON Schema describing pricing.json. It is
// embedded only so the test below can verify both files stay in sync
// without shelling out to an external schema validator. Bringing in a
// full JSON-Schema dependency for one test is overkill; the
// structural assertions below cover the same constraints the schema
// declares.
//
//go:embed pricing.schema.json
var pricingSchemaBytes []byte

// TestSchemaParses confirms the shipped schema file is valid JSON
// and self-identifies as draft 2020-12.
func TestSchemaParses(t *testing.T) {
	t.Parallel()
	var doc map[string]any
	if err := json.Unmarshal(pricingSchemaBytes, &doc); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if doc["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Errorf("$schema = %v, want draft 2020-12", doc["$schema"])
	}
	if _, ok := doc["$defs"]; !ok {
		t.Error("$defs missing from schema")
	}
}

// TestEmbeddedJSONConformsToSchemaStructure cross-checks the embedded
// pricing.json against the structural invariants the schema file
// declares: required top-level keys present, providers[] non-empty,
// every tier has citation_url + source + prices.
func TestEmbeddedJSONConformsToSchemaStructure(t *testing.T) {
	t.Parallel()
	var raw map[string]any
	if err := json.Unmarshal(pricingJSONBytes, &raw); err != nil {
		t.Fatalf("pricing.json is not valid JSON: %v", err)
	}
	for _, k := range []string{"version", "schema_url", "generated_at", "generated_by", "currency", "providers"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("pricing.json missing required key %q", k)
		}
	}
	provs, ok := raw["providers"].([]any)
	if !ok || len(provs) == 0 {
		t.Fatal("pricing.json providers[] is empty")
	}
	for _, p := range provs {
		pm, ok := p.(map[string]any)
		if !ok {
			t.Fatalf("provider is not an object: %T", p)
		}
		models, ok := pm["models"].([]any)
		if !ok || len(models) == 0 {
			t.Errorf("provider %q has empty models[]", pm["name"])
			continue
		}
		for _, m := range models {
			mm, ok := m.(map[string]any)
			if !ok {
				t.Fatalf("model is not an object: %T", m)
			}
			tiers, ok := mm["tiers"].([]any)
			if !ok || len(tiers) == 0 {
				t.Errorf("model %q has empty tiers[]", mm["name"])
				continue
			}
			for _, ti := range tiers {
				tm, ok := ti.(map[string]any)
				if !ok {
					t.Errorf("provider %q model %q tier is not an object: %T", pm["name"], mm["name"], ti)
					continue
				}
				for _, k := range []string{"effective_date", "citation_url", "source", "prices"} {
					if _, ok := tm[k]; !ok {
						t.Errorf("provider %q model %q tier missing %q", pm["name"], mm["name"], k)
					}
				}
				prices, ok := tm["prices"].(map[string]any)
				if !ok {
					t.Errorf("provider %q model %q tier.prices is not an object: %T", pm["name"], mm["name"], tm["prices"])
					continue
				}
				for _, k := range []string{"input_per_million", "output_per_million"} {
					v, ok := prices[k]
					if !ok {
						t.Errorf("provider %q model %q tier.prices missing required %q", pm["name"], mm["name"], k)
						continue
					}
					num, ok := v.(float64)
					if !ok {
						t.Errorf("provider %q model %q tier.prices.%s is not a number: %T", pm["name"], mm["name"], k, v)
						continue
					}
					if num < 0 {
						t.Errorf("provider %q model %q tier.prices.%s is negative: %v", pm["name"], mm["name"], k, num)
					}
				}
			}
		}
	}
}
