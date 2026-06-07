package ingest

import (
	"database/sql"
	"testing"
)

// SOW-0055 characterization pins for catalog_migrate.go cascade behaviour.

// TestCatalog_CharacterizationCascadingIdentityCorrection pins the
// catalog_migrate.go cascade contract: a chain of OpStarted re-emits on the
// SAME (turn, seq) must drain the prior identity and re-book totals onto the
// new identity, with OpFinalized totals migrating through the chain.
func TestCatalog_CharacterizationCascadingIdentityCorrection(t *testing.T) {
	t.Parallel()
	const src = "codex:/tmp"
	w, db, ctx := sow55SetupWriter(t, src, "codex", "/tmp")
	tx := sow55Begin(t, ctx, db)
	sow55ApplyBatch(t, ctx, w, tx, sow55ToolCascadeEvents(src))
	sow55CommitTx(t, tx)

	assertToolCascadeIntermediateDrained(t, db)
	assertToolCascadeFinalIdentity(t, db)
	assertToolCascadeCostMigrated(t, db)
}

// TestCatalog_CharacterizationLLMCostMigratesAcrossCascade pins explicit cost
// movement on catalog_models AND catalog_providers when the LLM identity
// switches provider/model. The SOW-0055 split of addMigratedTotals /
// removeOpContribution must keep the cost column and its sign correct.
func TestCatalog_CharacterizationLLMCostMigratesAcrossCascade(t *testing.T) {
	t.Parallel()
	const src = "codex:/tmp"
	w, db, ctx := sow55SetupWriter(t, src, "codex", "/tmp")
	tx := sow55Begin(t, ctx, db)
	sow55ApplyBatch(t, ctx, w, tx, sow55LLMCascadeEvents(src))
	sow55CommitTx(t, tx)

	assertLLMProviderDrained(t, db)
	assertLLMProviderMigrated(t, db)
	assertLLMModelMigrated(t, db)
}

// ----- catalog cascade assertions -----

func assertToolCascadeIntermediateDrained(t *testing.T, db *sql.DB) {
	t.Helper()
	if got := scanInt(t, db, `SELECT COALESCE(call_count,0) FROM catalog_tools WHERE namespace='custom' AND name='search'`); got != 0 {
		t.Errorf("placeholder custom/search call_count = %d, want 0 (cascade drained)", got)
	}
	if got := scanInt(t, db, `SELECT COALESCE(call_count,0) FROM catalog_tools WHERE namespace='mcp:files' AND name='files.read'`); got != 0 {
		t.Errorf("intermediate mcp:files/files.read call_count = %d, want 0 (cascade drained)", got)
	}
}

func assertToolCascadeFinalIdentity(t *testing.T, db *sql.DB) {
	t.Helper()
	if got := scanInt(t, db, `SELECT call_count FROM catalog_tools WHERE namespace='mcp:files-v2' AND name='files.read_v2'`); got != 1 {
		t.Errorf("final mcp:files-v2/files.read_v2 call_count = %d, want 1 (one physical op = one row)", got)
	}
	if got := scanInt(t, db, `SELECT failure_count FROM catalog_tools WHERE namespace='mcp:files-v2' AND name='files.read_v2'`); got != 1 {
		t.Errorf("final tool failure_count = %d, want 1 (failure migrated through both corrections)", got)
	}
	if got := scanInt(t, db, `SELECT total_tokens_in FROM catalog_tools WHERE namespace='mcp:files-v2' AND name='files.read_v2'`); got != 7 {
		t.Errorf("final tool total_tokens_in = %d, want 7 (tokens migrated, not duplicated)", got)
	}
}

func assertToolCascadeCostMigrated(t *testing.T, db *sql.DB) {
	t.Helper()
	if got := scanFloat(t, db, `SELECT total_cost_usd FROM catalog_tools WHERE namespace='mcp:files-v2' AND name='files.read_v2'`); !floatNear(got, 0.5) {
		t.Errorf("final tool total_cost_usd = %v, want 0.5 (cost migrated through cascade)", got)
	}
	if got := scanFloat(t, db, `SELECT COALESCE(total_cost_usd,0) FROM catalog_tools WHERE namespace='custom' AND name='search'`); got != 0 {
		t.Errorf("stale tool total_cost_usd on placeholder = %v, want 0 (cascade drained)", got)
	}
}

// ----- LLM cascade assertions -----

func assertLLMProviderDrained(t *testing.T, db *sql.DB) {
	t.Helper()
	if got := scanInt(t, db, `SELECT COALESCE(call_count,0) FROM catalog_providers WHERE name='anthropic'`); got != 0 {
		t.Errorf("old provider call_count = %d, want 0 (cost migration must drain anthropic)", got)
	}
	if got := scanFloat(t, db, `SELECT COALESCE(total_cost_usd,0) FROM catalog_providers WHERE name='anthropic'`); got != 0 {
		t.Errorf("old provider total_cost_usd = %v, want 0 (cost must be migrated off)", got)
	}
}

func assertLLMProviderMigrated(t *testing.T, db *sql.DB) {
	t.Helper()
	if got := scanFloat(t, db, `SELECT total_cost_usd FROM catalog_providers WHERE name='openai'`); !floatNear(got, 1.25) {
		t.Errorf("new provider total_cost_usd = %v, want 1.25 (migrated, not duplicated)", got)
	}
	if got := scanInt(t, db, `SELECT call_count FROM catalog_providers WHERE name='openai'`); got != 1 {
		t.Errorf("new provider call_count = %d, want 1 (one physical op)", got)
	}
}

func assertLLMModelMigrated(t *testing.T, db *sql.DB) {
	t.Helper()
	if got := scanFloat(t, db, `SELECT COALESCE(total_cost_usd,0) FROM catalog_models WHERE provider='anthropic' AND name='claude-opus-4.7'`); got != 0 {
		t.Errorf("old model total_cost_usd = %v, want 0", got)
	}
	if got := scanFloat(t, db, `SELECT total_cost_usd FROM catalog_models WHERE provider='openai' AND name='gpt-5.5'`); !floatNear(got, 1.25) {
		t.Errorf("new model total_cost_usd = %v, want 1.25", got)
	}
	if got := scanInt(t, db, `SELECT call_count FROM catalog_models WHERE provider='openai' AND name='gpt-5.5'`); got != 1 {
		t.Errorf("new model call_count = %d, want 1", got)
	}
	if got := scanInt(t, db, `SELECT total_duration_us FROM catalog_models WHERE provider='openai' AND name='gpt-5.5'`); got != 900 {
		t.Errorf("new model total_duration_us = %d, want 900 (migrated)", got)
	}
}
