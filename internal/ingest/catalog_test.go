package ingest

import (
	"context"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

func TestCatalog_AgentRowCreatedOnSessionStart(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase:    canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:     "s",
		RootNativeID: "s",
		Kind:         canonical.KindRoot,
		AgentName:    "research",
		Cwd:          "/work",
	})
	_ = tx.Commit()

	if got := scanInt(t, db, `SELECT session_count FROM catalog_agents WHERE source_format='aiagent_v3' AND name='research'`); got != 1 {
		t.Errorf("catalog_agents session_count = %d, want 1", got)
	}
	if got := scanInt(t, db, `SELECT session_count FROM catalog_cwds WHERE source_format='aiagent_v3' AND cwd='/work'`); got != 1 {
		t.Errorf("catalog_cwds session_count = %d, want 1", got)
	}
}

func TestCatalog_AgentRowIncrementsOnSecondSession(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})

	for i := 0; i < 3; i++ {
		tx, _ := db.BeginTx(ctx, nil)
		_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
			EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: uint64(i + 1), Ts: int64(1000 + i*10)},
			NativeID:  string(rune('a' + i)), RootNativeID: string(rune('a' + i)),
			Kind: canonical.KindRoot, AgentName: "research",
		})
		_ = tx.Commit()
	}
	if got := scanInt(t, db, `SELECT session_count FROM catalog_agents WHERE name='research'`); got != 3 {
		t.Errorf("session_count = %d, want 3", got)
	}
}

func TestCatalog_ToolDefaultsToBuiltinNamespace(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	_ = w.apply(ctx, tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpTool, Name: "read_file", // no namespace
	})
	_ = tx.Commit()

	if got := scanInt(t, db, `SELECT call_count FROM catalog_tools WHERE namespace='builtin' AND name='read_file'`); got != 1 {
		t.Errorf("namespace defaulted incorrectly")
	}
}

func TestCatalog_ProviderAliasPreserved(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "opencode:/tmp", "opencode", "/tmp")
	w := newWriter("opencode:/tmp", "opencode", "/tmp", NopPricer{})

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "opencode:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	_ = w.apply(ctx, tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: "opencode:/tmp", SourceSeq: 2, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "call",
		Provider: "openai", ProviderAlias: "my-alias", Model: "gpt-5",
	})
	_ = tx.Commit()

	if got := scanInt(t, db, `SELECT call_count FROM catalog_providers WHERE name='openai' AND alias='my-alias'`); got != 1 {
		t.Errorf("alias not stored: got call_count = %d", got)
	}
}

func TestCatalog_OnSessionStartedNoOpForEmptyMetadata(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
		// no AgentName, no Cwd
	})
	_ = tx.Commit()
	if got := scanInt(t, db, `SELECT COUNT(*) FROM catalog_agents`); got != 0 {
		t.Errorf("catalog_agents = %d, want 0", got)
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM catalog_cwds`); got != 0 {
		t.Errorf("catalog_cwds = %d, want 0", got)
	}
}
