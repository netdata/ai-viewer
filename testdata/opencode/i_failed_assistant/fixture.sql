-- i_failed_assistant: pins SOW-0005 round-5 P3-1 — a session whose LAST assistant
-- message carries data.error finalizes as SessionFinalized(Status="failed") with
-- ErrorClass from error.name AND ErrorMessage from error.data.message.
--
-- opencode's AssistantError is a tagged union (NamedError.create →
-- {"name":<ErrorName>,"data":<DataSchema>}); every shipping variant except
-- MessageOutputLengthError carries a `message` string in `data`. Here the
-- assistant message errored with a MessageAbortedError whose data.message is
-- "request was aborted by the user" (the most common variant on the reference DB).
--
-- One ROOT session, one assistant turn: step-start (opens the LLM op) -> a
-- completed read tool -> step-finish (closes the LLM op). The assistant message
-- itself carries data.error, so the turn finalizes FAILED (ErrorClass=
-- MessageAbortedError; TurnFinalizedEvent has no ErrorMessage field) AND the
-- session finalizes FAILED (ErrorClass + ErrorMessage). No time_archived, so the
-- failed path (not the archived/completed path) decides the terminal.
-- All ids/timestamps/messages are synthetic and invented (SOW-0005 R5).

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
	('ses_err01', 'prj_x', '', 'amber-wolf', '/work/proj', 'Aborted session', '1.0.0', 'general',
	 '{"id":"claude-x","providerID":"anthropic"}', 1000, 9000, NULL);

INSERT INTO message (id, session_id, time_created, time_updated, data)
VALUES
	('msg_e1', 'ses_err01', 2000, 9000,
	 '{"role":"assistant","providerID":"anthropic","modelID":"claude-x","agent":"general","cost":0.01,"tokens":{"input":300,"output":40,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":2000,"completed":9000},"finish":"stop","error":{"name":"MessageAbortedError","data":{"message":"request was aborted by the user"}}}');

INSERT INTO part (id, message_id, session_id, time_created, time_updated, data)
VALUES
	('prt_e01', 'msg_e1', 'ses_err01', 2100, 2100, '{"type":"step-start"}'),
	('prt_e02', 'msg_e1', 'ses_err01', 2500, 2600, '{"type":"tool","callID":"call_e2","tool":"read","state":{"status":"completed","input":{"path":"/work/proj/main.go"},"output":"package main","time":{"start":2500,"end":2600}}}'),
	('prt_e03', 'msg_e1', 'ses_err01', 9000, 9000, '{"type":"step-finish","reason":"stop","cost":0.01,"tokens":{"input":300,"output":40,"reasoning":0,"cache":{"read":0,"write":0}}}');
