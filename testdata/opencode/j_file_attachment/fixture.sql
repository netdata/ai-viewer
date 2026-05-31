-- j_file_attachment: pins SOW-0005 round-4 P2-3 + round-6 P3-3 — a file part is a
-- user file ATTACHMENT, surfaced as an INF LogEntry carrying {filename, url, mime}
-- in its extras, NOT a PayloadRef. The canonical PayloadRefEvent.PayloadKind set
-- (internal/canonical/events.go) has no attachment kind, so the adapter must emit
-- NO PayloadRef for a file part (the removed "user_attachment" kind was a
-- canonical-contract violation). This is the end-to-end golden the unit tests
-- (mapper_test.go TestMapSession_FilePartLogEntry) lacked: the full load→map→golden
-- pipeline, asserting the LogEntry flows through and no non-canonical PayloadKind
-- appears anywhere in the stream (golden_invariants_test.go).
--
-- One ROOT session, one assistant turn: step-start (opens the LLM op) -> a file
-- part (the attachment) -> step-finish (closes the LLM op). The turn has a completed
-- ts and ≥1 step-finish, so it finalizes COMPLETED; NO archive + NO data.error =>
-- NO SessionFinalized (running session). The file part is scoped to the turn and the
-- open LLM op (OpSeq=1). The URL uses the example.invalid host (no operator data).
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
	('ses_file01', 'prj_x', '', 'tidy-heron', '/work/proj', 'File-attachment session', '1.0.0', 'general',
	 '{"id":"claude-x","providerID":"anthropic"}', 1000, 9000, NULL);

INSERT INTO message (id, session_id, time_created, time_updated, data)
VALUES
	('msg_j1', 'ses_file01', 2000, 9000,
	 '{"role":"assistant","providerID":"anthropic","modelID":"claude-x","agent":"general","cost":0.01,"tokens":{"input":200,"output":30,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":2000,"completed":9000},"finish":"stop"}');

INSERT INTO part (id, message_id, session_id, time_created, time_updated, data)
VALUES
	('prt_j01', 'msg_j1', 'ses_file01', 2100, 2100, '{"type":"step-start"}'),
	('prt_j02', 'msg_j1', 'ses_file01', 2300, 2300, '{"type":"file","mime":"image/png","filename":"diagram.png","url":"https://cdn.example.invalid/diagram.png"}'),
	('prt_j03', 'msg_j1', 'ses_file01', 9000, 9000, '{"type":"step-finish","reason":"stop","cost":0.01,"tokens":{"input":200,"output":30,"reasoning":0,"cache":{"read":0,"write":0}}}');
