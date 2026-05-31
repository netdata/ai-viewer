-- c_multi_provider: one session, two assistant turns using DIFFERENT providers
-- (AC#7). Turn 1 is providerID=anthropic/modelID=claude-x; turn 2 is
-- providerID=openai/modelID=gpt-y. Each turn's LLM op must carry its own
-- ProviderAlias verbatim and a canonical Provider (both are in
-- knownProviderAliases, so Provider==alias here). Both providers surface so the
-- downstream catalog seeds two provider rows.
--
-- This fixture ALSO pins the two-level cumulative-token model:
--   * per-op tokens reset per message (computeStepDeltas): turn1 op = 100/30,
--     turn2 op = 300/80 (each is the single step-finish's own cumulative).
--   * per-turn tokens are the message-level delta across the session: turn1 =
--     100/30 (first turn), turn2 = 200/50 (300-100, 80-30).
-- All ids/timestamps synthetic.

CREATE TABLE session (
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
	time_archived INTEGER, time_compacting INTEGER);

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
	(id, project_id, parent_id, slug, directory, title, version, agent, model, time_created, time_updated, time_archived)
VALUES
	('ses_multi01', 'prj_x', '', 'dual-vendor', '/work/proj', 'Two providers', '1.0.0', 'general',
	 '{"id":"claude-x","providerID":"anthropic"}', 1000, 9000, NULL);

INSERT INTO message (id, session_id, time_created, time_updated, data)
VALUES
	('msg_m1', 'ses_multi01', 2000, 4000,
	 '{"role":"assistant","providerID":"anthropic","modelID":"claude-x","agent":"general","cost":0.01,"tokens":{"input":100,"output":30,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":2000,"completed":4000},"finish":"stop"}'),
	('msg_m2', 'ses_multi01', 5000, 8000,
	 '{"role":"assistant","providerID":"openai","modelID":"gpt-y","agent":"general","cost":0.02,"tokens":{"input":300,"output":80,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":5000,"completed":8000},"finish":"stop"}');

INSERT INTO part (id, message_id, session_id, time_created, time_updated, data)
VALUES
	('prt_m1a', 'msg_m1', 'ses_multi01', 2100, 2100, '{"type":"step-start"}'),
	('prt_m1b', 'msg_m1', 'ses_multi01', 2200, 2200, '{"type":"text","text":"anthropic reply"}'),
	('prt_m1c', 'msg_m1', 'ses_multi01', 4000, 4000, '{"type":"step-finish","reason":"stop","cost":0.01,"tokens":{"input":100,"output":30,"reasoning":0,"cache":{"read":0,"write":0}}}'),
	('prt_m2a', 'msg_m2', 'ses_multi01', 5100, 5100, '{"type":"step-start"}'),
	('prt_m2b', 'msg_m2', 'ses_multi01', 5200, 5200, '{"type":"text","text":"openai reply"}'),
	('prt_m2c', 'msg_m2', 'ses_multi01', 8000, 8000, '{"type":"step-finish","reason":"stop","cost":0.02,"tokens":{"input":300,"output":80,"reasoning":0,"cache":{"read":0,"write":0}}}');
