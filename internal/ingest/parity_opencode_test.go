package ingest

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/adapters/opencode"
	"github.com/netdata/ai-viewer/internal/canonical"
	"github.com/netdata/ai-viewer/internal/parity"
)

func TestOpencodeIngestArtifactsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := writeOpencodeParityFixtureDB(t)
	sourceID := "opencode:" + dbPath

	adapter, err := opencode.New(dbPath, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("opencode adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("opencode.New: %v", err)
	}

	events := make(chan canonical.Event, 256)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("opencode Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "opencode", dbPath); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyOpencodeEventsForParity(t, ctx, db, sourceID, dbPath, events)

	sourceArtifacts, err := parity.ExtractOpencodeSource(ctx, parity.OpencodeSourceOptions{
		DBPath:   dbPath,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractOpencodeSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterOpencodeParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterOpencodeParityArtifacts(canonicalArtifacts)
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassSessionMetadata); got != 2 {
		t.Fatalf("source session_metadata count = %d, want 2", got)
	}
	if got := countArtifactsByClass(canonicalArtifacts, parity.ClassSessionMetadata); got != 2 {
		t.Fatalf("canonical session_metadata count = %d, want 2", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassUserPrompt); got != 1 {
		t.Fatalf("source user_prompt count = %d, want 1", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassUserImage); got != 1 {
		t.Fatalf("source user_image count = %d, want 1", got)
	}
	if got := countArtifactsByClass(canonicalArtifacts, parity.ClassUserImage); got != 1 {
		t.Fatalf("canonical user_image count = %d, want 1", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassAssistantMessage); got != 1 {
		t.Fatalf("source assistant_message count = %d, want 1", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassToolRequest); got != 2 {
		t.Fatalf("source tool_request count = %d, want 2", got)
	}
	if got := countArtifactsByClass(canonicalArtifacts, parity.ClassToolRequest); got != 2 {
		t.Fatalf("canonical tool_request count = %d, want 2", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassSubagentLink); got != 1 {
		t.Fatalf("source subagent_link count = %d, want 1", got)
	}
	if got := countArtifactsByClass(canonicalArtifacts, parity.ClassSubagentLink); got != 1 {
		t.Fatalf("canonical subagent_link count = %d, want 1", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassPatchMetadata); got != 1 {
		t.Fatalf("source patch_metadata count = %d, want 1", got)
	}
	if got := countArtifactsByClass(canonicalArtifacts, parity.ClassPatchMetadata); got != 1 {
		t.Logf("llm op extras_json = %s", scanString(t, db, `SELECT IFNULL(extras_json, '') FROM ops WHERE kind='llm' ORDER BY seq LIMIT 1`))
		t.Fatalf("canonical patch_metadata count = %d, want 1", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassToolError); got != 1 {
		t.Fatalf("source tool_error count = %d, want 1", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassCompactionEvent); got != 1 {
		t.Fatalf("source compaction_event count = %d, want 1", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassAttachmentMetadata); got != 1 {
		t.Fatalf("source attachment_metadata count = %d, want 1", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassSystemOp); got != 2 {
		t.Fatalf("source system_op count = %d, want 2", got)
	}
	if got := countArtifactsByClass(canonicalArtifacts, parity.ClassSystemOp); got != 2 {
		t.Fatalf("canonical system_op count = %d, want 2", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassLogEntry); got != 5 {
		t.Fatalf("source log_entry count = %d, want 5", got)
	}
	if got := countArtifactsByClass(canonicalArtifacts, parity.ClassLogEntry); got != 5 {
		t.Fatalf("canonical log_entry count = %d, want 5", got)
	}
	if len(sourceArtifacts) != 31 {
		t.Fatalf("source artifact count = %d, want 31", len(sourceArtifacts))
	}
	if len(canonicalArtifacts) != 31 {
		t.Fatalf("canonical artifact count = %d, want 31", len(canonicalArtifacts))
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("opencode parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestOpencodeFailedAssistantArtifactsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := writeOpencodeFailedAssistantParityFixtureDB(t)
	sourceID := "opencode:" + dbPath

	adapter, err := opencode.New(dbPath, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("opencode adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("opencode.New: %v", err)
	}

	events := make(chan canonical.Event, 64)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("opencode Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "opencode", dbPath); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyOpencodeEventsForParity(t, ctx, db, sourceID, dbPath, events)

	sourceArtifacts, err := parity.ExtractOpencodeSource(ctx, parity.OpencodeSourceOptions{
		DBPath:   dbPath,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractOpencodeSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterOpencodeParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterOpencodeParityArtifacts(canonicalArtifacts)
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassLLMError); got != 1 {
		t.Fatalf("source llm_error count = %d, want 1", got)
	}
	if got := countArtifactsByClass(canonicalArtifacts, parity.ClassLLMError); got != 1 {
		t.Fatalf("canonical llm_error count = %d, want 1", got)
	}
	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("opencode failed-assistant parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func writeOpencodeParityFixtureDB(t *testing.T) string {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: dbPath}).String())
	if err != nil {
		t.Fatalf("open opencode fixture db: %v", err)
	}
	defer func() { _ = db.Close() }()

	statements := []string{
		`CREATE TABLE session (
			id TEXT PRIMARY KEY, project_id TEXT NOT NULL, parent_id TEXT,
			slug TEXT NOT NULL, directory TEXT NOT NULL, title TEXT NOT NULL,
			version TEXT NOT NULL, agent TEXT, model TEXT,
			cost REAL NOT NULL DEFAULT 0,
			tokens_input INTEGER NOT NULL DEFAULT 0,
			tokens_output INTEGER NOT NULL DEFAULT 0,
			tokens_reasoning INTEGER NOT NULL DEFAULT 0,
			tokens_cache_read INTEGER NOT NULL DEFAULT 0,
			tokens_cache_write INTEGER NOT NULL DEFAULT 0,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			time_archived INTEGER, time_compacting INTEGER)`,
		`CREATE TABLE message (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			data TEXT NOT NULL)`,
		`CREATE TABLE part (
			id TEXT PRIMARY KEY, message_id TEXT NOT NULL, session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			data TEXT NOT NULL)`,
		`CREATE TABLE session_message (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL, type TEXT NOT NULL,
			seq INTEGER NOT NULL, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			data TEXT NOT NULL)`,
		`CREATE TABLE session_input (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL, prompt TEXT NOT NULL,
			delivery TEXT, admitted_seq INTEGER NOT NULL, promoted_seq INTEGER,
			time_created INTEGER NOT NULL)`,
		`INSERT INTO session
			(id, project_id, parent_id, slug, directory, title, version, agent, model, time_created, time_updated, time_archived)
			VALUES
			('ses_open01', 'prj_x', '', 'calm-otter', '/work/proj', 'Opencode parity', '1.0.0', 'general',
			 '{"id":"claude-x","providerID":"anthropic"}', 1000, 9000, NULL),
			('child-session', 'prj_x', 'ses_open01', 'child-session', '/work/proj', 'Child session', '1.0.0', 'reviewer',
			 '{"id":"claude-child","providerID":"anthropic"}', 2650, 2660, NULL)`,
		`INSERT INTO message (id, session_id, time_created, time_updated, data)
			VALUES
			('msg_user', 'ses_open01', 1500, 1500,
			 '{"role":"user","time":{"created":1500}}'),
			('msg_assistant', 'ses_open01', 2000, 9000,
			 '{"role":"assistant","parentID":"msg_user","providerID":"anthropic","modelID":"claude-x","agent":"general","cost":0.02,"tokens":{"input":500,"output":80,"reasoning":0,"cache":{"read":100,"write":0}},"time":{"created":2000,"completed":9000},"finish":"stop"}')`,
		`INSERT INTO session_input
				(id, session_id, prompt, delivery, admitted_seq, promoted_seq, time_created)
				VALUES ('msg_user', 'ses_open01', '{"text":"summarize README.md","files":[{"uri":"file:///tmp/diagram.png","mime":"image/png","name":"diagram.png","description":"screen"},{"uri":"file:///tmp/readme.txt","mime":"text/plain","name":"readme.txt"}]}', 'delivered', 1, 1, 1500)`,
		`INSERT INTO part (id, message_id, session_id, time_created, time_updated, data)
			VALUES
			('prt_step_start', 'msg_assistant', 'ses_open01', 2100, 2100, '{"type":"step-start"}'),
			('prt_reason', 'msg_assistant', 'ses_open01', 2200, 2300, '{"type":"reasoning","text":"thinking","time":{"start":2200,"end":2300}}'),
			('prt_text', 'msg_assistant', 'ses_open01', 2400, 2400, '{"type":"text","text":"answer"}'),
			('prt_step_zpatch', 'msg_assistant', 'ses_open01', 2450, 2450, '{"type":"patch","hash":"abc123","files":["/work/proj/internal/a.go","/work/proj/internal/b.go"]}'),
			('prt_step_zcompaction', 'msg_assistant', 'ses_open01', 2700, 2700, '{"type":"compaction","auto":true}'),
			('prt_step_zretry', 'msg_assistant', 'ses_open01', 2800, 2800, '{"type":"retry","attempt":2,"error":{"name":"RateLimit"}}'),
			('prt_step_zfile', 'msg_assistant', 'ses_open01', 2900, 2900, '{"type":"file","filename":"diagram.png","url":"https://cdn.example.invalid/diagram.png","mime":"image/png"}'),
			('prt_tool', 'msg_assistant', 'ses_open01', 2500, 2600, '{"type":"tool","callID":"call_1","tool":"bash","state":{"status":"error","input":{"cmd":"cat","path":"README.md"},"output":"partial output","error":"permission denied","time":{"start":2500,"end":2600}}}'),
			('prt_task', 'msg_assistant', 'ses_open01', 2650, 2660, '{"type":"tool","callID":"call_task","tool":"task","state":{"status":"completed","input":{"prompt":"review"},"output":"done","metadata":{"sessionId":"child-session"},"time":{"start":2650,"end":2660}}}'),
			('prt_zz_step_finish', 'msg_assistant', 'ses_open01', 9000, 9000, '{"type":"step-finish","reason":"stop","cost":0.02,"tokens":{"input":500,"output":80,"reasoning":0,"cache":{"read":100,"write":0}}}')`,
		`INSERT INTO session_message (id, session_id, type, seq, time_created, time_updated, data)
			VALUES
			('evt_agent', 'ses_open01', 'agent-switched', 1, 1600, 1600, '{"agent":"reviewer"}'),
			('evt_model', 'ses_open01', 'model-switched', 2, 1700, 1700, '{"model":{"id":"gpt-5","providerID":"openai","variant":"fast"}}')`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec opencode fixture statement:\n%s\nerror: %v", stmt, err)
		}
	}
	return dbPath
}

func writeOpencodeFailedAssistantParityFixtureDB(t *testing.T) string {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: dbPath}).String())
	if err != nil {
		t.Fatalf("open opencode failed-assistant fixture db: %v", err)
	}
	defer func() { _ = db.Close() }()

	statements := []string{
		`CREATE TABLE session (
			id TEXT PRIMARY KEY, project_id TEXT NOT NULL, parent_id TEXT,
			slug TEXT NOT NULL, directory TEXT NOT NULL, title TEXT NOT NULL,
			version TEXT NOT NULL, agent TEXT, model TEXT,
			cost REAL NOT NULL DEFAULT 0,
			tokens_input INTEGER NOT NULL DEFAULT 0,
			tokens_output INTEGER NOT NULL DEFAULT 0,
			tokens_reasoning INTEGER NOT NULL DEFAULT 0,
			tokens_cache_read INTEGER NOT NULL DEFAULT 0,
			tokens_cache_write INTEGER NOT NULL DEFAULT 0,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			time_archived INTEGER, time_compacting INTEGER)`,
		`CREATE TABLE message (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			data TEXT NOT NULL)`,
		`CREATE TABLE part (
			id TEXT PRIMARY KEY, message_id TEXT NOT NULL, session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			data TEXT NOT NULL)`,
		`CREATE TABLE session_message (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL, type TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			data TEXT NOT NULL)`,
		`CREATE TABLE session_input (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL, prompt TEXT NOT NULL,
			delivery TEXT, admitted_seq INTEGER NOT NULL, promoted_seq INTEGER,
			time_created INTEGER NOT NULL)`,
		`INSERT INTO session
			(id, project_id, parent_id, slug, directory, title, version, agent, model, time_created, time_updated, time_archived)
			VALUES
			('ses_err01', 'prj_x', '', 'amber-wolf', '/work/proj', 'Aborted session', '1.0.0', 'general',
			 '{"id":"claude-x","providerID":"anthropic"}', 1000, 9000, NULL)`,
		`INSERT INTO message (id, session_id, time_created, time_updated, data)
			VALUES
			('msg_e1', 'ses_err01', 2000, 9000,
			 '{"role":"assistant","providerID":"anthropic","modelID":"claude-x","agent":"general","cost":0.01,"tokens":{"input":300,"output":40,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":2000,"completed":9000},"finish":"stop","error":{"name":"MessageAbortedError","data":{"message":"request was aborted by the user"}}}')`,
		`INSERT INTO part (id, message_id, session_id, time_created, time_updated, data)
			VALUES
			('prt_e01', 'msg_e1', 'ses_err01', 2100, 2100, '{"type":"step-start"}'),
			('prt_e03', 'msg_e1', 'ses_err01', 9000, 9000, '{"type":"step-finish","reason":"stop","cost":0.01,"tokens":{"input":300,"output":40,"reasoning":0,"cache":{"read":0,"write":0}}}')`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec opencode failed-assistant fixture statement:\n%s\nerror: %v", stmt, err)
		}
	}
	return dbPath
}

func applyOpencodeEventsForParity(t *testing.T, ctx context.Context, db *sql.DB, sourceID string, location string, events <-chan canonical.Event) {
	t.Helper()

	writer := newWriter(sourceID, "opencode", location, NopPricer{})
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	for event := range events {
		if err := writer.apply(ctx, tx, event); err != nil {
			_ = tx.Rollback()
			t.Fatalf("apply %T: %v", event, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	resolver := newResolver(db, silentLogger(), time.Minute)
	if err := resolver.linkOrphans(ctx); err != nil {
		t.Fatalf("linkOrphans: %v", err)
	}
}

func filterOpencodeParityArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	allowed := map[parity.ArtifactClass]struct{}{
		parity.ClassSessionBoundary:    {},
		parity.ClassTurnBoundary:       {},
		parity.ClassOpBoundary:         {},
		parity.ClassUserPrompt:         {},
		parity.ClassUserImage:          {},
		parity.ClassAssistantMessage:   {},
		parity.ClassReasoningText:      {},
		parity.ClassToolRequest:        {},
		parity.ClassToolResponse:       {},
		parity.ClassLLMError:           {},
		parity.ClassToolError:          {},
		parity.ClassSubagentLink:       {},
		parity.ClassSystemOp:           {},
		parity.ClassSessionMetadata:    {},
		parity.ClassCompactionEvent:    {},
		parity.ClassLogEntry:           {},
		parity.ClassAttachmentMetadata: {},
		parity.ClassPatchMetadata:      {},
	}
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if _, ok := allowed[artifact.Class]; ok && artifact.Adapter == "opencode" {
			out = append(out, artifact)
		}
	}
	return out
}
