-- e_cumulative_tokens: the cumulative->delta token regression (AC#3, the top
-- silent-defect guard). ONE assistant message, FOUR step-start/step-finish pairs
-- whose step-finish tokens are CUMULATIVE within the message:
--   input:  100, 250, 410, 400   output: 20, 50, 90, 80
-- The mapper's computeStepDeltas must emit per-LLM-op DELTAS:
--   op1 in=100 out=20   (first step = its own cumulative)
--   op2 in=150 out=30   (250-100, 50-20)
--   op3 in=160 out=40   (410-250, 90-50)
--   op4 in=0   out=0    (400<410 and 80<90 => negative delta CLAMPED to 0)
-- A regression to raw-value emission (100/250/410/400) would fail the golden.
-- The per-turn tokens are the message-level cumulative (input 400 / output 80);
-- this single turn is the first, so its delta equals its own cumulative.
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
	('ses_cumul01', 'prj_x', '', 'token-mole', '/work/proj', 'Cumulative tokens', '1.0.0', 'general',
	 '{"id":"claude-x","providerID":"anthropic"}', 1000, 9000, NULL);

INSERT INTO message (id, session_id, time_created, time_updated, data)
VALUES
	('msg_e1', 'ses_cumul01', 2000, 9000,
	 '{"role":"assistant","providerID":"anthropic","modelID":"claude-x","agent":"general","cost":0.04,"tokens":{"input":400,"output":80,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":2000,"completed":9000},"finish":"stop"}');

INSERT INTO part (id, message_id, session_id, time_created, time_updated, data)
VALUES
	('prt_e01', 'msg_e1', 'ses_cumul01', 2100, 2100, '{"type":"step-start"}'),
	('prt_e02', 'msg_e1', 'ses_cumul01', 2200, 2200, '{"type":"step-finish","reason":"tool-calls","cost":0.01,"tokens":{"input":100,"output":20,"reasoning":0,"cache":{"read":0,"write":0}}}'),
	('prt_e03', 'msg_e1', 'ses_cumul01', 3000, 3000, '{"type":"step-start"}'),
	('prt_e04', 'msg_e1', 'ses_cumul01', 3100, 3100, '{"type":"step-finish","reason":"tool-calls","cost":0.01,"tokens":{"input":250,"output":50,"reasoning":0,"cache":{"read":0,"write":0}}}'),
	('prt_e05', 'msg_e1', 'ses_cumul01', 4000, 4000, '{"type":"step-start"}'),
	('prt_e06', 'msg_e1', 'ses_cumul01', 4100, 4100, '{"type":"step-finish","reason":"tool-calls","cost":0.01,"tokens":{"input":410,"output":90,"reasoning":0,"cache":{"read":0,"write":0}}}'),
	('prt_e07', 'msg_e1', 'ses_cumul01', 5000, 5000, '{"type":"step-start"}'),
	('prt_e08', 'msg_e1', 'ses_cumul01', 9000, 9000, '{"type":"step-finish","reason":"stop","cost":0.01,"tokens":{"input":400,"output":80,"reasoning":0,"cache":{"read":0,"write":0}}}');
