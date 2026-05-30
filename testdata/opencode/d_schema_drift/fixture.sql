-- d_schema_drift: a pre-20260510033149_session_usage opencode schema. The
-- session table LACKS the optional columns added by later migrations:
--   agent, model, cost, tokens_input, tokens_output, tokens_reasoning,
--   tokens_cache_read, tokens_cache_write, time_archived, time_compacting
--   (10 columns; time_compacting added to the wanted set in SOW-0005 round-2 P2-E).
-- It keeps the REQUIRED id/time_created/time_updated (+ the always-present
-- project_id/parent_id/slug/directory/title/version), so introspectAll must
-- ACCEPT it (graceful degrade — adapter-opencode.md "Edge Cases" #1, AC#5), the
-- dynamic SELECT must OMIT the missing columns (never SELECT *), and the
-- emitted events must carry empty/zero session-level values for them:
--   SessionStarted.Model="" (session.model absent), .AgentName="" (session.agent
--   absent), and Extras WITHOUT providerID/variant (both come from session.model).
-- The LLM-op/turn token + provider values are UNAFFECTED: they come from the
-- message.data JSON body (modelID/providerID/tokens), which the drift does not
-- touch — so the op still carries Provider=anthropic/Model=claude-x and the turn
-- still carries its tokens.
--
-- The spec/AC#5 promise of "one INF log per missing optional column" IS wired in
-- production: tailer.go logMissingColumns iterates tableSchema.Missing right
-- after introspectAll in both scanLoop and tailLoop, emitting one INFO per
-- (table, column) via the Adapter.logger threaded from adapter.go Scan/Tail.
-- This fixture pins both halves: the graceful DEGRADE (accept + omit-columns +
-- zero values) via golden_invariants_test.go:TestGoldenInvariant_DSchemaDrift,
-- and the INF emission via TestGoldenInvariant_DSchemaDrift_MissingColumnsLoggedINF
-- (a record-capturing slog.Handler asserts the logged (table,column) set equals
-- the introspected Missing set). The INF set is a log, not a canonical event, so
-- it is correctly absent from expected.jsonl. All ids/timestamps synthetic.

CREATE TABLE session (
	id TEXT PRIMARY KEY, project_id TEXT NOT NULL, parent_id TEXT,
	slug TEXT NOT NULL, directory TEXT NOT NULL, title TEXT NOT NULL,
	version TEXT NOT NULL,
	time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL);

CREATE TABLE message (
	id TEXT PRIMARY KEY, session_id TEXT NOT NULL,
	time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
	data TEXT NOT NULL);

CREATE TABLE part (
	id TEXT PRIMARY KEY, message_id TEXT NOT NULL, session_id TEXT NOT NULL,
	time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
	data TEXT NOT NULL);

CREATE TABLE session_message (
	id TEXT PRIMARY KEY, session_id TEXT NOT NULL, type TEXT NOT NULL,
	time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
	data TEXT NOT NULL);

INSERT INTO session
	(id, project_id, parent_id, slug, directory, title, version, time_created, time_updated)
VALUES
	('ses_drift01', 'prj_x', '', 'old-badger', '/work/proj', 'Old schema session', '0.9.0', 1000, 6000);

INSERT INTO message (id, session_id, time_created, time_updated, data)
VALUES
	('msg_d1', 'ses_drift01', 2000, 6000,
	 '{"role":"assistant","providerID":"anthropic","modelID":"claude-x","agent":"general","cost":0.01,"tokens":{"input":60,"output":15,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":2000,"completed":6000},"finish":"stop"}');

INSERT INTO part (id, message_id, session_id, time_created, time_updated, data)
VALUES
	('prt_d1a', 'msg_d1', 'ses_drift01', 2100, 2100, '{"type":"step-start"}'),
	('prt_d1b', 'msg_d1', 'ses_drift01', 2200, 2200, '{"type":"text","text":"old reply"}'),
	('prt_d1c', 'msg_d1', 'ses_drift01', 6000, 6000, '{"type":"step-finish","reason":"stop","cost":0.01,"tokens":{"input":60,"output":15,"reasoning":0,"cache":{"read":0,"write":0}}}');
