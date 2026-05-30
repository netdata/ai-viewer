-- a_happy: the baseline opencode session tree.
-- One ROOT session, one assistant turn whose parts exercise the full op chain:
--   step-start (opens the LLM op) -> reasoning op (+ llm_reasoning PayloadRef)
--   -> text part (llm_response PayloadRef on the LLM op) -> tool op (read, with a
--   tool_response PayloadRef) -> step-finish (closes the LLM op).
-- Pins: SessionStarted(root) -> TurnStarted -> the op/payload tree in part order
--   -> TurnFinalized. No archive + no data.error => NO SessionFinalized (opencode
--   never finalizes a running session; adapter-opencode.md "Canonical Model Gaps" #5).
-- All ids/timestamps are synthetic and invented (SOW-0005 R5).

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
	time_archived INTEGER);

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
	('ses_happy01', 'prj_x', '', 'calm-otter', '/work/proj', 'Happy session', '1.0.0', 'general',
	 '{"id":"claude-x","providerID":"anthropic"}', 1000, 9000, NULL);

INSERT INTO message (id, session_id, time_created, time_updated, data)
VALUES
	('msg_a1', 'ses_happy01', 2000, 9000,
	 '{"role":"assistant","providerID":"anthropic","modelID":"claude-x","agent":"general","cost":0.02,"tokens":{"input":500,"output":80,"reasoning":0,"cache":{"read":100,"write":0}},"time":{"created":2000,"completed":9000},"finish":"stop"}');

INSERT INTO part (id, message_id, session_id, time_created, time_updated, data)
VALUES
	('prt_a01', 'msg_a1', 'ses_happy01', 2100, 2100, '{"type":"step-start"}'),
	('prt_a02', 'msg_a1', 'ses_happy01', 2200, 2300, '{"type":"reasoning","text":"thinking it through","time":{"start":2200,"end":2300}}'),
	('prt_a03', 'msg_a1', 'ses_happy01', 2400, 2400, '{"type":"text","text":"the answer"}'),
	('prt_a04', 'msg_a1', 'ses_happy01', 2500, 2600, '{"type":"tool","callID":"call_a4","tool":"read","state":{"status":"completed","input":{"path":"/work/proj/main.go"},"output":"package main","time":{"start":2500,"end":2600}}}'),
	('prt_a05', 'msg_a1', 'ses_happy01', 9000, 9000, '{"type":"step-finish","reason":"stop","cost":0.02,"tokens":{"input":500,"output":80,"reasoning":0,"cache":{"read":100,"write":0}}}');
