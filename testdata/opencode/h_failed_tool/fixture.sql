-- h_failed_tool: pins SOW-0005 round-2 P1-C — an opencode tool whose
-- state.status == "error" must finalize as the CANONICAL op status "failed"
-- (NOT the non-canonical "error"), carrying the opencode detail in ErrorClass
-- (a class label) + ErrorMessage (state.error). Canonical op statuses are
-- running|completed|failed|cancelled|truncated (canonical-events.md:196).
--
-- One ROOT session, one assistant turn: step-start (opens the LLM op) -> a
-- bash tool that ERRORED (state.status="error", state.error="command failed")
-- -> step-finish (closes the LLM op). The turn itself carries NO data.error and
-- has a completed ts, so it finalizes COMPLETED; only the TOOL op is failed.
-- This isolates the op-status mapping from the turn/session failed path.
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
	('ses_fail01', 'prj_x', '', 'cross-lynx', '/work/proj', 'Failed-tool session', '1.0.0', 'general',
	 '{"id":"claude-x","providerID":"anthropic"}', 1000, 9000, NULL);

INSERT INTO message (id, session_id, time_created, time_updated, data)
VALUES
	('msg_f1', 'ses_fail01', 2000, 9000,
	 '{"role":"assistant","providerID":"anthropic","modelID":"claude-x","agent":"general","cost":0.01,"tokens":{"input":300,"output":40,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":2000,"completed":9000},"finish":"stop"}');

INSERT INTO part (id, message_id, session_id, time_created, time_updated, data)
VALUES
	('prt_f01', 'msg_f1', 'ses_fail01', 2100, 2100, '{"type":"step-start"}'),
	('prt_f02', 'msg_f1', 'ses_fail01', 2500, 2600, '{"type":"tool","callID":"call_f2","tool":"bash","state":{"status":"error","input":{"command":"make build"},"error":"command failed: exit status 2","time":{"start":2500,"end":2600}}}'),
	('prt_f03', 'msg_f1', 'ses_fail01', 9000, 9000, '{"type":"step-finish","reason":"stop","cost":0.01,"tokens":{"input":300,"output":40,"reasoning":0,"cache":{"read":0,"write":0}}}');
